package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/belphemur/limen/internal/upstream"
)

type fakeAuthProvider struct {
	headersErr error
}

func (f *fakeAuthProvider) Headers(context.Context) (upstream.AuthResult, error) {
	return upstream.AuthResult{}, f.headersErr
}
func (f *fakeAuthProvider) HeadersForceRefresh(context.Context) (upstream.AuthResult, error) {
	return upstream.AuthResult{}, f.headersErr
}

// TestAuthInjectingTransport_SessionCloseBypassesAuth asserts that the
// mcp-go session-teardown DELETE (issued on context.Background() with no
// user) goes out unauthenticated instead of erroring with ErrNoUser.
// Regression: see "failed to send close request: upstream: no
// authenticated user on ctx" spam on every upstream client close.
func TestAuthInjectingTransport_SessionCloseBypassesAuth(t *testing.T) {
	var gotAuthHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	tr := &AuthInjectingTransport{
		Base: http.DefaultTransport,
		Auth: &fakeAuthProvider{headersErr: upstream.ErrNoUser},
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Mcp-Session-Id", "sess-xyz")

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status: got %d want 202", resp.StatusCode)
	}
	if gotAuthHeader != "" {
		t.Errorf("Authorization header leaked on session close: %q", gotAuthHeader)
	}
}

// TestAuthInjectingTransport_NonCloseRequestPropagatesErrNoUser keeps
// the guard tight: any other request without a user (POST tools/call,
// GET sessions, etc.) must still fail closed.
func TestAuthInjectingTransport_NonCloseRequestPropagatesErrNoUser(t *testing.T) {
	tr := &AuthInjectingTransport{
		Base: http.DefaultTransport,
		Auth: &fakeAuthProvider{headersErr: upstream.ErrNoUser},
	}
	// POST without session header — definitely not a close.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.invalid/", http.NoBody)
	resp, err := tr.RoundTrip(req)
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("expected ErrNoUser, got nil")
	}
}
