package portal

import (
	"context"
	"path"
	"strings"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/tenancy"
)

// ctx keys are package-private; readers go through the typed helpers
// below.
type ctxKey int

const (
	ctxKeySession ctxKey = iota + 1
)

// SessionFromContext returns the verified user session pinned by the
// session interceptor. Returns false on RPCs that ran without auth
// (only GetSession).
func SessionFromContext(ctx context.Context) (*UserSession, bool) {
	s, ok := ctx.Value(ctxKeySession).(*UserSession)
	return s, ok && s != nil
}

// withSession is unexported because only the session interceptor pins
// the value.
func withSession(ctx context.Context, s *UserSession) context.Context {
	return context.WithValue(ctx, ctxKeySession, s)
}

// tenancyInterceptor is the first interceptor in the stack. It is
// defense in depth: tenancy.RequireTenant HTTP middleware should already
// have resolved the tenant before any RPC dispatches; this surfaces a
// misconfigured mount as CodeNotFound instead of a nil-pointer panic
// deeper in the handler.
func tenancyInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if _, ok := tenancy.TenantFromContext(ctx); !ok {
				return nil, errNotFound("tenant not bound to ctx — mount misconfigured")
			}
			return next(ctx, req)
		}
	}
}

// sessionInterceptor decrypts the limen_portal cookie, validates the
// ID token, refreshes transparently on expiry, and pins UserSession
// into ctx. GetSession is the only RPC allowed through without a
// session — the SPA calls it on boot to discover whether it has one.
//
// The cookie's AEAD AAD includes the URL-derived tenant public id; a
// cookie minted for tenant A cannot be decrypted under tenant B, so
// cross-tenant cookie replay fails at the cryptographic layer rather
// than via a string comparison.
func sessionInterceptor(resolve SessionResolver, logger *zap.Logger) connect.UnaryInterceptorFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			proc := procedureMethod(req.Spec().Procedure)
			if requiredRole[proc] == RoleAny {
				return next(ctx, req)
			}
			t := tenancy.MustTenant(ctx)
			sess, setCookie, err := resolve(ctx, req.Header(), t.PublicID)
			if err != nil {
				logger.Debug("portal session reject",
					zap.String("procedure", proc),
					zap.String("tenant", t.PublicID),
					zap.Error(err))
				return nil, errUnauthenticated("session invalid")
			}
			resp, rerr := next(withSession(ctx, sess), req)
			if rerr == nil && setCookie != nil && resp != nil {
				resp.Header().Add("Set-Cookie", setCookie.String())
			}
			return resp, rerr
		}
	}
}

// roleInterceptor enforces the requiredRole table. Unknown methods
// default-deny.
func roleInterceptor(logger *zap.Logger) connect.UnaryInterceptorFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			proc := procedureMethod(req.Spec().Procedure)
			need, known := requiredRole[proc]
			if !known {
				logger.Warn("portal: unknown procedure default-deny", zap.String("procedure", proc))
				return nil, errPermissionDenied("unknown procedure")
			}
			if need == RoleAny {
				return next(ctx, req)
			}
			sess, ok := SessionFromContext(ctx)
			if !ok {
				return nil, errUnauthenticated("no session")
			}
			if !satisfies(sess.Roles, need) {
				logger.Info("portal: role denied",
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

// procedureMethod returns the trailing method name from a Connect
// procedure path (e.g. "/limen.portal.v1.PortalService/GetSession" →
// "GetSession"). Matches the keys of requiredRole.
func procedureMethod(procedure string) string {
	procedure = strings.TrimPrefix(procedure, "/")
	return path.Base(procedure)
}
