// Package serveportal is the portal binary entry point used by
// cmd/portal. It mounts the customer / admin Connect-RPC routes
// (Phase 9b/9c land the actual services), the OIDC RP (/auth/*), the
// OAuth proxy (DCR + AS metadata + redirector), and the upstream OAuth
// callback. Holds the most privileged secrets in the system (the
// Zitadel management credential and the portal-session cipher key).
package serveportal

import (
	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/boot/oauthproxymount"
	"github.com/belphemur/limen/internal/boot/oidcboot"
	"github.com/belphemur/limen/internal/boot/portalmount"
	"github.com/belphemur/limen/internal/boot/upstreammount"
	"github.com/belphemur/limen/internal/boot/zitadelboot"
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
	boot.MountHealth(r)

	portalmount.Mount(r, rt, oidc)
	if err := oauthproxymount.Mount(r, rt, zclient); err != nil {
		return err
	}
	upstreammount.Mount(r, rt, oidc)

	return boot.RunHTTPServer(rt, r)
}
