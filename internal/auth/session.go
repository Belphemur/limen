package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/zitadel"
)

// sessionKind is the crypto AAD `Kind` bound to every portal cookie. Pairs
// with the tenant slug in AAD.TenantID so a cookie minted for tenant A
// cannot decrypt under tenant B.
const sessionKind = "portal.session"

// sessionCacheTTL is the positive-cache window for the Zitadel GetSession
// liveness check. Short enough that a server-side logout propagates
// quickly, long enough that the IdP doesn't see a request per protected
// page load.
const sessionCacheTTL = 60 * time.Second

// SessionData is the payload sealed inside the portal cookie.
type SessionData struct {
	ZitadelSID   string    `json:"sid"`
	ZitadelToken string    `json:"stk"`
	Subject      string    `json:"sub"`           // Zitadel user id
	LocalUserID  int64     `json:"uid,omitempty"` // limen users.id, 0 if not yet mirrored
	Email        string    `json:"em,omitempty"`
	Roles        []string  `json:"r,omitempty"`
	IssuedAt     time.Time `json:"iat"`
	ExpiresAt    time.Time `json:"exp"`
}

// SessionConfig wires a SessionManager.
type SessionConfig struct {
	// Cipher is the AES-SIV primitive that seals the cookie payload. Must
	// be non-nil — typically `crypto.ActiveCipher()` after startup wires
	// the master key.
	Cipher *crypto.Cipher
	// Zitadel is used by RequirePortalSession to confirm the upstream
	// session has not been revoked. May be nil in tests that bypass the
	// liveness check via `WithSessionLivenessCheck(false)`.
	Zitadel *zitadel.Client
	// CookieName is the browser cookie name (e.g. "limen_portal").
	CookieName string
	// CookieSecure controls the Secure attribute. Set false only for
	// local dev over plain HTTP.
	CookieSecure bool
	// Lifetime is the cookie's absolute TTL.
	Lifetime time.Duration
	// SkipLivenessCheck disables the Zitadel GetSession call in
	// RequirePortalSession. Tests only.
	SkipLivenessCheck bool
}

func (c SessionConfig) validate() error {
	if c.Cipher == nil {
		return errors.New("auth: SessionConfig.Cipher is required")
	}
	if c.CookieName == "" {
		return errors.New("auth: SessionConfig.CookieName is required")
	}
	if c.Lifetime <= 0 {
		return errors.New("auth: SessionConfig.Lifetime must be positive")
	}
	if !c.SkipLivenessCheck && c.Zitadel == nil {
		return errors.New("auth: SessionConfig.Zitadel is required unless SkipLivenessCheck=true")
	}
	return nil
}

// SessionManager seals/opens portal cookies and exposes the middlewares
// that gate /t/{slug}/... routes.
type SessionManager struct {
	cfg   SessionConfig
	cache sync.Map // sid -> cacheEntry
	now   func() time.Time
}

type cacheEntry struct {
	until   time.Time
	revoked bool
}

// NewSessionManager validates cfg and returns a manager.
func NewSessionManager(cfg SessionConfig) (*SessionManager, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &SessionManager{cfg: cfg, now: time.Now}, nil
}

