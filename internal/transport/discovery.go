package transport

import (
	"encoding/json"
	"net/http"
)

// DiscoveryHandler serves GET /auth/discovery. It returns the
// Zitadel issuer URL plus the captcha provider + site key so SPAs
// (admin/portal/staff/signup) can build links to the IdP and
// lazy-load the right captcha widget without hard-coding either.
//
// Response shape MUST match what the SPAs' bootstrap consumers
// expect in web/shared and web/admin/src/pages/Signup*.vue.
func DiscoveryHandler(issuer, captchaProvider, captchaSiteKey string) http.HandlerFunc {
	body, _ := json.Marshal(struct {
		ZitadelIssuer   string `json:"zitadelIssuer"`
		CaptchaProvider string `json:"captchaProvider"`
		CaptchaSiteKey  string `json:"captchaSiteKey"`
	}{
		ZitadelIssuer:   issuer,
		CaptchaProvider: captchaProvider,
		CaptchaSiteKey:  captchaSiteKey,
	})
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}
}
