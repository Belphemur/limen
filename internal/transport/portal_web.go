package transport

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/belphemur/limen/internal/auth"
)

//go:embed web/portal/index.html
var portalIndexHTML []byte

// meResponse is the JSON payload returned by GET /portal/me. The fields
// mirror the subset of the live ID-token claims a browser SPA needs to
// render a "who am I" view.
type meResponse struct {
	Subject string   `json:"sub"`
	Email   string   `json:"email,omitempty"`
	Name    string   `json:"name,omitempty"`
	Roles   []string `json:"roles"`
	Exp     int64    `json:"exp,omitempty"`
}

func portalMeHandler(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	resp := meResponse{
		Subject: claims.GetSubject(),
		Email:   claims.Email,
		Name:    claims.Name,
		Roles:   auth.ExtractRoles(claims),
	}
	if exp := claims.GetExpiration(); !exp.IsZero() {
		resp.Exp = exp.Unix()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// portalStaticHandler serves the single self-contained portal page. All
// CSS + JS is inlined into the HTML; there are no other static assets to
// route. Phase 4 is a POC — Phase 9 will replace this with a real SPA.
func portalStaticHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(portalIndexHTML)
	}
}