// Issue seals data and writes the cookie scoped to /t/{slug}.
func (sm *SessionManager) Issue(w http.ResponseWriter, slug string, data SessionData) error {
	if slug == "" {
		return errors.New("auth: Issue: slug is required")
	}
	if data.IssuedAt.IsZero() {
		data.IssuedAt = sm.now().UTC()
	}
	if data.ExpiresAt.IsZero() {
		data.ExpiresAt = data.IssuedAt.Add(sm.cfg.Lifetime)
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("auth: marshal session: %w", err)
	}
	sealed, err := sm.cfg.Cipher.Encrypt(payload, crypto.AAD{TenantID: slug, Kind: sessionKind})
	if err != nil {
		return fmt.Errorf("auth: encrypt session: %w", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sm.cfg.CookieName,
		Value:    base64.RawURLEncoding.EncodeToString(sealed),
		Path:     "/t/" + slug,
		Expires:  data.ExpiresAt,
		MaxAge:   int(time.Until(data.ExpiresAt).Seconds()),
		HttpOnly: true,
		Secure:   sm.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// Clear deletes the cookie for the given tenant slug.
func (sm *SessionManager) Clear(w http.ResponseWriter, slug string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sm.cfg.CookieName,
		Value:    "",
		Path:     "/t/" + slug,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   sm.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ErrNoSession is returned by Load when the cookie is absent.
var ErrNoSession = errors.New("auth: no portal session cookie")

// ErrSessionExpired is returned by Load when the sealed payload has expired.
var ErrSessionExpired = errors.New("auth: portal session expired")

// Load extracts and decrypts the cookie for the given slug. Returns
// ErrNoSession if absent, ErrSessionExpired if expired, or a generic error
// for tampering / malformed cookies (do not echo the detail to clients).
func (sm *SessionManager) Load(r *http.Request, slug string) (*SessionData, error) {
	c, err := r.Cookie(sm.cfg.CookieName)
	if err != nil {
		return nil, ErrNoSession
	}
	if c.Value == "" {
		return nil, ErrNoSession
	}
	sealed, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil, fmt.Errorf("auth: decode session cookie: %w", err)
	}
	payload, err := sm.cfg.Cipher.Decrypt(sealed, crypto.AAD{TenantID: slug, Kind: sessionKind})
	if err != nil {
		return nil, fmt.Errorf("auth: decrypt session cookie: %w", err)
	}
	var data SessionData
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil, fmt.Errorf("auth: unmarshal session cookie: %w", err)
	}
	if sm.now().After(data.ExpiresAt) {
		return nil, ErrSessionExpired
	}
	return &data, nil
}

// invalidate marks a session id as revoked in the local cache so subsequent
// requests fail fast even if their cookie has not expired.
func (sm *SessionManager) invalidate(sid string) {
	sm.cache.Store(sid, cacheEntry{until: sm.now().Add(sessionCacheTTL), revoked: true})
}

// checkLiveness calls Zitadel.GetSession with a short positive cache. A
// revoked session is cached for sessionCacheTTL to bound the rate at which
// we hammer the IdP for known-bad sessions.
func (sm *SessionManager) checkLiveness(ctx context.Context, sid, token string) error {
	if sm.cfg.SkipLivenessCheck {
		return nil
	}
	now := sm.now()
	if v, ok := sm.cache.Load(sid); ok {
		e := v.(cacheEntry)
		if now.Before(e.until) {
			if e.revoked {
				return errors.New("auth: session revoked")
			}
			return nil
		}
	}
	if _, err := sm.cfg.Zitadel.GetSession(ctx, sid, token); err != nil {
		sm.cache.Store(sid, cacheEntry{until: now.Add(sessionCacheTTL), revoked: true})
		return fmt.Errorf("auth: zitadel liveness: %w", err)
	}
	sm.cache.Store(sid, cacheEntry{until: now.Add(sessionCacheTTL)})
	return nil
}

// ctxKey is unexported so other packages must go through helpers.
type ctxKey int

const ctxKeySession ctxKey = iota + 1

// WithSession pins a SessionData onto ctx.
func WithSession(ctx context.Context, s *SessionData) context.Context {
	return context.WithValue(ctx, ctxKeySession, s)
}

// SessionFromContext returns the session bound to ctx, or nil/false.
func SessionFromContext(ctx context.Context) (*SessionData, bool) {
	s, ok := ctx.Value(ctxKeySession).(*SessionData)
	return s, ok && s != nil
}

// MustSession returns the session or panics. Use inside handlers protected
// by RequirePortalSession.
func MustSession(ctx context.Context) *SessionData {
	s, ok := SessionFromContext(ctx)
	if !ok {
		panic("auth: session not in context — missing RequirePortalSession middleware")
	}
	return s
}

// RequirePortalSession produces a middleware that decrypts the portal
// cookie, confirms the upstream Zitadel session is alive (60s cache), and
// pins the session onto the request context. Must run after
// tenancy.RequireTenant so the slug is available for cookie scoping.
//
// Failure modes:
//   - missing/expired cookie → 302 to /t/{slug}/auth/login
//   - revoked / Zitadel rejection → cookie cleared + 302 to login
//   - decrypt failure → log warning + 302 to login (likely tamper / key rotation)
func (sm *SessionManager) RequirePortalSession(logger *zap.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t, ok := tenancy.TenantFromContext(r.Context())
			if !ok {
				http.Error(w, "tenant not resolved", http.StatusInternalServerError)
				return
			}
			data, err := sm.Load(r, t.Slug)
			if err != nil {
				if !errors.Is(err, ErrNoSession) {
					logger.Debug("portal session load failed", zap.String("slug", t.Slug), zap.Error(err))
					sm.Clear(w, t.Slug)
				}
				redirectToLogin(w, r, t.Slug)
				return
			}
			if err := sm.checkLiveness(r.Context(), data.ZitadelSID, data.ZitadelToken); err != nil {
				logger.Debug("portal session liveness failed", zap.String("slug", t.Slug), zap.String("sid", data.ZitadelSID), zap.Error(err))
				sm.Clear(w, t.Slug)
				redirectToLogin(w, r, t.Slug)
				return
			}
			ctx := WithSession(r.Context(), data)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole gates handlers behind one of the named roles. Roles are
// matched case-sensitively against SessionData.Roles. An empty `roles`
// argument list rejects every request.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	want := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		want[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s, ok := SessionFromContext(r.Context())
			if !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			for _, have := range s.Roles {
				if _, hit := want[have]; hit {
					next.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, "forbidden", http.StatusForbidden)
		})
	}
}

// redirectToLogin sends the browser to the tenant's login endpoint while
// preserving the original target as ?return_to. The return_to is path+query
// only — we never echo back a full URL, which would let an attacker craft
// open-redirect bait.
func redirectToLogin(w http.ResponseWriter, r *http.Request, slug string) {
	target := r.URL.RequestURI()
	if !strings.HasPrefix(target, "/") {
		target = "/"
	}
	dest := "/t/" + slug + "/auth/login?return_to=" + urlEncode(target)
	http.Redirect(w, r, dest, http.StatusFound)
}

// urlEncode wraps url.QueryEscape via a tiny shim so the function stays
// dependency-free at the top of the file.
func urlEncode(s string) string {
	// Inlined replacement table: just escape the few chars that matter
	// for path+query in a Location header. Full url.QueryEscape would
	// also work but pulls in net/url solely for this one call.
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '&' || c == '?' || c == '#' || c == '%' || c == ' ' || c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&b, "%%%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
