// Package signupmount builds the SignupService Connect handler for
// binaries that host the public signup wizard (portal, all-in-one).
// Mounted at the root /api/ prefix — NOT under /t/{tenant}/ — because
// SignupService is tenant-agnostic.
package signupmount

import (
	"net/http"

	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/signup"
)

// NewHandler returns the URL-path prefix + http.Handler pair for
// SignupService. The caller mounts it on the root chi router under
// /api/ (see transport.MountPortal).
func NewHandler(rt *boot.Runtime) (string, http.Handler) {
	svc := signup.NewService(rt.Store, rt.Logger)
	return svc.Handler()
}
