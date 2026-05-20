package none

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
)

func TestNone_BasicShape(t *testing.T) {
	s := New(nil)
	if s.Type() != upstream.StrategyNone {
		t.Fatalf("Type = %q, want %q", s.Type(), upstream.StrategyNone)
	}
	if s.RequiresLink() {
		t.Fatalf("RequiresLink = true, want false")
	}
	h, err := s.Headers(context.Background(), upstream.LinkContext{})
	if err != nil || len(h) != 0 {
		t.Fatalf("Headers = (%v, %v), want (empty, nil)", h, err)
	}
	if _, err := s.StartLink(context.Background(), upstream.LinkContext{}); !errors.Is(err, upstream.ErrUnsupported) {
		t.Fatalf("StartLink err = %v, want ErrUnsupported", err)
	}
	if _, err := s.FinishLink(context.Background(), upstream.LinkContext{}, ""); !errors.Is(err, upstream.ErrUnsupported) {
		t.Fatalf("FinishLink err = %v, want ErrUnsupported", err)
	}
}

func TestNone_ProvisionRejectsPRMAdvertise(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s := New(srv.Client())
	err := s.Provision(context.Background(), upstream.LinkContext{
		Upstream: &storage.Upstream{McpServerURL: srv.URL},
	})
	if err == nil {
		t.Fatalf("Provision should reject PRM-advertising upstream")
	}
}

func TestNone_ProvisionAcceptsPlainUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s := New(srv.Client())
	if err := s.Provision(context.Background(), upstream.LinkContext{
		Upstream: &storage.Upstream{McpServerURL: srv.URL},
	}); err != nil {
		t.Fatalf("Provision unexpected err: %v", err)
	}
}

func TestNone_ProvisionToleratesNetworkErrors(t *testing.T) {
	// Point at an unroutable port; the probe should fail and Provision
	// should *not* error — flaky upstream during onboarding is fine.
	s := New(&http.Client{Timeout: probeTimeout})
	err := s.Provision(context.Background(), upstream.LinkContext{
		Upstream: &storage.Upstream{McpServerURL: "http://127.0.0.1:1"},
	})
	if err != nil {
		t.Fatalf("Provision should tolerate network failure, got: %v", err)
	}
}
