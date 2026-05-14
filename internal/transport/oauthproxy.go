// Package transport's oauthproxy.go wires the Phase 5 OAuth proxy routes
// (AS-metadata + DCR + thin redirector) under /t/{tenant}/oauth/*.
//
// All routes are mounted behind tenancy.RequireTenant. The /register*
// subtree additionally runs through a per-tenant sliding-window rate
// limiter to keep the unauthenticated DCR endpoint from being an abuse
// vector.
package transport

import (
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/oauthproxy"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/zitadel"
)

// OAuthProxyDeps bundles everything MountOAuthProxy needs.
type OAuthProxyDeps struct {
	Store    *storage.Store
	Zitadel  *zitadel.Client
	Logger   *zap.Logger
	BaseURL  string // public origin of the Limen deployment (no trailing slash required)
	Issuer   string // Zitadel issuer URL — advertised as the AS-metadata `issuer`
	OAuthCfg config.OAuthProxyConfig
}

// MountOAuthProxy attaches the AS-metadata, redirector, DCR, and RFC 7592
// management endpoints under /t/{tenant}/oauth/*. Returns the first config
// error (caller wires it into the serve command's bootstrap).
func MountOAuthProxy(r chi.Router, deps OAuthProxyDeps) error {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	metadata, err := oauthproxy.NewMetadataHandler(oauthproxy.MetadataConfig{
		ZitadelIssuer: deps.Issuer,
		BaseURL:       deps.BaseURL,
	})
	if err != nil {
		return err
	}
	redirector := oauthproxy.NewRedirector(metadata.UpstreamEndpoints())

	dcr, err := oauthproxy.NewDCRHandler(oauthproxy.DCRConfig{
		DCREnabled:         deps.OAuthCfg.DCREnabled,
		InitialAccessToken: deps.OAuthCfg.DCRInitialAccessToken,
		BaseURL:            deps.BaseURL,
	}, deps.Store, deps.Zitadel, logger)
	if err != nil {
		return err
	}
	rateLimit := oauthproxy.PerTenantRateLimit(deps.OAuthCfg.RateLimit.RPS, deps.OAuthCfg.RateLimit.Burst)

	r.Route("/t/{tenant}/oauth", func(or chi.Router) {
		or.Use(tenancy.RequireTenant(deps.Store, logger))

		or.Get("/.well-known/oauth-authorization-server", metadata.ServeHTTP)
		or.Get("/.well-known/openid-configuration", metadata.ServeHTTP)

		or.Get("/authorize", redirector.Authorize)
		or.Post("/token", redirector.Token)
		or.Get("/userinfo", redirector.Userinfo)
		or.Post("/revoke", redirector.Revoke)
		or.Post("/introspect", redirector.Introspect)
		or.Get("/end_session", redirector.EndSession)

		or.Group(func(rr chi.Router) {
			rr.Use(rateLimit)
			rr.Post("/register", dcr.Register)
			rr.Get("/register/{client_id}", dcr.Get)
			rr.Put("/register/{client_id}", dcr.Put)
			rr.Delete("/register/{client_id}", dcr.Delete)
		})
	})
	return nil
}
