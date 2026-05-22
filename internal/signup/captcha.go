package signup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Verifier validates a captcha response token. err == nil means the
// token is accepted; any non-nil error means deny. Implementations
// must be safe for concurrent use.
type Verifier interface {
	Verify(ctx context.Context, token, remoteIP string) error
}

// DevBypassVerifier accepts the sentinel "dev-captcha-bypass" only.
// Used in dev / test / when captcha.provider="none". An empty token
// is rejected.
type DevBypassVerifier struct{}

// Verify accepts the dev sentinel and rejects anything else.
func (DevBypassVerifier) Verify(_ context.Context, token, _ string) error {
	if token == "dev-captcha-bypass" {
		return nil
	}
	return errors.New("captcha token rejected (dev bypass requires \"dev-captcha-bypass\")")
}

// HCaptchaVerifier calls the hCaptcha siteverify endpoint with the
// configured secret. The endpoint URL is overridable for tests.
type HCaptchaVerifier struct {
	Secret string
	// URL is "https://api.hcaptcha.com/siteverify" by default.
	URL    string
	Client *http.Client
}

// NewHCaptchaVerifier returns a Verifier bound to the production
// hCaptcha siteverify endpoint.
func NewHCaptchaVerifier(secret string) *HCaptchaVerifier {
	return &HCaptchaVerifier{
		Secret: secret,
		URL:    "https://api.hcaptcha.com/siteverify",
		Client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Verify posts the secret + token to hCaptcha and checks the JSON
// response's success field.
func (v *HCaptchaVerifier) Verify(ctx context.Context, token, remoteIP string) error {
	return postSiteverify(ctx, v.Client, v.URL, v.Secret, token, remoteIP)
}

// TurnstileVerifier mirrors HCaptchaVerifier for Cloudflare Turnstile.
type TurnstileVerifier struct {
	Secret string
	URL    string
	Client *http.Client
}

// NewTurnstileVerifier returns a Verifier bound to the production
// Cloudflare Turnstile siteverify endpoint.
func NewTurnstileVerifier(secret string) *TurnstileVerifier {
	return &TurnstileVerifier{
		Secret: secret,
		URL:    "https://challenges.cloudflare.com/turnstile/v0/siteverify",
		Client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Verify posts the secret + token to Turnstile.
func (v *TurnstileVerifier) Verify(ctx context.Context, token, remoteIP string) error {
	return postSiteverify(ctx, v.Client, v.URL, v.Secret, token, remoteIP)
}

func postSiteverify(ctx context.Context, client *http.Client, endpoint, secret, token, remoteIP string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("captcha: missing token")
	}
	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("captcha: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("captcha: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Success    bool     `json:"success"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("captcha: decode response: %w", err)
	}
	if !body.Success {
		return fmt.Errorf("captcha: rejected (codes=%v)", body.ErrorCodes)
	}
	return nil
}
