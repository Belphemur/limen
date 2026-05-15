package mcprs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

func TestNewHandler_Validation(t *testing.T) {
	if _, err := NewHandler(MetadataConfig{}); err == nil {
		t.Fatal("expected error on empty BaseURL")
	}
	if _, err := NewHandler(MetadataConfig{BaseURL: "not-a-url"}); err == nil {
		t.Fatal("expected error on relative BaseURL")
	}
	h, err := NewHandler(MetadataConfig{BaseURL: "https://limen.example.com"})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if got := h.ResourceURL("tnt_1"); got != "https://limen.example.com/t/tnt_1/mcp" {
		t.Errorf("ResourceURL: %q", got)
	}
	if got := h.MetadataURL("tnt_1"); got != "https://limen.example.com/t/tnt_1/mcp/.well-known/oauth-protected-resource" {
		t.Errorf("MetadataURL: %q", got)
	}
	if got := h.AuthorizationServerURL("tnt_1"); got != "https://limen.example.com/t/tnt_1/oauth" {
		t.Errorf("AuthorizationServerURL: %q", got)
	}
}

func TestHandler_ServeHTTP(t *testing.T) {
	h, err := NewHandler(MetadataConfig{
		BaseURL:               "https://limen.example.com",
		ResourceDocumentation: "https://docs.example.com",
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/t/tnt_1/mcp/.well-known/oauth-protected-resource", nil)
	req = req.WithContext(tenancy.WithTenant(req.Context(), &storage.Tenant{
		Base: storage.Base{PublicID: "tnt_1"},
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("Cache-Control: %q", got)
	}

	var body prmResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Resource != "https://limen.example.com/t/tnt_1/mcp" {
		t.Errorf("Resource: %q", body.Resource)
	}
	if len(body.AuthorizationServers) != 1 ||
		body.AuthorizationServers[0] != "https://limen.example.com/t/tnt_1/oauth" {
		t.Errorf("AuthorizationServers: %v", body.AuthorizationServers)
	}
	if len(body.BearerMethodsSupported) != 1 || body.BearerMethodsSupported[0] != "header" {
		t.Errorf("BearerMethodsSupported: %v", body.BearerMethodsSupported)
	}
	if len(body.ScopesSupported) == 0 {
		t.Errorf("ScopesSupported empty")
	}
	if body.ResourceDocumentation != "https://docs.example.com" {
		t.Errorf("ResourceDocumentation: %q", body.ResourceDocumentation)
	}
}
