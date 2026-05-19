// Package upstream implements Phase 7's outbound-upstream linking system.
//
// Each upstream MCP server declares a Strategy that drives credentialing:
//
//   - "none"          — no auth (or a static URL embedded in the upstream URL).
//   - "static_header" — a fixed HTTP header, either tenant-wide or per-user.
//   - "mcp_spec"      — the MCP discovery + DCR + PKCE OAuth flow.
//
// A Strategy is registered once at startup (NewRegistry, registry.Register)
// and resolved per request by upstreams.StrategyType. The runtime contract
// is intentionally small: provisioning, optional user-link
// start/finish, and per-call header generation. Future credential schemes
// only have to implement this interface.
//
// Everything in this package is callable from server-side Go code; the
// Connect-RPC handlers Phase 9b ships are thin wrappers over the methods
// here. The only HTTP route this package owns is GET .../callback (see
// callback.go) because the OAuth redirect URI is protocol-mandated.
package upstream

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/belphemur/limen/internal/storage"
)

// StrategyType is the value stored in upstreams.strategy_type. Modelled
// as a named string so the compiler distinguishes a raw upstream label
// from any other free-form string at every call site, the same way
// FailureReason does for upstream_links.last_failure_reason.
type StrategyType string

const (
	StrategyNone         StrategyType = "none"
	StrategyStaticHeader StrategyType = "static_header"
	StrategyMCPSpec      StrategyType = "mcp_spec"
)

// String lets StrategyType satisfy fmt.Stringer for logging.
func (s StrategyType) String() string { return string(s) }

// FailureReason is a short enum string written into
// upstream_links.last_failure_reason. Keeps the column human-readable
// without dragging an enum type into the schema.
type FailureReason string

const (
	ReasonRefreshFailed FailureReason = "refresh_failed"
	ReasonInvalidGrant  FailureReason = "invalid_grant"
	ReasonToolCall401   FailureReason = "tool_call_401"
	ReasonToolCall403   FailureReason = "tool_call_403"
	ReasonToolCall5xx   FailureReason = "tool_call_5xx"
	ReasonNetwork       FailureReason = "network"
)

// ErrNeedsRelink signals that the upstream link can no longer be refreshed
// and the user must walk through the link flow again. The Phase 8
// per-request roundtripper maps this (and a fresh 401 after a refresh) to
// the structured MCP "re-link required" error.
var ErrNeedsRelink = errors.New("upstream: link needs re-link")

// LinkContext bundles the per-request inputs every Strategy method needs.
// Tenant and User are pre-loaded; Link is nil when no link exists yet
// (Strategy.RequiresLink == false, or pre-StartLink).
type LinkContext struct {
	Tenant   *storage.Tenant
	User     *storage.User
	Upstream *storage.Upstream
	Link     *storage.UpstreamLink
	// ReturnTo is where the SPA wants the browser to land after the link
	// completes. Strategy.FinishLink consumes it; ignored by tenant-mode
	// strategies.
	ReturnTo string
}

// StartLinkResult is what StartLink returns to the caller. RedirectURL is
// either an external authorize endpoint (mcp_spec) or a relative SPA URL
// (static_header user mode); the SPA handles the 302 either way.
type StartLinkResult struct {
	RedirectURL string
}

// Strategy is the per-upstream credential driver. Implementations live in
// internal/upstream/{none,statichdr,mcpspec}/. Methods that don't apply to
// a strategy return a non-nil ErrUnsupported.
type Strategy interface {
	// Type is the value written to upstreams.strategy_type.
	Type() StrategyType

	// RequiresLink reports whether this strategy needs per-user
	// UpstreamLink rows. mcp_spec and user-mode static_header do;
	// tenant-mode static_header and none do not.
	RequiresLink() bool

	// Provision is called when an admin attaches the upstream to a
	// tenant. Strategies use this hook for shape validation, optional
	// DCR registration (mcp_spec), or sanity probes (none rejects an
	// upstream that advertises PRM).
	Provision(ctx context.Context, lctx LinkContext) error

	// StartLink begins the per-user link flow. Returns a redirect URL
	// the SPA navigates to. No-op for !RequiresLink strategies.
	StartLink(ctx context.Context, lctx LinkContext) (StartLinkResult, error)

	// FinishLink completes the per-user link flow given the authorization-
	// server callback parameters (raw query string). Persists the
	// UpstreamLink. No-op for !RequiresLink strategies.
	FinishLink(ctx context.Context, lctx LinkContext, callbackQuery string) error

	// Headers returns the HTTP headers Phase 8's roundtripper attaches
	// to outbound calls. May read or update Link (lazy refresh) under
	// a transaction.
	Headers(ctx context.Context, lctx LinkContext) (map[string]string, error)

	// HeadersForceRefresh is the same as Headers but mandates a token
	// rotation first. Phase 8's reactive 401 handler calls this exactly
	// once per request. Returns ErrNeedsRelink when invalid_grant or an
	// equivalent terminal error came back from the AS.
	HeadersForceRefresh(ctx context.Context, lctx LinkContext) (map[string]string, error)

	// Maintain is the background-refresher hook. Called from a
	// WithSuperuser(ctx) goroutine for every link whose ExpiresAt is
	// within the refresh window. No-op for strategies that don't rotate
	// tokens (none, static_header).
	Maintain(ctx context.Context, lctx LinkContext) error
}

// ErrUnsupported is returned by Strategy methods that don't apply to the
// concrete strategy (e.g. StartLink on "none"). Callers branch on this so
// the Service layer can return a clean 400.
var ErrUnsupported = errors.New("upstream: operation not supported by this strategy")

// Registry maps strategy types to Strategy implementations.
type Registry struct {
	mu  sync.RWMutex
	all map[StrategyType]Strategy
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{all: make(map[StrategyType]Strategy)} }

// Register installs s under its Type(). Panics on duplicate registration —
// startup wiring, not runtime.
func (r *Registry) Register(s Strategy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := s.Type()
	if _, exists := r.all[t]; exists {
		panic(fmt.Sprintf("upstream: strategy %q already registered", t))
	}
	r.all[t] = s
}

// Resolve returns the Strategy registered for t, or an error if unknown.
// The string form is what storage hands us (upstreams.strategy_type is a
// plain TEXT column), so this is the one place we cross the named-type
// boundary.
func (r *Registry) Resolve(t StrategyType) (Strategy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.all[t]
	if !ok {
		return nil, fmt.Errorf("upstream: unknown strategy type %q", t)
	}
	return s, nil
}

// Known reports the registered strategy types — diagnostic only.
func (r *Registry) Known() []StrategyType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]StrategyType, 0, len(r.all))
	for t := range r.all {
		out = append(out, t)
	}
	return out
}
