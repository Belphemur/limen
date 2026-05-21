package admin

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/tenant"
)

func (s *Service) GetTenantSettings(ctx context.Context, _ *connect.Request[adminv1.GetTenantSettingsRequest]) (*connect.Response[adminv1.GetTenantSettingsResponse], error) {
	t := tenancy.MustTenant(ctx)
	settings, allowlist, _, err := s.tenant.LoadSettings(ctx, t)
	if err != nil {
		return nil, s.internal("load tenant settings", err)
	}
	return connect.NewResponse(&adminv1.GetTenantSettingsResponse{
		Settings:                toTenantSettingsProto(t, settings),
		DcrRedirectUriAllowlist: allowlist,
		ZitadelOrgId:            t.ZitadelOrgID,
	}), nil
}

func (s *Service) UpdateTenantSettings(ctx context.Context, req *connect.Request[adminv1.UpdateTenantSettingsRequest]) (*connect.Response[adminv1.UpdateTenantSettingsResponse], error) {
	t := tenancy.MustTenant(ctx)
	msg := req.Msg
	in := tenant.UpdateSettingsInput{
		SetInvitedTeamAt: msg.GetInvitedTeamAtNow(),
		SetConfiguredAt:  msg.GetConfiguredAtNow(),
		AllowlistSet:     msg.GetDcrRedirectUriAllowlistSet(),
	}
	if name := msg.GetName(); name != "" {
		v := name
		in.Name = &v
	}
	if in.AllowlistSet {
		in.DCRRedirectURIAllowlist = append([]string(nil), msg.GetDcrRedirectUriAllowlist()...)
	}

	settings, allowlist, err := s.tenant.UpdateSettings(ctx, t, in)
	if err != nil {
		return nil, s.mapSettingsErr(err)
	}
	return connect.NewResponse(&adminv1.UpdateTenantSettingsResponse{
		Settings:                toTenantSettingsProto(t, settings),
		DcrRedirectUriAllowlist: allowlist,
	}), nil
}

func (s *Service) mapSettingsErr(err error) error {
	var entryErr *tenant.ErrAllowlistEntryInvalid
	if errors.As(err, &entryErr) {
		return s.invalidArg(fmt.Sprintf("dcr_redirect_uri_allowlist[%d]", entryErr.Index), entryErr.Err.Error())
	}
	if errors.Is(err, gorm.ErrInvalidValue) {
		return s.invalidArg("name", "name must not be empty")
	}
	return s.internal("update tenant settings", err)
}

// toTenantSettingsProto composes the wire-shape from the tenant
// identity (name, public_id come from the Tenant row) and the
// preference timestamps from TenantSettings.
func toTenantSettingsProto(t *storage.Tenant, s *storage.TenantSettings) *adminv1.TenantSettings {
	out := &adminv1.TenantSettings{
		Name:     t.Name,
		PublicId: t.PublicID,
	}
	if s != nil && s.InvitedTeamAt != nil {
		out.InvitedTeamAt = s.InvitedTeamAt.UTC().Format(timeFormatRFC3339)
	}
	if s != nil && s.ConfiguredAt != nil {
		out.ConfiguredAt = s.ConfiguredAt.UTC().Format(timeFormatRFC3339)
	}
	return out
}

const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"
