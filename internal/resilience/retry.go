package resilience

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/belphemur/limen/internal/config"
	"github.com/cenkalti/backoff/v4"
	"go.uber.org/zap"
)

type retryTransport struct {
	base   http.RoundTripper
	cfg    config.ResiliencePolicy
	logger *zap.Logger
}

func newRetryTransport(cfg config.ResiliencePolicy, base http.RoundTripper, logger *zap.Logger) http.RoundTripper {
	return &retryTransport{
		base:   base,
		cfg:    cfg,
		logger: logger,
	}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = t.cfg.BaseBackoff
	bo.MaxInterval = t.cfg.MaxBackoff
	bo.MaxElapsedTime = 0

	ctx := req.Context()
	var (
		resp        *http.Response
		err         error
		skipBackoff bool
	)

	for attempt := 0; attempt <= t.cfg.MaxRetries; attempt++ {
		if attempt > 0 && !skipBackoff {
			d := bo.NextBackOff()
			if d == backoff.Stop {
				break
			}

			t.logger.Debug("retrying HTTP request",
				zap.Int("attempt", attempt),
				zap.Duration("backoff", d),
				zap.Error(err),
			)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(d):
			}
		}
		skipBackoff = false

		resp, err = t.base.RoundTrip(req)
		if err != nil {
			var breakerOpen *BreakerOpenError
			if errors.As(err, &breakerOpen) {
				return nil, err
			}
			if attempt == t.cfg.MaxRetries {
				return nil, err
			}
			continue
		}

		switch resp.StatusCode {
		case http.StatusTooManyRequests: // 429
			if attempt == t.cfg.MaxRetries {
				return resp, nil
			}

			var sleep time.Duration
			if after := resp.Header.Get("Retry-After"); after != "" {
				if secs, parseErr := strconv.Atoi(after); parseErr == nil && secs > 0 {
					sleep = time.Duration(secs) * time.Second
					_ = bo.NextBackOff() // consume one backoff tick to keep state consistent
				}
			}
			if sleep == 0 {
				sleep = bo.NextBackOff()
				if sleep == backoff.Stop {
					return resp, nil
				}
			}

			resp.Body.Close()

			t.logger.Debug("retrying after 429",
				zap.Int("attempt", attempt+1),
				zap.Duration("sleep", sleep),
			)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(sleep):
			}

			skipBackoff = true

		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			if attempt == t.cfg.MaxRetries {
				resp.Body.Close()
				return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			resp.Body.Close()
			continue

		case http.StatusUnauthorized, http.StatusRequestTimeout:
			return resp, nil

		default:
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return resp, nil
			}
			return resp, nil
		}
	}

	if err != nil {
		return nil, err
	}
	if resp != nil {
		return resp, nil
	}
	return nil, fmt.Errorf("exhausted retries")
}
