package resilience

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/belphemur/limen/internal/config"
	"github.com/sony/gobreaker/v2"
	"go.uber.org/zap"
)

type breakerExecutor interface {
	Execute(func() (struct{}, error)) (struct{}, error)
}

type breakerTransport struct {
	base    http.RoundTripper
	breaker breakerExecutor
	name    string
}

func buildBreakerSettings(name string, cfg config.ResiliencePolicy, logger *zap.Logger, distributed bool) gobreaker.Settings {
	prefix := "circuit breaker"
	if distributed {
		prefix = "distributed circuit breaker"
	}
	return gobreaker.Settings{
		Name: name,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= uint32(cfg.BreakerConsecutiveFails)
		},
		Timeout: cfg.BreakerOpenDuration,
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			logger.Info(prefix+" state changed",
				zap.String("dependency", name),
				zap.String("from", from.String()),
				zap.String("to", to.String()),
			)
		},
	}
}

func newBreakerTransport(name string, cfg config.ResiliencePolicy, base http.RoundTripper, logger *zap.Logger) http.RoundTripper {
	settings := buildBreakerSettings(name, cfg, logger, false)
	return &breakerTransport{
		base:    base,
		breaker: gobreaker.NewCircuitBreaker[struct{}](settings),
		name:    name,
	}
}

func newDistributedBreakerTransport(name string, cfg config.ResiliencePolicy, base http.RoundTripper, store gobreaker.SharedDataStore, logger *zap.Logger) (bt http.RoundTripper) {
	settings := buildBreakerSettings(name, cfg, logger, true)
	var dcb *gobreaker.DistributedCircuitBreaker[struct{}]
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic creating distributed circuit breaker: %v", r)
			}
		}()
		dcb, err = gobreaker.NewDistributedCircuitBreaker[struct{}](store, settings)
	}()
	if err != nil {
		logger.Error("failed to create distributed circuit breaker, falling back to local", zap.Error(err))
		return newBreakerTransport(name, cfg, base, logger)
	}
	return &breakerTransport{
		base:    base,
		breaker: dcb,
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
