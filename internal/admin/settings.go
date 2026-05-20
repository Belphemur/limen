package admin

import (
	"context"

	"connectrpc.com/connect"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
)

func (s *Service) UpdateTenantSettings(_ context.Context, _ *connect.Request[adminv1.UpdateTenantSettingsRequest]) (*connect.Response[adminv1.UpdateTenantSettingsResponse], error) {
	return nil, errUnimplemented("slice-3")
}
