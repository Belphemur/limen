package admin

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
)

// CreateServiceAccount creates a Zitadel machine user and stores a
// local mirror row. Returns the service account and a one-time PAT.
func (s *Service) CreateServiceAccount(ctx context.Context, req *connect.Request[adminv1.CreateServiceAccountRequest]) (*connect.Response[adminv1.CreateServiceAccountResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: CreateServiceAccount is not implemented"))
}

// ListServiceAccounts returns every service account for the tenant.
func (s *Service) ListServiceAccounts(ctx context.Context, req *connect.Request[adminv1.ListServiceAccountsRequest]) (*connect.Response[adminv1.ListServiceAccountsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: ListServiceAccounts is not implemented"))
}

// DeleteServiceAccount removes the Zitadel machine user and the local
// mirror row. All tokens are revoked.
func (s *Service) DeleteServiceAccount(ctx context.Context, req *connect.Request[adminv1.DeleteServiceAccountRequest]) (*connect.Response[adminv1.DeleteServiceAccountResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: DeleteServiceAccount is not implemented"))
}

// RegenerateServiceAccountToken revokes existing tokens and issues a
// new one-time PAT for the service account.
func (s *Service) RegenerateServiceAccountToken(ctx context.Context, req *connect.Request[adminv1.RegenerateServiceAccountTokenRequest]) (*connect.Response[adminv1.RegenerateServiceAccountTokenResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: RegenerateServiceAccountToken is not implemented"))
}

// ImpersonateServiceAccount starts an impersonation session for the
// caller, scoped to the requested service account. The response carries
// a Set-Cookie header with the impersonation session cookie.
func (s *Service) ImpersonateServiceAccount(ctx context.Context, req *connect.Request[adminv1.ImpersonateServiceAccountRequest]) (*connect.Response[adminv1.ImpersonateServiceAccountResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: ImpersonateServiceAccount is not implemented"))
}

// ExitImpersonation ends the current impersonation session. The
// response carries a Set-Cookie header clearing the impersonation cookie.
func (s *Service) ExitImpersonation(ctx context.Context, req *connect.Request[adminv1.ExitImpersonationRequest]) (*connect.Response[adminv1.ExitImpersonationResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: ExitImpersonation is not implemented"))
}
