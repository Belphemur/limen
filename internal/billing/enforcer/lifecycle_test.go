package enforcer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// TestEvaluateBillingStatus is the exhaustive table-driven suite for
// the lifecycle state machine. Every Stripe subscription.status value
// is covered, plus the grace-window edge cases (nil, before, at,
// after) and an unknown / future status. The function is pure, so
// the only inputs are (status, graceUntil, now).
//
// Verdict map exercised here:
//
//	none / trialing / active    → decisionPass
//	canceled                    → decisionPass (auto-downgrade in caller)
//	past_due / unpaid (grace)   → decisionPassGrace
//	past_due / unpaid (expired) → decisionPass (auto-downgrade in caller)
//	incomplete / paused         → decisionBlock
//	unknown                     → decisionPassUnknown
func TestEvaluateBillingStatus(t *testing.T) {
	// Fixed reference time so the grace-window cases are
	// deterministic. Status names follow Stripe's enum verbatim.
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	grace := now.Add(24 * time.Hour) // grace expires 24h from now
	past := now.Add(-time.Hour)      // grace expired 1h ago
	future := now.Add(time.Hour)     // grace still valid 1h from now

	tests := []struct {
		name       string
		status     string
		graceUntil *time.Time
		now        time.Time
		want       lifecycleDecision
	}{
		// Pass-through statuses.
		{
			name:   "none passes",
			status: "none",
			want:   decisionPass,
		},
		{
			name:   "trialing passes",
			status: "trialing",
			want:   decisionPass,
		},
		{
			name:   "active passes",
			status: "active",
			want:   decisionPass,
		},

		// past_due with grace window — the grace period is intact
		// so the tenant keeps their Team limits and the SPA renders
		// the warning banner.
		{
			name:       "past_due within grace passes with header",
			status:     "past_due",
			graceUntil: &future,
			now:        now,
			want:       decisionPassGrace,
		},
		{
			name:       "unpaid within grace passes with header",
			status:     "unpaid",
			graceUntil: &future,
			now:        now,
			want:       decisionPassGrace,
		},

		// past_due / unpaid where the grace window has elapsed
		// (nil or in the past). The state machine now returns
		// decisionPass — the middleware auto-downgrades the tenant
		// to Developer and lets the request through with developer
		// limits. The grace comparison is strict-less-than, so
		// now == grace is treated as expired and also passes here.
		{
			name:       "past_due with nil grace passes (auto-downgrade)",
			status:     "past_due",
			graceUntil: nil,
			now:        now,
			want:       decisionPass,
		},
		{
			name:       "unpaid with nil grace passes (auto-downgrade)",
			status:     "unpaid",
			graceUntil: nil,
			now:        now,
			want:       decisionPass,
		},
		{
			name:       "past_due grace expired passes (auto-downgrade)",
			status:     "past_due",
			graceUntil: &past,
			now:        now,
			want:       decisionPass,
		},
		{
			name:       "unpaid grace expired passes (auto-downgrade)",
			status:     "unpaid",
			graceUntil: &past,
			now:        now,
			want:       decisionPass,
		},
		{
			name:       "past_due grace exactly now passes (auto-downgrade)",
			status:     "past_due",
			graceUntil: &now,
			now:        now,
			want:       decisionPass,
		},

		// canceled — always auto-downgrade and pass. The grace
		// window is irrelevant for canceled: the subscription is
		// gone regardless.
		{
			name:   "canceled passes (auto-downgrade)",
			status: "canceled",
			want:   decisionPass,
		},
		{
			name:       "canceled within grace still passes (auto-downgrade)",
			status:     "canceled",
			graceUntil: &future,
			now:        now,
			want:       decisionPass,
		},

		// Hard-block statuses — Stripe mid-flow states we don't
		// auto-recover from. The tenant must finish checkout
		// (incomplete) or un-pause (paused) before they get back
		// in. incomplete_expired is permanent.
		{
			name:   "incomplete blocks",
			status: "incomplete",
			want:   decisionBlock,
		},
		{
			name:   "incomplete_expired blocks",
			status: "incomplete_expired",
			want:   decisionBlock,
		},
		{
			name:   "paused blocks",
			status: "paused",
			want:   decisionBlock,
		},

		// Unknown / future statuses — fail-open (pass with a
		// warning in the caller). The verdict is the dedicated
		// decisionPassUnknown so the caller can log it separately
		// from a known-good pass. Better to let a tenant through
		// than to lock them out on a typo.
		{
			name:   "unknown status fails open",
			status: "totally_new_status",
			want:   decisionPassUnknown,
		},
		{
			name:       "unknown status with grace still fails open",
			status:     "weird_state",
			graceUntil: &grace,
			now:        now,
			want:       decisionPassUnknown,
		},
		{
			name:   "empty status fails open",
			status: "",
			want:   decisionPassUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateBillingStatus(tt.status, tt.graceUntil, tt.now)
			if got != tt.want {
				t.Errorf("evaluateBillingStatus(%q, %v, %v) = %d, want %d",
					tt.status, tt.graceUntil, tt.now, got, tt.want)
			}
		})
	}
}

