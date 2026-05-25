package resilience

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/belphemur/limen/internal/config"
	"github.com/sony/gobreaker/v2"
	"go.uber.org/zap"
)

type breakerTransport struct {
	base    http.RoundTripper
	breaker *gobreaker.CircuitBreaker[struct{}]
	name    string
}

func newBreakerTransport(name string, cfg config.ResiliencePolicy, base http.RoundTripper, logger *zap.Logger) http.RoundTripper {
	settings := gobreaker.Settings{
		Name: name,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= uint32(cfg.BreakerConsecutiveFails)
		},
		Timeout: cfg.BreakerOpenDuration,
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			logger.Info("circuit breaker state changed",
				zap.String("dependency", name),
				zap.String("from", from.String()),
				zap.String("to", to.String()),
			)
		},
	}

	return &breakerTransport{
		base:    base,
		breaker: gobreaker.NewCircuitBreaker[struct{}](settings),
		name:    name,
	}
}

func (t *breakerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var roundTripErr error

	_, breakerErr := t.breaker.Execute(func() (struct{}, error) {
		resp, roundTripErr = t.base.RoundTrip(req)
		if roundTripErr != nil {
			return struct{}{}, roundTripErr
		}
		if resp.StatusCode >= 500 {
			return struct{}{}, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return struct{}{}, nil
	})

	if errors.Is(breakerErr, gobreaker.ErrOpenState) || errors.Is(breakerErr, gobreaker.ErrTooManyRequests) {
		return nil, &BreakerOpenError{Name: t.name}
	}

	if roundTripErr != nil {
		return nil, roundTripErr
	}

	return resp, nil
}
