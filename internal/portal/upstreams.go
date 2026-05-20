package portal

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/portal/portalv1"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
)

// ListUpstreams returns every tenant upstream paired with the caller's
// link state. The role interceptor has already enforced RoleMember;
// here we resolve the OIDC subject → storage.User and delegate to the
// upstream service.
func (s *Service) ListUpstreams(ctx context.Context, _ *connect.Request[portalv1.ListUpstreamsRequest]) (*connect.Response[portalv1.ListUpstreamsResponse], error) {
	tenant, user, err := s.callerContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.upstream.ListUpstreamsForUser(ctx, tenant, user)
	if err != nil {
		s.logger.Warn("portal: list upstreams failed",
			zap.String("tenant", tenant.PublicID), zap.Error(err))
		return nil, errInternal(err)
	}
	out := make([]*portalv1.UpstreamSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, toUpstreamSummaryProto(r))
	}
	return connect.NewResponse(&portalv1.ListUpstreamsResponse{Upstreams: out}), nil
}

// StartConnect drives the chosen upstream's StartLink. The returned
// redirect_url is either an external authorize URL (mcp_spec) or a
// relative SPA path (static_header user mode).
func (s *Service) StartConnect(ctx context.Context, req *connect.Request[portalv1.StartConnectRequest]) (*connect.Response[portalv1.StartConnectResponse], error) {
	name := strings.TrimSpace(req.Msg.UpstreamName)
	if name == "" {
		return nil, errInvalidArgument("upstream_name required")
	}
	tenant, user, err := s.callerContext(ctx)
	if err != nil {
		return nil, err
	}
	returnTo := strings.TrimSpace(req.Msg.ReturnTo)
	if returnTo == "" {
		returnTo = "/t/" + tenant.PublicID + "/"
	}
	redirect, err := s.upstream.StartConnect(ctx, tenant, user, name, returnTo)
	if err != nil {
		return nil, mapUpstreamError(err, "start connect", name, s.logger)
	}
	return connect.NewResponse(&portalv1.StartConnectResponse{RedirectUrl: redirect}), nil
}

// SubmitUpstreamAPIKey persists a user-mode static_header secret. The
// key is NEVER logged — only its length, so an operator debugging from
// logs cannot leak a customer credential. Errors from this path are
// also generic to avoid surfacing whether a key looked valid.
func (s *Service) SubmitUpstreamAPIKey(ctx context.Context, req *connect.Request[portalv1.SubmitUpstreamAPIKeyRequest]) (*connect.Response[portalv1.SubmitUpstreamAPIKeyResponse], error) {
	name := strings.TrimSpace(req.Msg.UpstreamName)
	if name == "" {
		return nil, errInvalidArgument("upstream_name required")
	}
	key := req.Msg.ApiKey
	if strings.TrimSpace(key) == "" {
		return nil, errInvalidArgument("api_key required")
	}
	tenant, user, err := s.callerContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.upstream.PersistUserStaticHeaderSecret(ctx, tenant, user, name, key); err != nil {
		s.logger.Info("portal: persist api key failed",
			zap.String("tenant", tenant.PublicID),
			zap.String("upstream", name),
			zap.Int("api_key_len", len(key)),
			zap.Error(err))
		return nil, mapUpstreamError(err, "submit api key", name, s.logger)
	}
	return connect.NewResponse(&portalv1.SubmitUpstreamAPIKeyResponse{}), nil
}

// SetUpstreamLinkEnabled flips the per-user link toggle. Re-enabling
// an auto_disabled link transparently clears the failure counters
// inside upstream.Service.SetLinkEnabled.
func (s *Service) SetUpstreamLinkEnabled(ctx context.Context, req *connect.Request[portalv1.SetUpstreamLinkEnabledRequest]) (*connect.Response[portalv1.SetUpstreamLinkEnabledResponse], error) {
	name := strings.TrimSpace(req.Msg.UpstreamName)
	if name == "" {
		return nil, errInvalidArgument("upstream_name required")
	}
	tenant, user, err := s.callerContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.upstream.SetLinkEnabled(ctx, tenant, user, name, req.Msg.Enabled); err != nil {
		return nil, mapUpstreamError(err, "set link enabled", name, s.logger)
	}
	return connect.NewResponse(&portalv1.SetUpstreamLinkEnabledResponse{}), nil
}

