package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
)

// AuthInjectingTransport is the http.RoundTripper that sits between the
// mcp-go streamable client and the upstream MCP server. It does three
// jobs per request:
//
//  1. Calls AuthProvider.Headers(ctx) for the current request's user and
//     stamps the resulting headers onto a clone of the request.
//  2. On a 401, calls AuthProvider.HeadersForceRefresh(ctx) exactly once
//     and retries the request with the refreshed headers. A second 401
//     surfaces as a structured re-link error.
//  3. Records per-link health (success / network / 5xx / 401 / 403) via
//     RecordSuccess/RecordTenantSuccess and RecordFailure/RecordTenantFailure,
//     dispatched by the AuthResult.LinkTable discriminator.
//
// The Base RoundTripper is the swap point for the Phase 10
// `resilience.Client("upstream.<name>.calls", cfg).Transport`. Until
// Phase 10 ships, callers wire http.DefaultTransport — there is exactly
// one construction site (the per-(tenant, upstream) Bundle in
// internal/gateway) so the swap stays a one-liner.
type AuthInjectingTransport struct {
	// Base is the underlying transport. In Phase 10 this is the
	// transport on the *http.Client returned by resilience.Client;
	// until then it's http.DefaultTransport.
	Base http.RoundTripper

	// Auth supplies per-request headers for the user on ctx.
	Auth upstream.AuthProvider

	// Store is used for RecordSuccess / RecordFailure. Required when
	// the strategy yields links (per-user); ignored for tenant-mode.
	Store *storage.Store

	// UpstreamName labels structured log lines.
	UpstreamName string

	// HealthThresholds is the auto-disable policy. Passed through to
	// RecordFailure so concurrent failures from the refresher and live
	// traffic use the same numbers.
	HealthThresholds upstream.HealthThresholds

	Logger *zap.Logger
}

// RoundTrip implements http.RoundTripper.
func (t *AuthInjectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	ar, err := t.Auth.Headers(ctx)
	if err != nil {
		// mcp-go's StreamableHTTP.Close sends a best-effort session
		// DELETE on context.Background(), which carries no tenant or
		// user. Surfacing ErrNoUser there spams the stdlib log via
		// mcp-go's default logger ("failed to send close request:
		// upstream: no authenticated user on ctx") on every client
		// teardown. Detect that shape and forward the request
		// unauthenticated — the upstream identifies the session via
		// the Mcp-Session-Id header, and any rejection becomes a
		// normal HTTP response that mcp-go discards quietly.
		if errors.Is(err, upstream.ErrNoUser) && isSessionCloseRequest(req) {
			return t.do(req, nil)
		}
		return nil, err
	}

	resp, err := t.do(req, ar.Headers)

	if shouldRefresh(resp, err) {
		drain(resp)
		ar2, refreshErr := t.Auth.HeadersForceRefresh(ctx)
		if refreshErr != nil {
			t.record(ctx, ar.LinkID, ar.LinkTable, nil, refreshErr)
			return nil, refreshErr
		}
		if ar2.LinkID != 0 {
			ar.LinkID = ar2.LinkID
		}
		if ar2.LinkTable != "" {
			ar.LinkTable = ar2.LinkTable
		}
		resp, err = t.do(req, ar2.Headers)
	}

	t.record(ctx, ar.LinkID, ar.LinkTable, resp, err)
	return resp, err
}

func (t *AuthInjectingTransport) do(req *http.Request, headers map[string]string) (*http.Response, error) {
	clone, err := cloneRequest(req)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		clone.Header.Set(k, v)
	}
	return t.Base.RoundTrip(clone)
}

func (t *AuthInjectingTransport) record(ctx context.Context, linkID int64, linkTable string, resp *http.Response, err error) {
	if linkID == 0 || t.Store == nil {
		return
	}
	bg := context.WithoutCancel(ctx)

	if err != nil {
		t.recordFailure(bg, linkID, linkTable, upstream.ReasonNetwork, false)
		return
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		t.recordSuccess(bg, linkID, linkTable)
	case resp.StatusCode == http.StatusUnauthorized:
		// Second 401 after refresh: link is past saving — flip
		// NeedsRelink immediately.
		t.recordFailure(bg, linkID, linkTable, upstream.ReasonToolCall401, true)
	case resp.StatusCode == http.StatusForbidden:
		t.recordFailure(bg, linkID, linkTable, upstream.ReasonToolCall403, false)
	case resp.StatusCode >= 500:
		t.recordFailure(bg, linkID, linkTable, upstream.ReasonToolCall5xx, false)
	}
}

func (t *AuthInjectingTransport) recordSuccess(ctx context.Context, linkID int64, linkTable string) {
	if linkTable == "upstream_tenant_links" {
		_ = upstream.RecordTenantSuccess(ctx, t.Store, linkID)
		return
	}
	_ = upstream.RecordSuccess(ctx, t.Store, linkID)
}

func (t *AuthInjectingTransport) recordFailure(ctx context.Context, linkID int64, linkTable string, reason upstream.FailureReason, needsRelink bool) {
	if linkTable == "upstream_tenant_links" {
		_ = upstream.RecordTenantFailure(ctx, t.Store, linkID, reason, needsRelink, t.HealthThresholds)
		return
	}
	_ = upstream.RecordFailure(ctx, t.Store, linkID, reason, needsRelink, t.HealthThresholds)
}

func shouldRefresh(resp *http.Response, err error) bool {
	if err != nil {
		return false
	}
	return resp != nil && resp.StatusCode == http.StatusUnauthorized
}

// isSessionCloseRequest detects mcp-go's StreamableHTTP teardown
// DELETE: method DELETE, no body, an Mcp-Session-Id header. mcp-go is
// the only producer of requests through this transport, and it only
// emits DELETEs from its closeOnce path — so the shape is a reliable
// signal that the request is a best-effort session close that does
// not need (and cannot acquire) user auth.
func isSessionCloseRequest(req *http.Request) bool {
	return req.Method == http.MethodDelete &&
		req.Body == nil &&
		req.Header.Get("Mcp-Session-Id") != ""
}

// cloneRequest returns a deep-enough copy of req that mutating headers
// and consuming the body on the clone does not affect the original. It
// requires GetBody for retryable bodies; the mcp-go streamable client
// sets GetBody on every JSON-RPC POST.
func cloneRequest(req *http.Request) (*http.Request, error) {
	clone := req.Clone(req.Context())
	if req.Body == nil {
		return clone, nil
	}
	if req.GetBody == nil {
		return nil, errors.New("gateway: request body is not replayable (GetBody nil)")
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	clone.Body = body
	return clone, nil
}

func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
