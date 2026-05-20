// Package none implements the "no auth" upstream strategy.
//
// An upstream registered as StrategyNone publishes a public MCP server
// (or one whose credentials are baked into the URL — e.g. a per-tenant
// shared-secret subdomain). Limen attaches no headers; the upstream is
// reachable as-is.
//
// Provision rejects an upstream that advertises an OAuth 2.0 Protected
// Resource Metadata document at <url>/.well-known/oauth-protected-resource
// because that means the upstream *wants* a Bearer credential, and a
// "none" registration in that scenario would silently produce 401s on
// every tool call. The probe is intentionally a HEAD with a short timeout
// so a flaky upstream during onboarding doesn't block tenants forever.
package none

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/belphemur/limen/internal/upstream"
)

// probeTimeout caps the PRM probe in Provision. Short enough to fail fast
// during onboarding; not so short that a slow upstream gets a false pass.
const probeTimeout = 5 * time.Second

// Strategy implements upstream.Strategy for StrategyNone.
type Strategy struct {
	http *http.Client
}

// New returns a Strategy. Pass nil to use a default http.Client with the
// 5-second probe timeout; Phase 10 will swap in a resilience-wrapped client.
func New(client *http.Client) *Strategy {
	if client == nil {
		client = &http.Client{Timeout: probeTimeout}
	}
	return &Strategy{http: client}
}

// Type implements upstream.Strategy.
func (s *Strategy) Type() upstream.StrategyType { return upstream.StrategyNone }

// RequiresLink implements upstream.Strategy: a "none" upstream needs no
// per-user state.
func (s *Strategy) RequiresLink() bool { return false }

// Provision probes the upstream's PRM endpoint and rejects an upstream
// that advertises one. We intentionally treat any 200 as "advertises PRM"
// — RFC 9728 says the document is JSON, but parsing it would only let an
// attacker hide PRM behind invalid JSON.
func (s *Strategy) Provision(ctx context.Context, lctx upstream.LinkContext) error {
	if lctx.Upstream == nil {
		return errors.New("none: provision: upstream missing")
	}
	probeURL := strings.TrimRight(lctx.Upstream.McpServerURL, "/") + "/.well-known/oauth-protected-resource"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, probeURL, nil)
	if err != nil {
		return fmt.Errorf("none: build probe: %w", err)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		// Probe failure is not a provisioning error — the upstream may
		// simply be offline at onboarding time. The point of the probe
		// is to flag servers that *do* advertise PRM, not to fail
		// closed on every network blip.
		return nil
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		// SAFETY: rejecting here is what keeps an operator from
		// configuring "none" against an upstream that demands a Bearer.
		// Every tool call would otherwise return 401.
		return fmt.Errorf("none: upstream advertises OAuth Protected Resource Metadata; use the mcp_spec strategy instead")
	}
	return nil
}

// StartLink is unsupported.
func (s *Strategy) StartLink(_ context.Context, _ upstream.LinkContext) (upstream.StartLinkResult, error) {
	return upstream.StartLinkResult{}, upstream.ErrUnsupported
}

// FinishLink is unsupported.
func (s *Strategy) FinishLink(_ context.Context, _ upstream.LinkContext, _ string) (string, error) {
	return "", upstream.ErrUnsupported
}

// Headers returns an empty header map — no credential to attach.
func (s *Strategy) Headers(_ context.Context, _ upstream.LinkContext) (map[string]string, error) {
	return map[string]string{}, nil
}

// HeadersForceRefresh is the same as Headers — there's nothing to refresh.
func (s *Strategy) HeadersForceRefresh(_ context.Context, _ upstream.LinkContext) (map[string]string, error) {
	return map[string]string{}, nil
}

// Maintain is a no-op for "none".
func (s *Strategy) Maintain(_ context.Context, _ upstream.LinkContext) error { return nil }
