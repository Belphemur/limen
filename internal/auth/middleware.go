package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

type Middleware struct {
	logger   *zap.Logger
	jwksURL  string
	audience string
}

func NewMiddleware(logger *zap.Logger, jwksURL, audience string) *Middleware {
	return &Middleware{
		logger:   logger,
		jwksURL:  jwksURL,
		audience: audience,
	}
}

func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, `{"error":"missing authorization"}`, http.StatusUnauthorized)
			return
		}

		ctx, err := m.validateToken(r.Context(), token)
		if err != nil {
			m.logger.Warn("token validation failed", zap.Error(err))
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

func (m *Middleware) validateToken(ctx context.Context, token string) (context.Context, error) {
	if m.jwksURL == "" {
		return ctx, nil
	}

	// TODO: integrate with your JWT validation library
	// Example with gopkg.in/square/go-jose.v2 + JWKS:
	// - Fetch JWKS from m.jwksURL
	// - Parse and validate token
	// - Check audience matches m.audience
	// - Return context with claims

	return ctx, fmt.Errorf("JWKS validation not yet implemented")
}

type contextKey string

const claimsKey contextKey = "auth_claims"

func SetClaims(ctx context.Context, claims map[string]any) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

func GetClaims(ctx context.Context) map[string]any {
	if claims, ok := ctx.Value(claimsKey).(map[string]any); ok {
		return claims
	}
	return nil
}
