// Package serveportal is the portal binary entry point used by
// cmd/portal. It mounts the customer / admin Connect-RPC routes
// (Phase 9b/9c land the actual services), the OIDC RP (/auth/*), the
// OAuth proxy (DCR + AS metadata + redirector), and the upstream OAuth
// callback. Holds the most privileged secrets in the system (the
// Zitadel management credential and the portal-session cipher key).
package serveportal

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/boot/oauthproxymount"
	"github.com/belphemur/limen/internal/boot/oidcboot"
	"github.com/belphemur/limen/internal/boot/portalmount"
	"github.com/belphemur/limen/internal/boot/upstreammount"
	"github.com/belphemur/limen/internal/boot/zitadelboot"
	"github.com/belphemur/limen/internal/signup"
)

// Run boots a portal runtime and serves until SIGINT/SIGTERM.
func Run(configPath string) error {
	profile := boot.NeedStore | boot.NeedCipher | boot.NeedSigner | boot.NeedUpstream
	rt, cleanup, err := boot.BootRuntime(configPath, profile)
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

	r := chi.NewRouter()
	r.Use(boot.PermissiveCORS)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(boot.RequestLogger(rt.Logger))
	boot.MountHealth(r)

	signupSvc, err := portalmount.Mount(r, rt, oidc, zclient, zclient, zclient, zclient)
	if err != nil {
		return err
	}
	if rt.Cfg.Signup.Enabled && signupSvc != nil {
		go signup.NewSweeper(rt.Store, rt.Logger.Named("signup-sweeper")).Run(rt.Ctx)
	}
	if err := oauthproxymount.Mount(r, rt, zclient); err != nil {
		return err
	}
	upstreammount.Mount(r, rt, oidc)

	return boot.RunHTTPServer(rt, r)
}
