package gateway_test

import (
	"context"
	"encoding/json"
	"maps"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"

	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/none"
)

// TestCallTool_DoesNotInjectContext is the load-bearing regression for
// Phase 8c's "visibility, not injection" rule. The script-supplied args
// must reach the upstream MCP server VERBATIM — the gateway must never
// merge Upstream.DefaultsJSON into the outbound tool arguments.
//
// We stand up a tiny mcp-go server that captures the arguments handed
// to its single "echo" tool. We seed an upstream pointing at it with a
// fat DefaultsJSON, then call Manager.CallTool with a sparse args map
// and assert the captured request equals the sparse map exactly — no
// extra keys from DefaultsJSON show up.
func TestCallTool_DoesNotInjectContext(t *testing.T) {
	var (
		mu       sync.Mutex
		captured map[string]any
	)
	fakeMCP := mcpserver.NewMCPServer("fake", "0.0.1", mcpserver.WithToolCapabilities(true))
	fakeMCP.AddTool(
		mcp.NewTool("echo", mcp.WithDescription("echoes args")),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			mu.Lock()
			args := req.GetArguments()
			// Defensive copy so test reads aren't racing with mcp-go internals.
			captured = make(map[string]any, len(args))
			maps.Copy(captured, args)
			mu.Unlock()
			return mcp.NewToolResultText("ok"), nil
		},
	)
	httpSrv := httptest.NewServer(mcpserver.NewStreamableHTTPServer(fakeMCP, mcpserver.WithStateLess(true)))
	defer httpSrv.Close()

	store := storagetest.OpenMigrated(t)
	logger := zaptest.NewLogger(t)
	tenant := seedTenant(t, store, "ctxtest", "zorg-ctxtest")

	up := seedUpstream(t, store, tenant.ID, "fake", httpSrv.URL)
	// Stuff DefaultsJSON with keys we'd notice if injection ever happened.
	setUpstreamDefaults(t, store, tenant.ID, up.ID, map[string]any{
		"cloudId":        "abc-123",
		"defaultProject": "FALLBACK",
	})

	registry := upstream.NewRegistry()
	registry.Register(none.New(nil))
	mgr, err := gateway.NewManager(gateway.ManagerOptions{
		Store:    store,
		Service:  upstream.NewService(store, registry, logger),
		Registry: registry,
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx := tenancy.WithTenant(context.Background(), tenant)
	scriptArgs := map[string]any{"only": "this"}
	if _, err := mgr.CallTool(ctx, "fake", "echo", scriptArgs); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	mu.Lock()
	got := captured
	mu.Unlock()
	want := map[string]any{"only": "this"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("upstream received args %#v, want %#v (DefaultsJSON leaked into call)", got, want)
	}
	if _, leaked := got["cloudId"]; leaked {
		t.Errorf("DefaultsJSON 'cloudId' leaked into outbound tool call args")
	}
}

// TestUpstreamsForUser_InvalidStoredJSONDiscarded covers the read-time
// defense: if a row's DefaultsJSON (or a link's ContextJSON) fails to
// unmarshal, Manager.UpstreamsForUser must surface context={} for that
// upstream and emit gateway.context.invalid_json — never panic, never
// block the catalog load.
func TestUpstreamsForUser_InvalidStoredJSONDiscarded(t *testing.T) {
	store := storagetest.OpenMigrated(t)
	obs, logs := observer.New(zap.WarnLevel)
	logger := zap.New(obs)

	tenant := seedTenant(t, store, "junk", "zorg-junk")
	up := seedUpstream(t, store, tenant.ID, "broken", "https://broken.example/mcp")
	// Valid JSON but wrong shape — exercises the schema-drift /
	// manual-SQL-edit case. Outright invalid JSON is blocked by the
	// jsonb column type, so it cannot land at rest.
	setUpstreamRawDefaults(t, store, tenant.ID, up.ID, []byte(`[1,2,3]`))

	registry := upstream.NewRegistry()
	registry.Register(none.New(nil))
	mgr, err := gateway.NewManager(gateway.ManagerOptions{
		Store:    store,
		Service:  upstream.NewService(store, registry, logger),
		Registry: registry,
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx := tenancy.WithTenant(context.Background(), tenant)
	views, err := mgr.UpstreamsForUser(ctx)
	if err != nil {
		t.Fatalf("UpstreamsForUser: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	if got := views[0].Context; len(got) != 0 {
		t.Errorf("invalid JSON should degrade to empty context, got %#v", got)
	}

	var sawWarn bool
	for _, entry := range logs.All() {
		if entry.Message == "gateway.context.invalid_json" {
			sawWarn = true
			fields := entry.ContextMap()
			if src, _ := fields["source"].(string); !strings.Contains(src, "defaults_json") {
				t.Errorf("warn log source = %q, want defaults_json", src)
			}
		}
	}
	if !sawWarn {
		t.Errorf("expected gateway.context.invalid_json warn, got logs: %v", logs.All())
	}
}

// setUpstreamDefaults writes a JSON object into Upstream.DefaultsJSON.
func setUpstreamDefaults(t *testing.T, store *storage.Store, tenantID, upstreamID int64, defaults map[string]any) {
	t.Helper()
	raw, err := json.Marshal(defaults)
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}
	setUpstreamRawDefaults(t, store, tenantID, upstreamID, raw)
}

// setUpstreamRawDefaults writes arbitrary bytes (possibly invalid JSON)
// into Upstream.DefaultsJSON. Bypasses validation on purpose — the read
// path is what the read-time defense is exercising.
func setUpstreamRawDefaults(t *testing.T, store *storage.Store, tenantID, upstreamID int64, raw []byte) {
	t.Helper()
	ctx := storage.WithTenant(context.Background(), tenantID)
	db, commit, err := store.Session(ctx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if err := db.Model(&storage.Upstream{}).
		Where("id = ?", upstreamID).
		UpdateColumn("defaults_json", raw).Error; err != nil {
		_ = commit()
		t.Fatalf("update defaults_json: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit defaults: %v", err)
	}
}
