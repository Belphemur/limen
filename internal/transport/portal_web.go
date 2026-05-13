package transport

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/auth"
)

//go:embed web/portal/*
var portalAssetsFS embed.FS

// portalAssets is the subtree rooted at web/portal so handlers can serve
// "index.html" / "app.js" by their bare filenames.
var portalAssets = func() fs.FS {
	sub, err := fs.Sub(portalAssetsFS, "web/portal")
	if err != nil {
		// embed guarantees the path; a build that loses it should fail loud.
		panic(err)
	}
	return sub
}()

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

func portalStaticHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
		if rel == "" {
			rel = "index.html"
		}
		f, err := portalAssets.Open(rel)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = f.Close() }()
		stat, err := f.Stat()
		if err != nil || stat.IsDir() {
			http.NotFound(w, r)
			return
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, rel, time.Time{}, rs)
	}
}
