package resilience

import (
	"net/http"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/valkey"
	"go.uber.org/zap"
)

// Client builds an *http.Client whose transport is layered as:
//
//	[retry with backoff] → [circuit breaker] → http.DefaultTransport
//
// The breaker name is `name` for logging. Retry policy is driven by cfg.
// If logger is nil, a no-op logger is used.
//
// When valkeyClient is non-nil a distributed circuit breaker is constructed
// backed by Valkey so every instance sharing the same store sees the same
// breaker state. When nil (or omitted) a local-only breaker is used.
func Client(name string, cfg config.ResiliencePolicy, logger *zap.Logger, valkeyClient valkey.Client) *http.Client {
	if logger == nil {
		logger = zap.NewNop()
	}
	logger = logger.Named("resilience")

	base := http.DefaultTransport

	var breaker http.RoundTripper
	if valkeyClient != nil {
		store := NewValkeyStore(valkeyClient)
		breaker = newDistributedBreakerTransport(name, cfg, base, store, logger)
		logger.Info("using distributed circuit breaker backed by Valkey",
			zap.String("dependency", name),
		)
	} else {
		logger.Debug("using local circuit breaker", zap.String("dependency", name))
		breaker = newBreakerTransport(name, cfg, base, logger)
	}

	retry := newRetryTransport(cfg, breaker, logger)

	return &http.Client{
		Transport: retry,
	}
}
