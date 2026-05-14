package oauthproxy

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/httprate"

	"github.com/belphemur/limen/internal/tenancy"
)

// errMissingTenant is returned by the per-tenant key function when the
// middleware is mounted ahead of tenancy.RequireTenant. httprate surfaces
// the error via its WithErrorHandler hook, which we wire to a 500.
var errMissingTenant = errors.New("oauthproxy: tenant not resolved")

// PerTenantRateLimit builds the chi-style middleware enforcing a sliding-
// window request cap on the /register* subtree, keyed by tenant.PublicID.
//
// Phase 5 spec calls for a token bucket sized 10 rps / burst 20. httprate's
// sliding-window counter is functionally equivalent for the abuse-
// mitigation purpose: we let `burst` requests through per 1-second window,
// which caps the worst-case rate at ~20 req/s — Phase 5 cites burst as the
// effective ceiling. RPS is currently unused; it stays on the config so
// the limit can be re-tuned (or swapped for a token bucket via
// httprate-redis or a custom LimitCounter) without a breaking change.
//
// Must run AFTER tenancy.RequireTenant. The configured RPS/burst defaults
// (10 / 20) are applied here so the middleware can be wired with a
// zero-valued RateLimitConfig.
func PerTenantRateLimit(rps, burst int) func(http.Handler) http.Handler {
	if rps <= 0 {
		rps = 10
	}
	if burst <= 0 {
		burst = 20
	}
	_ = rps // see doc comment.

	return httprate.Limit(
		burst,
		time.Second,
		httprate.WithKeyFuncs(tenantKey),
		httprate.WithErrorHandler(func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "tenant not resolved", http.StatusInternalServerError)
		}),
		httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		}),
	)
}

func tenantKey(r *http.Request) (string, error) {
	t, ok := tenancy.TenantFromContext(r.Context())
	if !ok {
		return "", errMissingTenant
	}
	return t.PublicID, nil
}
