// Package mcprs implements Limen's MCP Resource Server surface: the
// Protected Resource Metadata document (RFC 9728) and the helpers that
// build the WWW-Authenticate challenge served on every 401/403.
//
// Per-request JWT validation lives in internal/auth (MCPAuth). This
// package is intentionally side-effect-free so it can be reused by tests
// and by future transport variants.
package mcprs

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/belphemur/limen/internal/tenancy"
)

// MetadataConfig configures the PRM handler.
type MetadataConfig struct {
	// BaseURL is the public origin Limen is reachable on (no trailing
	// slash), e.g. "https://limen.example.com".
	BaseURL string
	// Scopes advertised under "scopes_supported". Defaults to
	// {"openid","profile","email","offline_access"} when empty.
	Scopes []string
	// ResourceDocumentation is an optional URL surfaced in the PRM doc.
	ResourceDocumentation string
}

// Handler serves the Protected Resource Metadata for /t/{tenant}/mcp and
// exposes URL builders consumed by the WWW-Authenticate challenge.
//
// One handler instance covers every tenant — the tenant public id is
// derived from the request context (RequireTenant must run first).
type Handler struct {
	cfg MetadataConfig
}

const (
	// MetadataPath is the suffix appended to /t/{tenant}/mcp for the PRM
	// document. Exported so the transport layer can mount it consistently.
	MetadataPath = "/.well-known/oauth-protected-resource"

	// resourceSuffix is the canonical MCP resource path under a tenant.
	resourceSuffix = "/mcp"

	// asMetadataSuffix is the AS-metadata path under a tenant (Phase 5).
	asMetadataSuffix = "/oauth"
)

// NewHandler validates cfg and returns a ready Handler.
func NewHandler(cfg MetadataConfig) (*Handler, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New("mcprs: BaseURL is required")
	}
	if !strings.HasPrefix(cfg.BaseURL, "http://") && !strings.HasPrefix(cfg.BaseURL, "https://") {
		return nil, errors.New("mcprs: BaseURL must be an absolute http(s) URL")
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "profile", "email", "offline_access"}
	}
	return &Handler{cfg: cfg}, nil
}

// ResourceURL returns the canonical "resource" identifier advertised in
// the PRM document for the given tenant public id.
func (h *Handler) ResourceURL(tenantPublicID string) string {
	return h.cfg.BaseURL + "/t/" + tenantPublicID + resourceSuffix
}

// MetadataURL returns the absolute URL of the PRM document for the given
// tenant — the value Limen surfaces in WWW-Authenticate's
// "resource_metadata" parameter.
func (h *Handler) MetadataURL(tenantPublicID string) string {
	return h.ResourceURL(tenantPublicID) + MetadataPath
}

// AuthorizationServerURL returns Limen's per-tenant AS metadata wrapper
// (Phase 5) — surfaced in the PRM "authorization_servers" array.
func (h *Handler) AuthorizationServerURL(tenantPublicID string) string {
	return h.cfg.BaseURL + "/t/" + tenantPublicID + asMetadataSuffix
}

// prmResponse is the wire shape of the PRM document.
type prmResponse struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ScopesSupported        []string `json:"scopes_supported"`
	ResourceDocumentation  string   `json:"resource_documentation,omitempty"`
}

// ServeHTTP serves the Protected Resource Metadata document. The handler
// is public — no bearer required (PRM is a discovery document by design).
// Mount behind tenancy.RequireTenant so the tenant binding is present.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t := tenancy.MustTenant(r.Context())
	body := prmResponse{
		Resource:               h.ResourceURL(t.PublicID),
		AuthorizationServers:   []string{h.AuthorizationServerURL(t.PublicID)},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        h.cfg.Scopes,
		ResourceDocumentation:  h.cfg.ResourceDocumentation,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(body)
}
