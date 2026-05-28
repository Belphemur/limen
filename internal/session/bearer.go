package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

// bearerClaims is the JWT claim shape the BearerTokenInterceptor
// validates. It embeds the standard OIDC TokenClaims (iss/sub/aud/exp/…)
// and surfaces the Zitadel-specific resource-owner id used for tenant
// binding.
type bearerClaims struct {
	oidc.TokenClaims
	ResourceOwnerID string `json:"urn:zitadel:iam:user:resourceowner:id,omitempty"`
}

// BearerTokenInterceptor validates a Bearer token (PAT) against Zitadel JWKS,
// resolves the identity (service account or user) from the local DB, synthesizes
// a UserSession, and pins it on ctx. When no Bearer token is present, it passes
// through to let the cookie interceptor handle authentication.
//
// Construct with `BearerTokenInterceptor(cfg, store, logger)`.
func BearerTokenInterceptor(cfg BearerTokenConfig, store *storage.Store, logger *zap.Logger) connect.UnaryInterceptorFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if _, ok := UserFromContext(ctx); ok {
				return next(ctx, req)
			}

			token := extractBearerToken(req.Header())
			if token == "" {
				return next(ctx, req)
			}

			t := tenancy.MustTenant(ctx)

			claims, err := op.VerifyAccessToken[*bearerClaims](ctx, token, cfg.Verifier)
			if err != nil {
				return nil, errUnauthenticated("invalid bearer token")
			}

			if !slices.Contains(claims.Audience, cfg.Audience) {
				return nil, errPermissionDenied("audience mismatch")
			}

			if claims.ResourceOwnerID == "" || claims.ResourceOwnerID != t.ZitadelOrgID {
				return nil, errPermissionDenied("token does not belong to this tenant")
			}

			db, commit, err := store.Session(ctx)
			if err != nil {
				return nil, errInternal("store session", err)
			}
			defer func() {
				if cerr := commit(); cerr != nil {
					logger.Error("failed to close db session", zap.Error(cerr))
				}
			}()

			var sa storage.ServiceAccount
			saErr := db.Where("zitadel_user_id = ?", claims.Subject).First(&sa).Error
			if saErr == nil {
				sess := &UserSession{
					Subject:   claims.Subject,
					Roles:     []string{sa.Role},
					Email:     sa.Name,
					FirstName: sa.Name,
				}
				// Debounce: only write to DB if > 30s since last recorded usage.
				// The sa variable already has LastUsedAt populated from the SELECT above.
				now := time.Now()
				minInterval := 30 * time.Second
				if sa.LastUsedAt == nil || now.Sub(*sa.LastUsedAt) >= minInterval {
					if err := db.Model(&sa).Update("last_used_at", now).Error; err != nil {
						logger.Warn("bearer: update last_used_at failed", zap.Error(err))
					}
				}
				return next(WithUser(ctx, sess), req)
			}
			if saErr != nil && !errors.Is(saErr, gorm.ErrRecordNotFound) {
				return nil, errInternal("service account lookup", saErr)
			}

			var user storage.User
			if err := db.Where("zitadel_subject = ?", claims.Subject).First(&user).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, errUnauthenticated("unknown identity for valid token")
				}
				return nil, errInternal("user lookup", err)
			}

			// Human users should use browser login, not bearer tokens
			return nil, errPermissionDenied("bearer auth requires a service account — human users must use browser login")
		}
	}
}

// BearerTokenConfig holds the pieces needed to validate bearer tokens.
type BearerTokenConfig struct {
	Verifier *op.AccessTokenVerifier
	Audience string
}

// extractBearerToken returns the bearer token from Authorization header, or "".
func extractBearerToken(header http.Header) string {
	auth := header.Get("Authorization")
	if len(auth) < 7 {
		return ""
	}
	if !strings.EqualFold(auth[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(auth[7:])
}

func errInternal(desc string, err error) error {
	return connect.NewError(connect.CodeInternal, fmt.Errorf("session: %s: %w", desc, err))
}