// Disconnect soft-deletes the (user, upstream) link.
func (s *Service) Disconnect(ctx context.Context, req *connect.Request[portalv1.DisconnectRequest]) (*connect.Response[portalv1.DisconnectResponse], error) {
	name := strings.TrimSpace(req.Msg.UpstreamName)
	if name == "" {
		return nil, errInvalidArgument("upstream_name required")
	}
	tenant, user, err := s.callerContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.upstream.Disconnect(ctx, tenant, user, name); err != nil {
		return nil, mapUpstreamError(err, "disconnect", name, s.logger)
	}
	return connect.NewResponse(&portalv1.DisconnectResponse{}), nil
}

// callerContext resolves the tenant + local user row for the
// authenticated session. Returns CodeUnauthenticated if no session is
// pinned (which shouldn't happen for these RPCs after the interceptor
// stack, but is a defensive check) and CodeNotFound if the user row
// hasn't been mirrored yet (OIDC callback runs the upsert on first
// login, so this is essentially a race window).
func (s *Service) callerContext(ctx context.Context) (*storage.Tenant, *storage.User, error) {
	tenant := tenancy.MustTenant(ctx)
	sess, ok := session.UserFromContext(ctx)
	if !ok {
		return nil, nil, errUnauthenticated("no session")
	}
	user, err := s.upstream.LoadUserBySubject(ctx, tenant.ID, sess.Subject)
	if err != nil {
		s.logger.Info("portal: user lookup failed",
			zap.String("tenant", tenant.PublicID),
			zap.String("subject", sess.Subject),
			zap.Error(err))
		return nil, nil, errNotFound("user not mirrored — try logging out and back in")
	}
	return tenant, user, nil
}

// mapUpstreamError maps the upstream package's sentinel errors onto
// Connect status codes. Anything we don't recognise is wrapped as
// CodeInternal with the cause logged.
func mapUpstreamError(err error, op, upstreamName string, logger *zap.Logger) error {
	switch {
	case errors.Is(err, upstream.ErrUpstreamNotFound):
		return errNotFound("upstream not found")
	case errors.Is(err, upstream.ErrLinkNotFound):
		return errNotFound("link not found")
	case errors.Is(err, upstream.ErrUnsupported):
		return errInvalidArgument("operation not supported by this upstream strategy")
	case errors.Is(err, upstream.ErrNeedsRelink):
		return errInvalidArgument("link needs re-link")
	default:
		logger.Warn("portal: upstream op failed",
			zap.String("op", op),
			zap.String("upstream", upstreamName),
			zap.Error(err))
		return errInternal(err)
	}
}

func toUpstreamSummaryProto(r upstream.UserUpstreamSummary) *portalv1.UpstreamSummary {
	up := r.Upstream
	out := &portalv1.UpstreamSummary{
		PublicId:        up.PublicID,
		Name:            up.Name,
		DisplayName:     up.Name,
		McpUrl:          up.McpServerURL,
		StrategyType:    up.StrategyType,
		StrategySubMode: r.StrategySubMode,
		RequiresLink:    r.RequiresLink,
		LinkState:       linkStateProto(r.LinkState),
		LastErrorReason: r.LastErrorReason,
		Aliases:         r.Aliases,
		Tools:           toToolProtos(r.Tools),
	}
	if r.Link != nil && r.Link.LastFailureAt != nil {
		out.LastErrorAt = r.Link.LastFailureAt.UTC().Format(time.RFC3339)
	}
	return out
}

func toToolProtos(rows []storage.UpstreamTool) []*portalv1.UpstreamTool {
	if len(rows) == 0 {
		return nil
	}
	out := make([]*portalv1.UpstreamTool, 0, len(rows))
	for i := range rows {
		out = append(out, &portalv1.UpstreamTool{
			Name:        rows[i].Name,
			Description: rows[i].Description,
		})
	}
	return out
}

func linkStateProto(s upstream.LinkState) portalv1.LinkState {
	switch s {
	case upstream.LinkStateNone:
		return portalv1.LinkState_LINK_STATE_NONE
	case upstream.LinkStateConnected:
		return portalv1.LinkState_LINK_STATE_CONNECTED
	case upstream.LinkStateDisabled:
		return portalv1.LinkState_LINK_STATE_DISABLED
	case upstream.LinkStateAutoDisabled:
		return portalv1.LinkState_LINK_STATE_AUTO_DISABLED
	case upstream.LinkStateNeedsRelink:
		return portalv1.LinkState_LINK_STATE_NEEDS_RELINK
	default:
		return portalv1.LinkState_LINK_STATE_UNSPECIFIED
	}
}
