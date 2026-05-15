package mcpspec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
)

type prmDoc struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

type asMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	ScopesSupported                   []string `json:"scopes_supported"`
}

type discoveryEntry struct {
	prm     *prmDoc
	as      *asMetadata
	fetched time.Time
}

// discover resolves the PRM document and AS metadata for the upstream,
// caching the result in-process. Any fields the AS does not publish can
// be filled in from a static UpstreamStrategyConfig (Config) — that
// covers OAuth servers like GitHub that don't expose a metadata endpoint
// or omit registration_endpoint.
func (s *Strategy) discover(ctx context.Context, lctx upstream.LinkContext) (*prmDoc, *asMetadata, error) {
	up := lctx.Upstream
	if up == nil {
		return nil, nil, fmt.Errorf("mcpspec: discover: upstream missing")
	}
	s.discMu.RLock()
	if e, ok := s.disc[up.ID]; ok {
		s.discMu.RUnlock()
		return e.prm, e.as, nil
	}
	s.discMu.RUnlock()

	// Best-effort static overrides from UpstreamStrategyConfig.
	cfg, _ := s.tryLoadConfig(ctx, lctx)

	prm, prmErr := s.fetchPRM(ctx, up.McpServerURL)
	if prmErr != nil {
		if cfg.Issuer == "" {
			return nil, nil, prmErr
		}
		// Synthesize a PRM from the config so callers can proceed.
		prm = &prmDoc{Resource: up.McpServerURL, AuthorizationServers: []string{cfg.Issuer}}
	}
	if len(prm.AuthorizationServers) == 0 {
		if cfg.Issuer == "" {
			return nil, nil, fmt.Errorf("mcpspec: PRM at %s lists no authorization_servers", up.McpServerURL)
		}
		prm.AuthorizationServers = []string{cfg.Issuer}
	}

	issuer := prm.AuthorizationServers[0]
	as, asErr := s.fetchASMetadata(ctx, issuer)
	if asErr != nil {
		if cfg.AuthorizationEndpoint == "" || cfg.TokenEndpoint == "" {
			return nil, nil, asErr
		}
		as = &asMetadata{Issuer: issuer}
	}
	overlayConfigOnAS(as, cfg)
	if as.Issuer == "" {
		as.Issuer = issuer
	}
	if as.AuthorizationEndpoint == "" || as.TokenEndpoint == "" {
		return nil, nil, fmt.Errorf("mcpspec: AS %s missing endpoints", issuer)
	}
	if len(as.CodeChallengeMethodsSupported) > 0 && !sliceContains(as.CodeChallengeMethodsSupported, "S256") {
		return nil, nil, fmt.Errorf("mcpspec: AS %s does not support PKCE S256", issuer)
	}

	s.discMu.Lock()
	s.disc[up.ID] = &discoveryEntry{prm: prm, as: as, fetched: time.Now()}
	s.discMu.Unlock()
	return prm, as, nil
}

// overlayConfigOnAS fills in AS fields that the network metadata didn't
// provide. Network values always win when present.
func overlayConfigOnAS(as *asMetadata, cfg Config) {
	if as.AuthorizationEndpoint == "" {
		as.AuthorizationEndpoint = cfg.AuthorizationEndpoint
	}
	if as.TokenEndpoint == "" {
		as.TokenEndpoint = cfg.TokenEndpoint
	}
	if as.Issuer == "" {
		as.Issuer = cfg.Issuer
	}
}

