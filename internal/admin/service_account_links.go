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
	"github.com/belphemur/limen/internal/tenancy"
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
