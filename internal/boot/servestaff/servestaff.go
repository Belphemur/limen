// Package servestaff is the staff-backoffice binary entry point used
// by cmd/staff. Phase 9a ships only the scaffold (health endpoints +
// 404 for everything else); the actual staff routes land in Phase 12.
package servestaff

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/boot/zitadelboot"
)

// Run boots a staff runtime and serves until SIGINT/SIGTERM. The
// runtime opens the storage pool + Zitadel admin client (staff
// operations need both) but mounts no routes beyond /healthz + /readyz.
func Run(configPath string) error {
	rt, cleanup, err := boot.BootRuntime(configPath, boot.NeedStore)
	if err != nil {
		return err
	}
	defer cleanup()

	// Built but not yet wired into any route — Phase 12 attaches it
	// to the audit surface. Held here so the dependency is exercised
	// at boot.
	if _, err := zitadelboot.Build(rt); err != nil {
		return err
	}

	r := chi.NewRouter()
	r.Use(boot.PermissiveCORS)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(boot.RequestLogger(rt.Logger))
	boot.MountHealth(r)

	return boot.RunHTTPServer(rt, r)
}
