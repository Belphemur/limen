// Package serveall is the all-in-one binary entry point used by
// cmd/limen (and by 'limen serve' under cmd/limenctl's cobra root).
// Mounts the UNION of every route the split binaries collectively
// expose — adding a route to one of the split binaries without folding
// it in here is a regression.
package serveall

import (
	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/boot/mcpmount"
	"github.com/belphemur/limen/internal/boot/oauthproxymount"
	"github.com/belphemur/limen/internal/boot/oidcboot"
	"github.com/belphemur/limen/internal/boot/portalmount"
	"github.com/belphemur/limen/internal/boot/upstreammount"
	"github.com/belphemur/limen/internal/boot/zitadelboot"
	"github.com/belphemur/limen/internal/mcprs"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/signup"
)

// Run boots the all-in-one runtime and serves until SIGINT/SIGTERM.
func Run(configPath string) error {
	rt, cleanup, err := boot.BootRuntime(configPath, boot.AllProfiles)
	if err != nil {
		return err
	}
	defer cleanup()

	oidc, err := oidcboot.Build(rt)
	if err != nil {
		return err
	}
	zclient, err := zitadelboot.Build(rt)
	if err != nil {
		return err
	}

	_, mcpServer, err := mcpmount.Build(rt)
	if err != nil {
		return err
	}

	metadataHandler, err := mcprs.NewHandler(mcprs.MetadataConfig{BaseURL: rt.Cfg.Server.BaseURL})
	if err != nil {
		return err
	}
	mcpAuth, err := auth.NewMCPAuth(rt.Ctx, auth.MCPAuthConfig{
		Issuer:   rt.Cfg.OIDC.Issuer,
		Audience: rt.Cfg.Zitadel.MCPResourceAudience,
	}, metadataHandler, rt.Store, rt.Logger)
	if err != nil {
		return err
	}

	var bearerIntercept connect.UnaryInterceptorFunc
	if mcpAuth != nil {
		bearerIntercept = session.BearerTokenInterceptor(
			session.BearerTokenConfig{Verifier: mcpAuth.Verifier()},
			rt.Store,
			rt.Logger,
		)
	}

	r := chi.NewRouter()
	r.Use(boot.PermissiveCORS)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(boot.RequestLogger(rt.Logger))
	r.Get("/", boot.LandingPage)
	boot.MountHealth(r)

	signupSvc, err := portalmount.Mount(r, rt, oidc, bearerIntercept, zclient, zclient, zclient, zclient)
	if err != nil {
		return err
	}
	if rt.Cfg.Signup.Enabled && signupSvc != nil {
		go signup.NewSweeper(rt.Store, rt.Logger.Named("signup-sweeper")).Run(rt.Ctx)
	}
	if err := oauthproxymount.Mount(r, rt, zclient); err != nil {
		return err
	}
	if err := mcpmount.Mount(r, rt, mcpServer, mcpAuth); err != nil {
		return err
	}
	upstreammount.Mount(r, rt, oidc)

	return boot.RunHTTPServer(rt, r)
}
