// Package signupmount builds the SignupService Connect handler for
// binaries that host the public signup wizard (portal, all-in-one).
// Mounted at the root /api/ prefix — NOT under /t/{tenant}/ — because
// SignupService is tenant-agnostic.
package signupmount

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/belphemur/limen/internal/boot"
	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/mailer"
	"github.com/belphemur/limen/internal/signup"
)

// NewHandler returns the URL-path prefix + http.Handler pair for
// SignupService alongside the constructed *signup.Service (so the
// caller can launch the sweeper goroutine). zclient may be nil when
// signup is disabled — disabled mode never invokes Zitadel.
//
// The caller mounts the handler on the root chi router under /api/
// (see transport.MountPortal).
func NewHandler(rt *boot.Runtime, zclient signup.ZitadelClient) (string, http.Handler, *signup.Service, error) {
	cfg := rt.Cfg

	tpl, err := mailer.LoadSignupVerifyTemplate()
	if err != nil {
		return "", nil, nil, fmt.Errorf("signupmount: load template: %w", err)
	}

	var mail mailer.Mailer
	if cfg.Signup.Enabled {
		mail, err = mailer.NewSMTPMailer(mailer.SMTPConfig{
			Host:     cfg.Mailer.SMTP.Host,
			Port:     cfg.Mailer.SMTP.Port,
			From:     cfg.Mailer.SMTP.From,
			Username: cfg.Mailer.SMTP.Username,
			Password: cfg.Mailer.SMTP.Password,
			TLS:      cfg.Mailer.SMTP.TLS,
		})
		if err != nil {
			return "", nil, nil, fmt.Errorf("signupmount: build mailer: %w", err)
		}
	} else {
		mail = mailer.NopMailer{}
	}

	captcha, err := buildVerifier(cfg.Captcha.Provider, cfg.Captcha.SecretKey)
	if err != nil {
		return "", nil, nil, fmt.Errorf("signupmount: build captcha verifier: %w", err)
	}

	limiter := signup.NewPerIPLimiter(cfg.Signup.RateLimit.PerHour, cfg.Signup.RateLimit.Burst)

	key, err := crypto.ParseKey(cfg.Security.TokenEncryptionKey)
	if err != nil {
		return "", nil, nil, fmt.Errorf("signupmount: parse token key: %w", err)
	}

	deps := signup.Deps{
		Store:          rt.Store,
		Mailer:         mail,
		Template:       tpl,
		Zitadel:        zclient,
		Captcha:        captcha,
		Limiter:        limiter,
		Logger:         rt.Logger.Named("signup"),
		Enabled:        cfg.Signup.Enabled,
		BaseURL:        cfg.Server.BaseURL,
		ZitadelIssuer:  cfg.OIDC.Issuer,
		VerifyTokenTTL: cfg.Signup.VerifyTokenTTL,
		TokenKey:       key[:],
	}

	svc := signup.NewService(deps)
	prefix, handler := svc.Handler()
	return prefix, handler, svc, nil
}

func buildVerifier(provider, secret string) (signup.Verifier, error) {
	switch provider {
	case "", "none":
		return signup.DevBypassVerifier{}, nil
	case "hcaptcha":
		if secret == "" {
			return nil, errors.New("hcaptcha selected but secret_key is empty")
		}
		return signup.NewHCaptchaVerifier(secret), nil
	case "turnstile":
		if secret == "" {
			return nil, errors.New("turnstile selected but secret_key is empty")
		}
		return signup.NewTurnstileVerifier(secret), nil
	default:
		return nil, fmt.Errorf("unknown captcha provider %q", provider)
	}
}
