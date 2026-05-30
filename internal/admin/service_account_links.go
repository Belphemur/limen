package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
)

// ListServiceAccountUpstreamLinks returns all upstream links for a service account.
func (s *Service) ListServiceAccountUpstreamLinks(ctx context.Context, req *connect.Request[adminv1.ListServiceAccountUpstreamLinksRequest]) (*connect.Response[adminv1.ListServiceAccountUpstreamLinksResponse], error) {
	saPublicID := strings.TrimSpace(req.Msg.GetServiceAccountPublicId())
	if saPublicID == "" {
		return nil, s.invalidArg("service_account_public_id", "required")
	}

	t := tenancy.MustTenant(ctx)

	sa, err := s.store.GetServiceAccount(ctx, t.ID, saPublicID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("service account %q not found", saPublicID))
		}
		return nil, s.internal("get service account", err)
	}

	links, err := s.store.ListUpstreamLinksByServiceAccount(ctx, t.ID, sa.ID)
	if err != nil {
		return nil, s.internal("list upstream links", err)
	}

	var pbLinks []*adminv1.ServiceAccountUpstreamLink
	for _, link := range links {
		pbLinks = append(pbLinks, &adminv1.ServiceAccountUpstreamLink{
			UpstreamPublicId: link.Upstream.PublicID,
			UpstreamName:     link.Upstream.DisplayName,
			UpstreamUrl:      link.Upstream.McpServerURL,
			Enabled:          link.Enabled,
			ConnectedAt:      link.CreatedAt.Format(time.RFC3339),
		})
	}

	return connect.NewResponse(&adminv1.ListServiceAccountUpstreamLinksResponse{
		Links: pbLinks,
	}), nil
}

// SetServiceAccountLinkEnabled toggles the enabled flag on a service account's upstream link.
func (s *Service) SetServiceAccountLinkEnabled(ctx context.Context, req *connect.Request[adminv1.SetServiceAccountLinkEnabledRequest]) (*connect.Response[adminv1.SetServiceAccountLinkEnabledResponse], error) {
	saPublicID := strings.TrimSpace(req.Msg.GetServiceAccountPublicId())
	if saPublicID == "" {
		return nil, s.invalidArg("service_account_public_id", "required")
	}
	upstreamPublicID := strings.TrimSpace(req.Msg.GetUpstreamPublicId())
	if upstreamPublicID == "" {
		return nil, s.invalidArg("upstream_public_id", "required")
	}

	t := tenancy.MustTenant(ctx)

	sa, err := s.store.GetServiceAccount(ctx, t.ID, saPublicID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("service account %q not found", saPublicID))
		}
		return nil, s.internal("get service account", err)
	}

	up, err := s.upstream.LookupUpstream(ctx, t, upstreamPublicID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("upstream %q not found", upstreamPublicID))
		}
		return nil, s.internal("lookup upstream", err)
	}

	link, err := s.store.GetUpstreamLinkByServiceAccountAndUpstream(ctx, t.ID, sa.ID, up.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("link not found for service account %q and upstream %q", saPublicID, upstreamPublicID))
		}
		return nil, s.internal("get upstream link", err)
	}

	link.Enabled = req.Msg.GetEnabled()
	if err := s.store.UpdateUpstreamLink(ctx, link); err != nil {
		return nil, s.internal("update upstream link", err)
	}

	return connect.NewResponse(&adminv1.SetServiceAccountLinkEnabledResponse{}), nil
}

// StartServiceAccountConnect initiates the OAuth flow for a service account
// upstream link. Returns the authorize URL the SPA should redirect to.
func (s *Service) StartServiceAccountConnect(ctx context.Context, req *connect.Request[adminv1.StartServiceAccountConnectRequest]) (*connect.Response[adminv1.StartServiceAccountConnectResponse], error) {
	saPublicID := strings.TrimSpace(req.Msg.GetServiceAccountPublicId())
	if saPublicID == "" {
		return nil, s.invalidArg("service_account_public_id", "required")
	}
	upstreamPublicID := strings.TrimSpace(req.Msg.GetUpstreamPublicId())
	if upstreamPublicID == "" {
		return nil, s.invalidArg("upstream_public_id", "required")
	}

	t := tenancy.MustTenant(ctx)
	sess := session.MustUser(ctx)

	sa, err := s.store.GetServiceAccount(ctx, t.ID, saPublicID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("service account %q not found", saPublicID))
		}
		return nil, s.internal("get service account", err)
	}

	// Resolve admin user for state AAD
	adminUser, err := s.upstream.LoadUserBySubject(ctx, t.ID, sess.Subject)
	if err != nil {
		return nil, s.internal("load admin user", err)
	}

	redirectURL, err := s.upstream.StartConnectForServiceAccount(ctx, t, adminUser, sa.ID, upstreamPublicID, req.Msg.GetReturnTo())
	if err != nil {
		return nil, s.internal("start service account connect", err)
	}

	return connect.NewResponse(&adminv1.StartServiceAccountConnectResponse{
		RedirectUrl: redirectURL,
	}), nil
}

