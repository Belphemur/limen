// Package mcpspec implements the "mcp_spec" upstream strategy: discover
// the upstream's OAuth Authorization Server via the MCP-mandated Protected
// Resource Metadata document, register a client (RFC 7591 Dynamic Client
// Registration when the AS advertises it, otherwise a pre-provisioned
// static client from UpstreamStrategyConfig), drive the per-user
// authorization code flow with PKCE (S256), and persist + rotate
// access/refresh tokens.
//
// All discovery happens on-demand; results are cached in-memory keyed on
// upstream ID. Token refresh uses an upstream-scoped singleflight so
// concurrent requests for the same link don't trigger duplicate refreshes.
//
// File layout in this package:
//   - strategy.go   — Strategy struct, Options, constructor, interface stubs
//   - discovery.go  — PRM + AS metadata discovery and overlay logic
//   - config.go     — static-client UpstreamStrategyConfig encode/decode
//   - provision.go  — DCR + static-client provisioning
//   - link.go       — StartLink / FinishLink + PKCE helpers
//   - refresh.go    — token rotation, Headers, Maintain
package mcpspec

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/oauthstate"
)

const (
	kindAccessToken    = "upstream.access_token"
	kindRefreshToken   = "upstream.refresh_token"
	kindClientSecret   = "upstream.dcr.client_secret"
	kindRegAccessToken = "upstream.dcr.registration_access_token"
	kindStrategyConfig = "upstream.mcpspec.strategy_config"
	defaultHTTPTimeout = 30 * time.Second
)

// errInvalidGrant is the OAuth-spec error code that means the refresh
// token has been revoked / expired. Treated as a re-link signal.
const errInvalidGrant = "invalid_grant"

// Options configures the strategy.
type Options struct {
	// HTTPClient is used for all outbound calls (discovery, DCR, token
	// endpoints). Phase 10 replaces this with the resilient client.
	HTTPClient *http.Client
	// RedirectURLFn builds the absolute URL the upstream AS redirects
	// back to after the user authorizes. Must be registered with the AS.
	// Shape: https://<gateway>/t/{tenant_public_id}/upstream/{name}/callback
	RedirectURLFn func(tenantPublic, upstreamName string) string
	// ProactiveWindow is the "refresh if expiring within X" threshold.
	ProactiveWindow time.Duration
	// SoftwareID / SoftwareVersion advertised in DCR. Optional.
	SoftwareID      string
	SoftwareVersion string
}

// Strategy implements upstream.Strategy for StrategyMCPSpec.
type Strategy struct {
	store   *storage.Store
	cipher  *crypto.Cipher
	state   *oauthstate.Store
	http    *http.Client
	redirFn func(tenantPublic, upstreamName string) string
	proWin  time.Duration
	swID    string
	swVer   string

	discMu sync.RWMutex
	disc   map[int64]*discoveryEntry

	sf singleflight.Group
}

// New builds the strategy.
func New(store *storage.Store, cipher *crypto.Cipher, state *oauthstate.Store, opts Options) (*Strategy, error) {
	if store == nil || cipher == nil || state == nil {
		return nil, errors.New("mcpspec: store, cipher, state are required")
	}
	if opts.RedirectURLFn == nil {
		return nil, errors.New("mcpspec: RedirectURLFn is required")
	}
	c := opts.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: defaultHTTPTimeout}
	}
	pw := opts.ProactiveWindow
	if pw <= 0 {
		pw = 60 * time.Second
	}
	return &Strategy{
		store:   store,
		cipher:  cipher,
		state:   state,
		http:    c,
		redirFn: opts.RedirectURLFn,
		proWin:  pw,
		swID:    opts.SoftwareID,
		swVer:   opts.SoftwareVersion,
		disc:    make(map[int64]*discoveryEntry),
	}, nil
}

// Type implements upstream.Strategy.
func (s *Strategy) Type() upstream.StrategyType { return upstream.StrategyMCPSpec }

// RequiresLink reports that mcp_spec upstreams need a per-user link.
func (s *Strategy) RequiresLink() bool { return true }
