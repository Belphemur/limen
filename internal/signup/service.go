// Package signup implements the public, tenant-agnostic SignupService
// Connect-RPC handler mounted at /api/limen.signup.v1.SignupService/*.
//
// Why this is a sibling of internal/admin instead of a skip-list on
// AdminService: there is no tenant on the URL to resolve, no portal
// cookie to verify, and no role to check. Encoding any of those as a
// "skip these methods" branch inside the AdminService interceptors
// forces every future reader to remember which RPCs are special. A
// separate service with no interceptors is the correct shape.
//
// Captcha + per-IP rate limiting land in phase 9c slice 4.
package signup

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/signup/signupv1/signupv1connect"
	"github.com/belphemur/limen/internal/storage"
)

// Service is the SignupServiceHandler implementation.
type Service struct {
	store  *storage.Store
	logger *zap.Logger
}

// NewService builds the signup Connect-RPC service.
func NewService(store *storage.Store, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{store: store, logger: logger}
}

// Handler returns the URL-path-prefix + http.Handler pair. The
// handler ships with NO interceptors — these RPCs are public.
func (s *Service) Handler() (string, http.Handler) {
	return signupv1connect.NewSignupServiceHandler(s)
}
