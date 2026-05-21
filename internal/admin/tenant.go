package admin

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/tenant"
)

func (s *Service) DeleteTenant(ctx context.Context, req *connect.Request[adminv1.DeleteTenantRequest]) (*connect.Response[adminv1.DeleteTenantResponse], error) {
	t := tenancy.MustTenant(ctx)
	if err := s.tenant.Delete(ctx, t, req.Msg.GetPublicIdConfirmation()); err != nil {
		if errors.Is(err, tenant.ErrConfirmationMismatch) {
			return nil, s.invalidArg("public_id_confirmation", "does not match the tenant public_id")
		}
		return nil, s.internal("delete tenant", err)
	}
	return connect.NewResponse(&adminv1.DeleteTenantResponse{}), nil
}
