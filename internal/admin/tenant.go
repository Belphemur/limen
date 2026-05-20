package admin

import (
	"context"

	"connectrpc.com/connect"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
)

func (s *Service) DeleteTenant(_ context.Context, _ *connect.Request[adminv1.DeleteTenantRequest]) (*connect.Response[adminv1.DeleteTenantResponse], error) {
	return nil, errUnimplemented("slice-3")
}
