// Phase 6 MCP Resource Server middleware lives in this file.
//
// RequireMCPAuth validates the inbound bearer access token in-process
// against Zitadel's JWKS (single issuer, single key set, RS256 only),
// enforces the audience claim, binds the token's home org to the request
// tenant, and resolves the local *storage.User row before handing off to
// the MCP handler.
//
// On every failure the response carries an RFC 9728 WWW-Authenticate
// challenge with a `resource_metadata` pointer at /t/{tenant}/mcp/.well-
// known/oauth-protected-resource so MCP clients can discover the AS.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/zitadel/oidc/v3/pkg/client"
	"github.com/zitadel/oidc/v3/pkg/client/rp"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/mcprs"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

// MCPAuthConfig wires the resource-server middleware.
type MCPAuthConfig struct {
	// Issuer is the Zitadel issuer URL; must match the `iss` claim on
	// every accepted access token.
	Issuer string
	// JWKSURL overrides the jwks_uri discovered from Issuer. Optional; in
	// production leave empty and rely on discovery.
	JWKSURL string
	// Audience is the value the token's `aud` claim must contain.
	Audience string
	// HTTPClient is used for discovery + JWKS fetches. Defaults to a
	// 5-second-timeout client when nil.
	HTTPClient *http.Client
}

// MCPAuth bundles the verifier, metadata handler, and store for the
// resource-server pipeline. Construct one per process and reuse.
type MCPAuth struct {
	verifier *op.AccessTokenVerifier
	audience string
	metadata *mcprs.Handler
	store    *storage.Store
	logger   *zap.Logger
}

