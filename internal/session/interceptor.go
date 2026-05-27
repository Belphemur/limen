package session

import (
	"context"
	"path"
	"strings"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/tenancy"
)

// TenancyInterceptor is the first interceptor in every SPA-facing
// service stack. It is defense in depth: tenancy.RequireTenant HTTP
// middleware should already have resolved the tenant before any RPC
// dispatches; this surfaces a misconfigured mount as CodeNotFound
// instead of a nil-pointer panic deeper in the handler.
func TenancyInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if _, ok := tenancy.TenantFromContext(ctx); !ok {
				return nil, errNotFound("tenant not bound to ctx — mount misconfigured")
			}
			return next(ctx, req)
		}
	}
}

// Interceptor decrypts the limen_portal cookie, validates the ID
// token, refreshes transparently on expiry, and pins UserSession into
// ctx. Every Connect RPC behind this interceptor is guaranteed to see
// an authenticated user via MustUser(ctx) — including the shared
// SessionService.GetSession, which is the first RPC each SPA calls and
// is the place 401s surface for unauthenticated callers.
//
// The cookie's AEAD AAD includes the URL-derived tenant public id; a
// cookie minted for tenant A cannot be decrypted under tenant B, so
// cross-tenant cookie replay fails at the cryptographic layer rather
// than via a string comparison.
func Interceptor(resolve Resolver, logger *zap.Logger) connect.UnaryInterceptorFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if _, ok := UserFromContext(ctx); ok {
				return next(ctx, req)
			}

			t := tenancy.MustTenant(ctx)

			sess, setCookie, err := resolve(ctx, req.Header(), t.PublicID)
			if err != nil || sess == nil {
				logger.Debug("session reject",
					zap.String("procedure", req.Spec().Procedure),
					zap.String("tenant", t.PublicID),
					zap.Error(err))
				return nil, errUnauthenticated("session invalid")
			}

			resp, rerr := next(WithUser(ctx, sess), req)
			if rerr == nil && setCookie != nil && resp != nil {
				resp.Header().Add("Set-Cookie", setCookie.String())
			}
			return resp, rerr
		}
	}
}

// RoleInterceptor enforces a per-procedure minimum role. Unknown
// procedures default-deny. Pass the requiredRole map owned by the
// service being mounted (portal, admin, staff each have their own).
//
// Services without per-RPC role differences (currently only
// SessionService) should omit this interceptor entirely rather than
// pass an empty map.
func RoleInterceptor(requiredRole map[string]Role, logger *zap.Logger) connect.UnaryInterceptorFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			proc := ProcedureMethod(req.Spec().Procedure)
			need, known := requiredRole[proc]
			if !known {
				logger.Warn("session: unknown procedure default-deny", zap.String("procedure", proc))
				return nil, errPermissionDenied("unknown procedure")
			}
			sess := MustUser(ctx)
			if !Satisfies(sess.Roles, need) {
				logger.Info("session: role denied",
					zap.String("procedure", proc),
					zap.String("subject", sess.Subject),
					zap.Strings("roles", sess.Roles),
					zap.String("need", string(need)))
				return nil, errPermissionDenied("insufficient role")
			}
			return next(ctx, req)
		}
	}
}

// ProcedureMethod returns the trailing method name from a Connect
// procedure path (e.g. "/limen.session.v1.SessionService/GetSession" →
// "GetSession"). Matches the keys of per-service requiredRole maps.
func ProcedureMethod(procedure string) string {
	procedure = strings.TrimPrefix(procedure, "/")
	return path.Base(procedure)
}
