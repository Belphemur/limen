package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/mcpspec"
	"github.com/belphemur/limen/internal/upstream/protoview"
	"github.com/belphemur/limen/internal/upstream/statichdr"
)

func (s *Service) CreateUpstream(ctx context.Context, req *connect.Request[adminv1.CreateUpstreamRequest]) (*connect.Response[adminv1.CreateUpstreamResponse], error) {
	tenant := tenancy.MustTenant(ctx)
	msg := req.Msg
	in := upstream.CreateUpstreamInput{
		Identifier:      msg.GetIdentifier(),
		DisplayName:     msg.GetDisplayName(),
		MCPServerURL:    msg.GetMcpUrl(),
		StrategyType:    upstream.StrategyType(msg.GetStrategyType()),
		StrategySubMode: msg.GetStrategySubMode(),
		StrategyConfig:  msg.GetStrategyConfig(),
	}
	if blob := msg.GetDefaultsJson(); strings.TrimSpace(blob) != "" {
		in.DefaultsJSON = []byte(blob)
	}
	if ov := msg.GetOauthClientOverride(); ov != nil {
		if in.StrategyType != upstream.StrategyMCPSpec {
			return nil, s.invalidArg("oauth_client_override", fmt.Sprintf("only applies to %q strategy", upstream.StrategyMCPSpec))
		}
		cfg := mcpspec.Config{
			ClientID:     strings.TrimSpace(ov.GetClientId()),
			ClientSecret: ov.GetClientSecret(),
		}
		sf, encErr := mcpspec.EncodeConfig(tenant.ID, cfg)
		if encErr != nil {
			return nil, s.internal("encode mcpspec config", encErr)
		}
		in.EncodedStrategyConfig = sf
	}

	if in.StrategyType == upstream.StrategyStaticHeader {
		cfg, parseErr := statichdr.ParseConfig(msg.GetStrategyConfig())
		if parseErr != nil {
			return nil, s.invalidArg("strategy_config", parseErr.Error())
		}
		sf, encErr := statichdr.EncodeConfig(tenant.ID, cfg)
		if encErr != nil {
			return nil, s.internal("encode statichdr config", encErr)
		}
		in.EncodedStrategyConfig = sf
	}

	up, err := s.upstream.CreateUpstream(ctx, tenant, in)
	if err != nil {
		return nil, s.mapCreateError(err)
	}

	// Tenant-mode strategies (`none`, `static_header` tenant mode)
	// have their tool catalog populated inline; per-user strategies
	// (`mcp_spec`, `static_header` user mode) only run Provision
	// here — the catalog is filled in by the first admin/owner to
	// complete the OAuth flow. A failure here means provision could
	// not complete (discovery, DCR, missing static client, …) so we
	// roll back the row.
	if provErr := s.upstream.ProvisionTenantMode(ctx, tenant, up); provErr != nil {
		s.logger.Warn("admin: CreateUpstream rolling back due to ProvisionTenantMode failure",
			zap.String("upstream", up.Identifier),
			zap.String("strategy", string(in.StrategyType)),
			zap.Error(provErr))
		if delErr := s.upstream.DeleteUpstream(ctx, tenant, up.PublicID); delErr != nil {
			s.logger.Error("admin: rollback after failed provision",
				zap.String("upstream", up.Identifier),
				zap.Error(delErr))
		}
		return nil, s.mapProvisionError(in.StrategyType, provErr)
	}

	summary := s.upstream.SummariseForAdmin(ctx, tenant, nil, up)
	return connect.NewResponse(&adminv1.CreateUpstreamResponse{
		Upstream:          protoview.ToSummaryProto(summary),
		RequiresAdminLink: in.StrategyType == upstream.StrategyMCPSpec,
		ConnectUrl:        "",
	}), nil
}