// SubmitServiceAccountAPIKey stores an API key override for a static_header
// upstream on behalf of a service account.
func (s *Service) SubmitServiceAccountAPIKey(ctx context.Context, req *connect.Request[adminv1.SubmitServiceAccountAPIKeyRequest]) (*connect.Response[adminv1.SubmitServiceAccountAPIKeyResponse], error) {
	saPublicID := strings.TrimSpace(req.Msg.GetServiceAccountPublicId())
	if saPublicID == "" {
		return nil, s.invalidArg("service_account_public_id", "required")
	}
	upstreamPublicID := strings.TrimSpace(req.Msg.GetUpstreamPublicId())
	if upstreamPublicID == "" {
		return nil, s.invalidArg("upstream_public_id", "required")
	}
	apiKey := req.Msg.GetApiKey()
	if strings.TrimSpace(apiKey) == "" {
		return nil, s.invalidArg("api_key", "required")
	}

	t := tenancy.MustTenant(ctx)
	sess := session.MustUser(ctx)

	sa, err := s.store.GetServiceAccount(ctx, t.ID, saPublicID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("service account %q not found", saPublicID))
		}
		return nil, s.internal("get service account", err)
	}

	adminUser, err := s.upstream.LoadUserBySubject(ctx, t.ID, sess.Subject)
	if err != nil {
		return nil, s.internal("load admin user", err)
	}

	if err := s.upstream.PersistServiceAccountStaticHeaderSecret(ctx, t, adminUser, sa.ID, upstreamPublicID, apiKey); err != nil {
		return nil, s.internal("submit service account api key", err)
	}

	return connect.NewResponse(&adminv1.SubmitServiceAccountAPIKeyResponse{}), nil
}

// ClearServiceAccountOverride drops the per-SA API key override on a
// static_header upstream, falling back to the shared secret.
func (s *Service) ClearServiceAccountOverride(ctx context.Context, req *connect.Request[adminv1.ClearServiceAccountOverrideRequest]) (*connect.Response[adminv1.ClearServiceAccountOverrideResponse], error) {
	saPublicID := strings.TrimSpace(req.Msg.GetServiceAccountPublicId())
	if saPublicID == "" {
		return nil, s.invalidArg("service_account_public_id", "required")
	}
	upstreamPublicID := strings.TrimSpace(req.Msg.GetUpstreamPublicId())
	if upstreamPublicID == "" {
		return nil, s.invalidArg("upstream_public_id", "required")
	}

	t := tenancy.MustTenant(ctx)
	sess := session.MustUser(ctx)

	sa, err := s.store.GetServiceAccount(ctx, t.ID, saPublicID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("service account %q not found", saPublicID))
		}
		return nil, s.internal("get service account", err)
	}

	adminUser, err := s.upstream.LoadUserBySubject(ctx, t.ID, sess.Subject)
	if err != nil {
		return nil, s.internal("load admin user", err)
	}

	if err := s.upstream.ClearServiceAccountStaticHeaderOverride(ctx, t, adminUser, sa.ID, upstreamPublicID); err != nil {
		return nil, s.internal("clear service account override", err)
	}

	return connect.NewResponse(&adminv1.ClearServiceAccountOverrideResponse{}), nil
}

// DisconnectServiceAccountUpstream soft-deletes the SA's upstream link.
func (s *Service) DisconnectServiceAccountUpstream(ctx context.Context, req *connect.Request[adminv1.DisconnectServiceAccountUpstreamRequest]) (*connect.Response[adminv1.DisconnectServiceAccountUpstreamResponse], error) {
	saPublicID := strings.TrimSpace(req.Msg.GetServiceAccountPublicId())
	if saPublicID == "" {
		return nil, s.invalidArg("service_account_public_id", "required")
	}
	upstreamPublicID := strings.TrimSpace(req.Msg.GetUpstreamPublicId())
	if upstreamPublicID == "" {
		return nil, s.invalidArg("upstream_public_id", "required")
	}

	t := tenancy.MustTenant(ctx)

	sa, err := s.store.GetServiceAccount(ctx, t.ID, saPublicID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("service account %q not found", saPublicID))
		}
		return nil, s.internal("get service account", err)
	}

	saID := sa.ID
	lctx := upstream.LinkContext{ServiceAccountID: &saID}
	if err := s.upstream.DisconnectByOwner(ctx, t, lctx, upstreamPublicID); err != nil {
		return nil, s.internal("disconnect service account upstream", err)
	}

	return connect.NewResponse(&adminv1.DisconnectServiceAccountUpstreamResponse{}), nil
}
