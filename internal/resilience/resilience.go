package resilience

import (
	"net/http"

	"github.com/belphemur/limen/internal/config"
	"go.uber.org/zap"
)

// Client builds an *http.Client whose transport is layered as:
//
//	[retry with backoff] → [circuit breaker] → http.DefaultTransport
//
// The breaker name is `name` for logging. Retry policy is driven by cfg.
// If logger is nil, a no-op logger is used.
func Client(name string, cfg config.ResiliencePolicy, logger *zap.Logger) *http.Client {
	if logger == nil {
		logger = zap.NewNop()
	}
	logger = logger.Named("resilience")

	base := http.DefaultTransport
	breaker := newBreakerTransport(name, cfg, base, logger)
	retry := newRetryTransport(cfg, breaker, logger)

	return &http.Client{
		Transport: retry,
	}
}