func (s *Service) UpdateUpstream(ctx context.Context, req *connect.Request[adminv1.UpdateUpstreamRequest]) (*connect.Response[adminv1.UpdateUpstreamResponse], error) {
	tenant := tenancy.MustTenant(ctx)
	msg := req.Msg
	patch := upstream.UpdateUpstreamPatch{}
	if dn := msg.GetDisplayName(); dn != "" {
		v := dn
		patch.DisplayName = &v
	}
	if blob := msg.GetDefaultsJson(); blob != "" {
		patch.DefaultsJSON = []byte(blob)
	}

	up, err := s.upstream.UpdateUpstream(ctx, tenant, msg.GetPublicId(), patch)
	if err != nil {
		return nil, s.mapMutationError(err)
	}

	if cfgPatch := msg.GetStrategyConfig(); len(cfgPatch) > 0 {
		if err := s.applyStrategyConfigPatch(ctx, tenant, up, cfgPatch); err != nil {
			return nil, err
		}
		// Re-fetch the upstream tool list now that credentials have
		// rotated. Best-effort: a failure here (e.g. upstream rejects
		// the new secret) MUST NOT roll back the secret write — the
		// admin needs the new value persisted so they can retry. Log
		// and surface the failure via the next refresher sweep.
		sess := session.MustUser(ctx)
		callingUser, _ := s.upstream.LoadUserBySubject(ctx, tenant.ID, sess.Subject)
		if _, err := s.upstream.ReindexCatalog(ctx, tenant, callingUser, msg.GetPublicId()); err != nil {
			s.logger.Warn("admin: reindex after strategy_config patch failed",
				zap.String("upstream_public_id", msg.GetPublicId()),
				zap.Error(err))
		}
	}

	summary := s.upstream.SummariseForAdmin(ctx, tenant, nil, up)
	return connect.NewResponse(&adminv1.UpdateUpstreamResponse{
		Upstream: protoview.ToSummaryProto(summary),
	}), nil
}

// applyStrategyConfigPatch merges a wire patch into the upstream's
// existing UpstreamStrategyConfig row. Only `static_header` carries
// a config row in v1; other strategies reject the patch with
// InvalidArgument to surface a programming mistake.
func (s *Service) applyStrategyConfigPatch(ctx context.Context, tenant *storage.Tenant, up *storage.Upstream, patch map[string]string) error {
	if upstream.StrategyType(up.StrategyType) != upstream.StrategyStaticHeader {
		return s.invalidArg("strategy_config", "only static_header upstreams accept a strategy_config patch")
	}
	encoded, err := s.upstream.LoadStrategyConfig(ctx, tenant, up)
	if err != nil {
		return s.mapMutationError(err)
	}
	cur, err := statichdr.DecodeConfig(tenant.ID, encoded)
	if err != nil {
		return s.internal("decode statichdr config", err)
	}
	next, err := statichdr.ApplyConfigPatch(cur, patch)
	if err != nil {
		return s.invalidArg("strategy_config", err.Error())
	}
	sf, err := statichdr.EncodeConfig(tenant.ID, next)
	if err != nil {
		return s.internal("encode statichdr config", err)
	}
	if err := s.upstream.ReplaceStrategyConfig(ctx, tenant, up, sf); err != nil {
		return s.mapMutationError(err)
	}
	return nil
}

func (s *Service) DeleteUpstream(ctx context.Context, req *connect.Request[adminv1.DeleteUpstreamRequest]) (*connect.Response[adminv1.DeleteUpstreamResponse], error) {
	tenant := tenancy.MustTenant(ctx)
	if err := s.upstream.DeleteUpstream(ctx, tenant, req.Msg.GetPublicId()); err != nil {
		return nil, s.mapMutationError(err)
	}
	return connect.NewResponse(&adminv1.DeleteUpstreamResponse{}), nil
}

func (s *Service) ReindexUpstreamCatalog(ctx context.Context, req *connect.Request[adminv1.ReindexUpstreamCatalogRequest]) (*connect.Response[adminv1.ReindexUpstreamCatalogResponse], error) {
	tenant := tenancy.MustTenant(ctx)
	sess := session.MustUser(ctx)
	callingUser, err := s.upstream.LoadUserBySubject(ctx, tenant.ID, sess.Subject)
	if err != nil {
		s.logger.Info("admin: caller not mirrored", zap.String("subject", sess.Subject), zap.Error(err))
		callingUser = nil
	}
	up, err := s.upstream.ReindexCatalog(ctx, tenant, callingUser, req.Msg.GetPublicId())
	if err != nil {
		if errors.Is(err, upstream.ErrCannotReindexWithoutLink) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("admin: connect your account before reindexing"))
		}
		return nil, s.mapMutationError(err)
	}
	summary := s.upstream.SummariseForAdmin(ctx, tenant, callingUser, up)
	return connect.NewResponse(&adminv1.ReindexUpstreamCatalogResponse{
		Upstream: protoview.ToSummaryProto(summary),
	}), nil
}

