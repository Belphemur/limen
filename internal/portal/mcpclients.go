package portal

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/portal/portalv1"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

// ListMCPClients enumerates the DCR-registered OIDC apps owned by the
// caller's tenant. Both RLS and an explicit tenant_id filter scope the
// query — see internal/storage/zitadel_apps.go for the rationale.
func (s *Service) ListMCPClients(ctx context.Context, _ *connect.Request[portalv1.ListMCPClientsRequest]) (*connect.Response[portalv1.ListMCPClientsResponse], error) {
	tenant := tenancy.MustTenant(ctx)
	rows, err := s.store.ListZitadelAppsByTenant(storage.WithTenant(ctx, tenant.ID), tenant.ID)
	if err != nil {
		s.logger.Warn("portal: list mcp clients failed",
			zap.String("tenant", tenant.PublicID), zap.Error(err))
		return nil, errInternal(err)
	}
	out := make([]*portalv1.MCPClient, 0, len(rows))
	for _, r := range rows {
		out = append(out, &portalv1.MCPClient{
			PublicId:        r.PublicID,
			ClientId:        r.ClientID,
			Name:            r.Name,
			SoftwareId:      r.SoftwareID,
			SoftwareVersion: r.SoftwareVersion,
			CreatedAt:       r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return connect.NewResponse(&portalv1.ListMCPClientsResponse{Clients: out}), nil
}

// RevokeMCPClient deletes the Zitadel OIDC app for an MCP client and
// then soft-deletes the local mirror row. Order matters: Zitadel first
// so a transient Limen DB failure can't leave an orphaned issuer that
// keeps minting tokens for a revoked client. If the Zitadel call
// reports the app as already gone (NotFound), we still soft-delete the
// mirror — the operation is idempotent end-to-end.
func (s *Service) RevokeMCPClient(ctx context.Context, req *connect.Request[portalv1.RevokeMCPClientRequest]) (*connect.Response[portalv1.RevokeMCPClientResponse], error) {
	if s.apps == nil {
		return nil, errInternal(errors.New("portal: app manager not wired"))
	}
	publicID := strings.TrimSpace(req.Msg.PublicId)
	if publicID == "" {
		return nil, errInvalidArgument("public_id required")
	}
	tenant := tenancy.MustTenant(ctx)
	ctxT := storage.WithTenant(ctx, tenant.ID)

	row, err := s.store.LoadZitadelAppByPublicID(ctxT, tenant.ID, publicID)
	if err != nil {
		if errors.Is(err, storage.ErrZitadelAppNotFound) {
			return nil, errNotFound("mcp client not found")
		}
		s.logger.Warn("portal: load mcp client failed",
			zap.String("tenant", tenant.PublicID),
			zap.String("public_id", publicID),
			zap.Error(err))
		return nil, errInternal(err)
	}

	if err := s.apps.DeleteOIDCApp(ctx, tenant.ZitadelOrgID, row.ZitadelProjectID, row.ZitadelAppID); err != nil {
		if !isZitadelNotFound(err) {
			s.logger.Warn("portal: zitadel delete app failed",
				zap.String("tenant", tenant.PublicID),
				zap.String("public_id", publicID),
				zap.String("zitadel_app_id", row.ZitadelAppID),
				zap.Error(err))
			return nil, errInternal(err)
		}
		s.logger.Info("portal: zitadel app already gone — proceeding with mirror cleanup",
			zap.String("zitadel_app_id", row.ZitadelAppID))
	}

	if err := s.store.SoftDeleteZitadelApp(ctxT, tenant.ID, publicID); err != nil {
		if errors.Is(err, storage.ErrZitadelAppNotFound) {
			// Concurrent revoke — fine.
			return connect.NewResponse(&portalv1.RevokeMCPClientResponse{}), nil
		}
		s.logger.Error("portal: soft-delete mcp client mirror failed",
			zap.String("tenant", tenant.PublicID),
			zap.String("public_id", publicID),
			zap.Error(err))
		return nil, errInternal(err)
	}
	return connect.NewResponse(&portalv1.RevokeMCPClientResponse{}), nil
}

// isZitadelNotFound matches the substring Zitadel's error wrapper
// produces for already-deleted resources. We don't have a typed sentinel
// from internal/zitadel for this case, so a string match keeps the
// dependency edge zero-weight.
func isZitadelNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "notfound")
}