// TestEvaluateBillingStatus_GraceBoundary pins the exact semantic of
// now.Before(*graceUntil) — the comparison is strict-less-than, so
// now == graceUntil is the boundary and is treated as EXPIRED
// (returns decisionPass + auto-downgrade in the middleware). One
// nanosecond before grace is still inside the window. This is the
// intent: grace is a real-time window, not a whole-day cushion, and
// the moment it ticks over the tenant flips to developer limits.
func TestEvaluateBillingStatus_GraceBoundary(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	// One nanosecond before grace expires → still grace.
	almostNow := now.Add(-time.Nanosecond)
	grace := now
	if got := evaluateBillingStatus("past_due", &grace, almostNow); got != decisionPassGrace {
		t.Errorf("expected passGrace at now-1ns, got %d", got)
	}

	// Exactly at grace → grace has expired; auto-downgrade path.
	if got := evaluateBillingStatus("past_due", &grace, now); got != decisionPass {
		t.Errorf("expected pass (auto-downgrade) at now==grace, got %d", got)
	}
}

// TestMCPBillingBlockBody pins the JSON-RPC 2.0 wire shape used by
// RequireBillingActiveMCP on decisionBlock. The error code sits in
// the JSON-RPC server-error range (-32000..-32099) and the message
// always carries the portal link. id=null because the middleware
// returns this before reading the client's request body — the
// server-error code is the same regardless of inbound id.
func TestMCPBillingBlockBody(t *testing.T) {
	body := mcpBillingBlockBody("https://limen.example.com")

	var parsed struct {
		JSONRPC string `json:"jsonrpc"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		ID any `json:"id"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	if parsed.JSONRPC != "2.0" {
		t.Errorf("jsonrpc: %q", parsed.JSONRPC)
	}
	if parsed.Error.Code != -32000 {
		t.Errorf("code: %d", parsed.Error.Code)
	}
	want := "billing: subscription past due — visit https://limen.example.com/billing to update payment"
	if parsed.Error.Message != want {
		t.Errorf("message: %q want %q", parsed.Error.Message, want)
	}
	if parsed.ID != nil {
		t.Errorf("id: %v (want nil)", parsed.ID)
	}
}

// TestMCPBillingBlockBody_EmptyOrigin ensures the block message
// degrades gracefully when no portal origin is configured — the
// dash + URL suffix is dropped, the prefix stays.
func TestMCPBillingBlockBody_EmptyOrigin(t *testing.T) {
	body := mcpBillingBlockBody("")
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Error.Message != "billing: subscription past due" {
		t.Errorf("message: %q", parsed.Error.Message)
	}
}

// TestMCPBillingWarningNotification pins the JSON-RPC 2.0
// notification shape used by RequireBillingActiveMCP on
// decisionPassGrace. The notification's method is
// "notifications/billing_warning" and the params carry a human
// message; no `id` field because notifications are fire-and-forget
// (per JSON-RPC 2.0 §4.1).
func TestMCPBillingWarningNotification(t *testing.T) {
	body := mcpBillingWarningNotification("https://limen.example.com")

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	if parsed["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc: %v", parsed["jsonrpc"])
	}
	if parsed["method"] != "notifications/billing_warning" {
		t.Errorf("method: %v", parsed["method"])
	}
	if _, hasID := parsed["id"]; hasID {
		t.Errorf("id should be absent on notifications, got %v", parsed["id"])
	}
	params, ok := parsed["params"].(map[string]any)
	if !ok {
		t.Fatalf("params: %v", parsed["params"])
	}
	want := "Your subscription payment is past due. Visit https://limen.example.com/billing"
	if params["message"] != want {
		t.Errorf("params.message: %q want %q", params["message"], want)
	}
}