func (s *Service) PreviewUpstreamContext(ctx context.Context, req *connect.Request[adminv1.PreviewUpstreamContextRequest]) (*connect.Response[adminv1.PreviewUpstreamContextResponse], error) {
	tenant := tenancy.MustTenant(ctx)
	merged, err := s.upstream.PreviewContext(ctx, tenant, req.Msg.GetPublicId(), req.Msg.GetUserId())
	if err != nil {
		switch {
		case errors.Is(err, upstream.ErrUpstreamNotFound):
			return nil, connect.NewError(connect.CodeNotFound, errors.New("admin: upstream not found"))
		case errors.Is(err, upstream.ErrUserNotFound):
			return nil, connect.NewError(connect.CodeNotFound, errors.New("admin: user not found"))
		default:
			return nil, s.internal("preview context", err)
		}
	}
	return connect.NewResponse(&adminv1.PreviewUpstreamContextResponse{
		MergedJson: string(merged),
	}), nil
}

func (s *Service) mapCreateError(err error) error {
	if errors.Is(err, upstream.ErrUpstreamAlreadyExists) {
		return connect.NewError(connect.CodeAlreadyExists, errors.New("admin: upstream name already exists"))
	}
	if msg := err.Error(); strings.HasPrefix(msg, "context:") {
		return s.invalidArg("defaults_json", msg)
	}
	return s.internal("create upstream", err)
}

// mapProvisionError translates a strategy Provision failure into a
// FailedPrecondition Connect error with a structpb detail of
// `{stage, strategy, reason}`. The SPA reads the stage to render a
// targeted modal (e.g. "Authorization server unreachable" vs
// "Dynamic client registration was rejected").
func (s *Service) mapProvisionError(strategyType upstream.StrategyType, err error) error {
	stage := "provision"
	switch {
	case errors.Is(err, mcpspec.ErrDiscoveryFailed):
		stage = "discovery"
	case errors.Is(err, mcpspec.ErrDCRFailed):
		stage = "dcr"
	case errors.Is(err, mcpspec.ErrStaticClientRequired):
		stage = "static_client_required"
	case errors.Is(err, mcpspec.ErrPersistFailed):
		stage = "persist"
	}
	cerr := connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("admin: provision %s: %w", stage, err))
	st, sErr := structpb.NewStruct(map[string]any{
		"stage":    stage,
		"strategy": string(strategyType),
		"reason":   err.Error(),
	})
	if sErr == nil {
		if d, dErr := connect.NewErrorDetail(st); dErr == nil {
			cerr.AddDetail(d)
		}
	}
	return cerr
}

func (s *Service) mapMutationError(err error) error {
	switch {
	case errors.Is(err, upstream.ErrUpstreamNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("admin: upstream not found"))
	case errors.Is(err, upstream.ErrUnsupported):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("admin: operation not supported by strategy"))
	}
	if msg := err.Error(); strings.HasPrefix(msg, "context:") {
		return s.invalidArg("defaults_json", msg)
	}
	return s.internal("upstream mutation", err)
}

// invalidArg returns CodeInvalidArgument with a google.protobuf.Struct
// detail of {"path": <field>, "reason": <message>} so the SPA can pin
// the validation banner to the exact form field.
func (s *Service) invalidArg(path, reason string) error {
	cerr := connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("admin: %s: %s", path, reason))
	st, err := structpb.NewStruct(map[string]any{
		"path":   path,
		"reason": reason,
	})
	if err == nil {
		if d, dErr := connect.NewErrorDetail(st); dErr == nil {
			cerr.AddDetail(d)
		}
	}
	return cerr
}

func (s *Service) internal(op string, err error) error {
	s.logger.Warn("admin: op failed", zap.String("op", op), zap.Error(err))
	return connect.NewError(connect.CodeInternal, errors.New("admin: internal error"))
}