func (s *Strategy) fetchPRM(ctx context.Context, mcpURL string) (*prmDoc, error) {
	u, err := url.Parse(mcpURL)
	if err != nil {
		return nil, fmt.Errorf("mcpspec: parse mcp url: %w", err)
	}
	path := strings.TrimRight(u.Path, "/")

	// Candidate well-known locations, in order:
	//   1. RFC 9728 §3.1 canonical: <origin>/.well-known/oauth-protected-resource<path>
	//   2. Legacy / pre-RFC: <origin><path>/.well-known/oauth-protected-resource
	candidates := []string{
		buildPRMURL(u, "/.well-known/oauth-protected-resource"+path),
		buildPRMURL(u, path+"/.well-known/oauth-protected-resource"),
	}
	var lastErr error
	for _, c := range candidates {
		prm, err := fetchJSON[prmDoc](ctx, s.http, c)
		if err == nil {
			return prm, nil
		}
		lastErr = err
	}

	// Last resort: probe the resource itself; a compliant server responds
	// with 401 + WWW-Authenticate: Bearer resource_metadata="<URL>".
	if prmURL := s.probePRMHint(ctx, mcpURL); prmURL != "" {
		prm, err := fetchJSON[prmDoc](ctx, s.http, prmURL)
		if err == nil {
			return prm, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func buildPRMURL(base *url.URL, path string) string {
	c := *base
	c.Path = path
	c.RawQuery = ""
	return c.String()
}

// probePRMHint issues an unauthenticated GET to mcpURL and parses any
// resource_metadata="..." parameter from the WWW-Authenticate header.
// Returns "" if no hint is found.
func (s *Strategy) probePRMHint(ctx context.Context, mcpURL string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mcpURL, nil)
	if err != nil {
		return ""
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
	for _, h := range resp.Header.Values("WWW-Authenticate") {
		if u := extractResourceMetadata(h); u != "" {
			return u
		}
	}
	return ""
}

// extractResourceMetadata pulls resource_metadata="<url>" out of a
// WWW-Authenticate header value (RFC 9728 §5.1).
func extractResourceMetadata(header string) string {
	const key = "resource_metadata="
	idx := strings.Index(header, key)
	if idx < 0 {
		return ""
	}
	v := header[idx+len(key):]
	v = strings.TrimLeft(v, " \t")
	if strings.HasPrefix(v, `"`) {
		v = v[1:]
		end := strings.IndexByte(v, '"')
		if end < 0 {
			return ""
		}
		return v[:end]
	}
	if end := strings.IndexAny(v, ", "); end >= 0 {
		return v[:end]
	}
	return v
}

func (s *Strategy) fetchASMetadata(ctx context.Context, issuer string) (*asMetadata, error) {
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("mcpspec: parse issuer: %w", err)
	}
	base := strings.TrimRight(u.Path, "/")
	candidates := []string{
		"/.well-known/oauth-authorization-server" + base,
		base + "/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration" + base,
		base + "/.well-known/openid-configuration",
	}
	var lastErr error
	for _, p := range candidates {
		c := *u
		c.Path = p
		c.RawQuery = ""
		md, err := fetchJSON[asMetadata](ctx, s.http, c.String())
		if err == nil {
			return md, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("mcpspec: AS metadata not found for %s: %w", issuer, lastErr)
}

func fetchJSON[T any](ctx context.Context, hc *http.Client, urlStr string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET %s: %s", urlStr, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("GET %s: parse json: %w", urlStr, err)
	}
	return &out, nil
}

func sliceContains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}

// tryLoadConfig returns the static config for the upstream, or a zero
// Config if none is provisioned. Errors are swallowed: a missing config
// is the common case and the caller decides whether the absence is fatal.
func (s *Strategy) tryLoadConfig(ctx context.Context, lctx upstream.LinkContext) (Config, error) {
	if lctx.Tenant == nil || lctx.Upstream == nil {
		return Config{}, nil
	}
	cfg, err := s.loadConfig(ctx, lctx.Tenant.ID, lctx.Upstream.ID)
	return cfg, err
}

// invalidate clears the discover cache for an upstream — called when
// Provision persists new credentials so the next StartLink picks up
// freshly resolved endpoints (e.g. after the operator added a static
// Config row for a server we'd previously failed to discover).
func (s *Strategy) invalidate(up *storage.Upstream) {
	if up == nil {
		return
	}
	s.discMu.Lock()
	delete(s.disc, up.ID)
	s.discMu.Unlock()
}