// NewMCPAuth discovers the jwks_uri (when not supplied), builds the
// access-token verifier with an RS256 allowlist, and returns a ready
// middleware factory.
func NewMCPAuth(ctx context.Context, cfg MCPAuthConfig, metadata *mcprs.Handler, store *storage.Store, logger *zap.Logger) (*MCPAuth, error) {
	if strings.TrimSpace(cfg.Issuer) == "" {
		return nil, errors.New("auth: MCPAuth issuer is required")
	}
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, errors.New("auth: MCPAuth audience is required")
	}
	if metadata == nil {
		return nil, errors.New("auth: MCPAuth metadata handler is required")
	}
	if store == nil {
		return nil, errors.New("auth: MCPAuth store is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}

	jwksURL := strings.TrimSpace(cfg.JWKSURL)
	if jwksURL == "" {
		disc, err := client.Discover(ctx, cfg.Issuer, httpClient)
		if err != nil {
			return nil, fmt.Errorf("auth: discover jwks_uri: %w", err)
		}
		jwksURL = disc.JwksURI
	}
	if jwksURL == "" {
		return nil, errors.New("auth: issuer discovery returned empty jwks_uri")
	}

	keys := rp.NewRemoteKeySet(httpClient, jwksURL)
	verifier := op.NewAccessTokenVerifier(
		cfg.Issuer,
		keys,
		op.WithSupportedAccessTokenSigningAlgorithms("RS256"),
	)
	return &MCPAuth{
		verifier: verifier,
		audience: cfg.Audience,
		metadata: metadata,
		store:    store,
		logger:   logger,
	}, nil
}

// RequireMCPAuth is the chi middleware that gates /t/{tenant}/mcp.
//
// Pipeline:
//  1. Extract Authorization: Bearer <token>.        Missing → 401.
//  2. Verify iss / signature / exp / nbf / alg.     Failure → 401.
//  3. Verify audience contains the configured aud.  Mismatch → 401.
//  4. Verify org_id claim matches tenant.OrgID.     Mismatch → 403.
//  5. Look up the local User row (no auto-provision).
//     Missing → 401.
//  6. Stash *User + claims on ctx and call next.
//
// Identity is taken ONLY from the bearer token. The MCP transport's
// `Mcp-Session-Id` header is opaque to this middleware: it carries no
// authentication weight, is never inspected here, and the bearer is
// re-validated on every request regardless of session continuity.
func (m *MCPAuth) RequireMCPAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := tenancy.MustTenant(r.Context())
		metadataURL := m.metadata.MetadataURL(t.PublicID)

		token := extractBearerToken(r)
		if token == "" {
			m.logger.Warn("mcp auth: missing bearer token",
				zap.String("tenant", t.PublicID),
				zap.String("path", r.URL.Path))
			mcprs.WriteChallenge(w, http.StatusUnauthorized, metadataURL,
				mcprs.ErrInvalidRequest, "missing bearer token")
			return
		}

		claims, err := op.VerifyAccessToken[*MCPAccessClaims](r.Context(), token, m.verifier)
		if err != nil {
			m.logger.Warn("mcp auth: access token verify failed",
				zap.String("tenant", t.PublicID), zap.Error(err))
			mcprs.WriteChallenge(w, http.StatusUnauthorized, metadataURL,
				mcprs.ErrInvalidToken, "token validation failed")
			return
		}

		if !audienceContains(claims.Audience, m.audience) {
			m.logger.Warn("mcp auth: audience mismatch",
				zap.String("tenant", t.PublicID),
				zap.Strings("got", claims.Audience),
				zap.String("want", m.audience))
			mcprs.WriteChallenge(w, http.StatusUnauthorized, metadataURL,
				mcprs.ErrInvalidToken, "audience mismatch")
			return
		}

		if claims.ResourceOwnerID == "" || claims.ResourceOwnerID != t.ZitadelOrgID {
			m.logger.Warn("cross-tenant token rejected",
				zap.String("tenant", t.PublicID),
				zap.String("want_org", t.ZitadelOrgID),
				zap.String("got_org", claims.ResourceOwnerID))
			mcprs.WriteChallenge(w, http.StatusForbidden, metadataURL,
				mcprs.ErrCrossTenantDenied, "token does not belong to this tenant")
			return
		}

		user, err := m.lookupUser(r.Context(), t.ID, claims.Subject)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				sa, saErr := m.lookupServiceAccount(r.Context(), claims.Subject)
				if saErr == nil {
					ctx := withMCPServiceAccount(r.Context(), sa)
					ctx = withMCPClaims(ctx, claims)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				m.logger.Info("unknown user for valid token",
					zap.String("tenant", t.PublicID),
					zap.String("sub", claims.Subject))
				mcprs.WriteChallenge(w, http.StatusUnauthorized, metadataURL,
					mcprs.ErrInvalidToken, "user not provisioned in this tenant")
				return
			}
			m.logger.Error("user lookup failed",
				zap.String("tenant", t.PublicID), zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		ctx := withMCPUser(r.Context(), user)
		ctx = withMCPClaims(ctx, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *MCPAuth) Verifier() *op.AccessTokenVerifier {
	return m.verifier
}

func (m *MCPAuth) lookupServiceAccount(ctx context.Context, zitadelUserID string) (*storage.ServiceAccount, error) {
	tx, commit, err := m.store.Session(ctx)
	if err != nil {
		return nil, err
	}
	var sa storage.ServiceAccount
	qerr := tx.Where("zitadel_user_id = ?", zitadelUserID).First(&sa).Error
	if cerr := commit(); cerr != nil && qerr == nil {
		return nil, cerr
	}
	if qerr != nil {
		return nil, qerr
	}
	return &sa, nil
}

func (m *MCPAuth) lookupUser(ctx context.Context, tenantID int64, subject string) (*storage.User, error) {
	_ = tenantID // tenant pinned on ctx by tenancy middleware; RLS scopes the SELECT.
	tx, commit, err := m.store.Session(ctx)
	if err != nil {
		return nil, err
	}
	var u storage.User
	qerr := tx.Where("zitadel_subject = ?", subject).First(&u).Error
	if cerr := commit(); cerr != nil && qerr == nil {
		return nil, cerr
	}
	if qerr != nil {
		return nil, qerr
	}
	return &u, nil
}

// MCPAccessClaims is the claim shape Limen requires on inbound access
// tokens. It embeds the OIDC base TokenClaims (iss/sub/aud/exp/...) and
// surfaces the Zitadel-specific resource-owner id used for tenant
// binding.
type MCPAccessClaims struct {
	oidc.TokenClaims
	Scope           oidc.SpaceDelimitedArray `json:"scope,omitempty"`
	ResourceOwnerID string                   `json:"urn:zitadel:iam:user:resourceowner:id,omitempty"`
}

// extractBearerToken returns the bearer token from Authorization, or "".
// Comparison is case-insensitive per RFC 6750 §2.1.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) < 7 {
		return ""
	}
	if !strings.EqualFold(auth[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(auth[7:])
}

func audienceContains(got []string, want string) bool {
	return slices.Contains(got, want)
}

// ctx plumbing for the verified user + claims.

type mcpCtxKey int

const (
	ctxKeyMCPUser mcpCtxKey = iota + 1
	ctxKeyMCPClaims
)

func withMCPUser(ctx context.Context, u *storage.User) context.Context {
	return context.WithValue(ctx, ctxKeyMCPUser, u)
}

// MCPUserFromContext returns the *storage.User pinned by RequireMCPAuth.
func MCPUserFromContext(ctx context.Context) (*storage.User, bool) {
	u, ok := ctx.Value(ctxKeyMCPUser).(*storage.User)
	return u, ok && u != nil
}

func withMCPClaims(ctx context.Context, c *MCPAccessClaims) context.Context {
	return context.WithValue(ctx, ctxKeyMCPClaims, c)
}

// MCPClaimsFromContext returns the verified access-token claims.
func MCPClaimsFromContext(ctx context.Context) (*MCPAccessClaims, bool) {
	c, ok := ctx.Value(ctxKeyMCPClaims).(*MCPAccessClaims)
	return c, ok && c != nil
}

const ctxKeyMCPServiceAccount mcpCtxKey = iota + 3

func withMCPServiceAccount(ctx context.Context, sa *storage.ServiceAccount) context.Context {
	return context.WithValue(ctx, ctxKeyMCPServiceAccount, sa)
}

// MCPServiceAccountFromContext returns the *storage.ServiceAccount pinned by RequireMCPAuth.
func MCPServiceAccountFromContext(ctx context.Context) (*storage.ServiceAccount, bool) {
	sa, ok := ctx.Value(ctxKeyMCPServiceAccount).(*storage.ServiceAccount)
	return sa, ok && sa != nil
}
