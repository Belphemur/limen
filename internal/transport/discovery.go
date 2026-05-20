package transport

import (
	"encoding/json"
	"net/http"
)

// DiscoveryHandler serves GET /auth/discovery. It returns the
// Zitadel issuer URL so that SPAs (admin/portal/staff) can build
// links to the IdP without hard-coding it.
//
// Response shape MUST match what the SPA's discoverSpaBasePath /
// session bootstrap expects in web/shared/session.
func DiscoveryHandler(issuer string) http.HandlerFunc {
	body, _ := json.Marshal(struct {
		ZitadelIssuer string `json:"zitadelIssuer"`
	}{ZitadelIssuer: issuer})
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}
}
