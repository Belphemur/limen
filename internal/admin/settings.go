package admin

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"gorm.io/gorm"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/tenant"
)

func (s *Service) GetTenantSettings(ctx context.Context, _ *connect.Request[adminv1.GetTenantSettingsRequest]) (*connect.Response[adminv1.GetTenantSettingsResponse], error) {
	t := tenancy.MustTenant(ctx)
	settings, _, err := s.tenant.LoadSettings(ctx, t)
	if err != nil {
		return nil, s.internal("load tenant settings", err)
	}
	// Best-effort: a Zitadel hiccup or a tenant whose project grant
	// has been removed must not block reading settings — the SPA
	// disables the role-assignment card when the grant id is empty.
	var grantID string
	if s.projectGrants != nil && s.projectID != "" && t.ZitadelOrgID != "" {
		gid, err := s.projectGrants.FindProjectGrantID(ctx, s.projectID, t.ZitadelOrgID)
		if err != nil {
			s.logger.Warn("find project grant id",
				zap.String("project_id", s.projectID),
				zap.String("org_id", t.ZitadelOrgID),
				zap.Error(err))
		} else {
			grantID = gid
		}
	}
	return connect.NewResponse(&adminv1.GetTenantSettingsResponse{
		Settings:              toTenantSettingsProto(t, settings),
		ZitadelOrgId:          t.ZitadelOrgID,
		ZitadelProjectId:      s.projectID,
		ZitadelProjectGrantId: grantID,
	}), nil
}

func (s *Service) UpdateTenantSettings(ctx context.Context, req *connect.Request[adminv1.UpdateTenantSettingsRequest]) (*connect.Response[adminv1.UpdateTenantSettingsResponse], error) {
	t := tenancy.MustTenant(ctx)
	msg := req.Msg
	in := tenant.UpdateSettingsInput{
		SetInvitedTeamAt: msg.GetInvitedTeamAtNow(),
		SetConfiguredAt:  msg.GetConfiguredAtNow(),
	}
	if name := msg.GetName(); name != "" {
		v := name
		in.Name = &v
	}

	settings, err := s.tenant.UpdateSettings(ctx, t, in)
	if err != nil {
		return nil, s.mapSettingsErr(err)
	}
	return connect.NewResponse(&adminv1.UpdateTenantSettingsResponse{
		Settings: toTenantSettingsProto(t, settings),
	}), nil
}

func (s *Service) MarkIDEChoiceSkipped(ctx context.Context, _ *connect.Request[adminv1.MarkIDEChoiceSkippedRequest]) (*connect.Response[adminv1.MarkIDEChoiceSkippedResponse], error) {
	t := tenancy.MustTenant(ctx)
	settings, err := s.tenant.UpdateSettings(ctx, t, tenant.UpdateSettingsInput{SetChoseIDEAt: true})
	if err != nil {
		return nil, s.internal("mark ide choice skipped", err)
	}
	return connect.NewResponse(&adminv1.MarkIDEChoiceSkippedResponse{
		Settings: toTenantSettingsProto(t, settings),
	}), nil
}

func (s *Service) mapSettingsErr(err error) error {
	if errors.Is(err, gorm.ErrInvalidValue) {
		return s.invalidArg("name", "name must not be empty")
	}
	return s.internal("update tenant settings", err)
}

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
	if s != nil && s.ChoseIDEAt != nil {
		out.ChoseIdeAt = s.ChoseIDEAt.UTC().Format(timeFormatRFC3339)
	}
	return out
}

const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"