// TestJSONRPCBufferingWriter_Commit writes some body bytes through
// the buffered writer, commits with a notification appended, and
// asserts the underlying writer received body + notification in
// order with the captured status code and headers. This is the
// core mechanic behind the in-band warning the MCP gate injects.
func TestJSONRPCBufferingWriter_Commit(t *testing.T) {
	rec := &recordingResponseWriter{header: http.Header{}}
	bw := newJSONRPCBufferingWriter(rec)
	bw.Header().Set("Content-Type", "application/json")
	bw.WriteHeader(http.StatusOK)
	_, _ = bw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))

	notif := []byte(`{"jsonrpc":"2.0","method":"notifications/billing_warning"}`)
	if err := bw.commit(notif); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if rec.statusCode != http.StatusOK {
		t.Errorf("status: %d", rec.statusCode)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: %q", got)
	}
	want := `{"jsonrpc":"2.0","id":1,"result":{}}` + string(notif)
	if rec.body.String() != want {
		t.Errorf("body:\n got: %q\nwant: %q", rec.body.String(), want)
	}
}

// TestJSONRPCBufferingWriter_CommitNoExtra ensures commit with nil
// extra just flushes the buffered body — the decisionPass /
// decisionPassUnknown case uses this path.
func TestJSONRPCBufferingWriter_CommitNoExtra(t *testing.T) {
	rec := &recordingResponseWriter{header: http.Header{}}
	bw := newJSONRPCBufferingWriter(rec)
	_, _ = bw.Write([]byte("hello"))
	if err := bw.commit(nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if rec.body.String() != "hello" {
		t.Errorf("body: %q", rec.body.String())
	}
}

// TestJSONRPCBufferingWriter_ImplicitStatus verifies that a Write
// before WriteHeader implicitly writes 200, matching net/http's
// default-status behaviour. The downstream handler can write the
// body without ever calling WriteHeader explicitly.
func TestJSONRPCBufferingWriter_ImplicitStatus(t *testing.T) {
	rec := &recordingResponseWriter{header: http.Header{}}
	bw := newJSONRPCBufferingWriter(rec)
	_, _ = bw.Write([]byte("x"))
	if err := bw.commit(nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if rec.statusCode != http.StatusOK {
		t.Errorf("status: %d", rec.statusCode)
	}
}

// TestJSONRPCBufferingWriter_DoubleWriteHeaderNoop pins the
// net/http-compatible behaviour: the second WriteHeader is a no-op
// rather than a panic or status override.
func TestJSONRPCBufferingWriter_DoubleWriteHeaderNoop(t *testing.T) {
	rec := &recordingResponseWriter{header: http.Header{}}
	bw := newJSONRPCBufferingWriter(rec)
	bw.WriteHeader(http.StatusTeapot)
	bw.WriteHeader(http.StatusOK)
	if err := bw.commit(nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if rec.statusCode != http.StatusTeapot {
		t.Errorf("status: %d (first WriteHeader should win)", rec.statusCode)
	}
}

// recordingResponseWriter is a minimal http.ResponseWriter used by
// the buffering-writer tests. It records status, headers, and body
// in memory. http.Flusher / Hijacker are intentionally not
// implemented because the buffering writer doesn't need them and
// the test surface here is a JSON-RPC POST handler.
type recordingResponseWriter struct {
	header     http.Header
	body       bytes.Buffer
	statusCode int
	wroteHead  bool
}

func (r *recordingResponseWriter) Header() http.Header { return r.header }
func (r *recordingResponseWriter) Write(p []byte) (int, error) {
	if !r.wroteHead {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(p)
}
func (r *recordingResponseWriter) WriteHeader(statusCode int) {
	if r.wroteHead {
		return
	}
	r.statusCode = statusCode
	r.wroteHead = true
}
