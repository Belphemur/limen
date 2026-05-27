package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/tenant"
)

func (s *Service) ListAllowlistEntries(ctx context.Context, _ *connect.Request[adminv1.ListAllowlistEntriesRequest]) (*connect.Response[adminv1.ListAllowlistEntriesResponse], error) {
	t := tenancy.MustTenant(ctx)
	entries, err := s.tenant.ListAllowlistEntries(ctx, t)
	if err != nil {
		return nil, s.internal("list allowlist entries", err)
	}
	return connect.NewResponse(&adminv1.ListAllowlistEntriesResponse{Entries: toAllowlistEntryProtos(entries)}), nil
}

func (s *Service) AddAllowlistEntry(ctx context.Context, req *connect.Request[adminv1.AddAllowlistEntryRequest]) (*connect.Response[adminv1.AddAllowlistEntryResponse], error) {
	t := tenancy.MustTenant(ctx)
	msg := req.Msg
	entry, err := s.tenant.AddAllowlistEntry(ctx, t, tenant.AddAllowlistEntryInput{
		IDEKey:  msg.GetIdeKey(),
		Label:   msg.GetLabel(),
		Pattern: msg.GetPattern(),
	})
	if err != nil {
		return nil, s.mapAllowlistErr(err)
	}
	return connect.NewResponse(&adminv1.AddAllowlistEntryResponse{Entry: toAllowlistEntryProto(entry)}), nil
}

func (s *Service) UpdateAllowlistEntry(ctx context.Context, req *connect.Request[adminv1.UpdateAllowlistEntryRequest]) (*connect.Response[adminv1.UpdateAllowlistEntryResponse], error) {
	t := tenancy.MustTenant(ctx)
	msg := req.Msg
	entry, err := s.tenant.UpdateAllowlistEntry(ctx, t, msg.GetPublicId(), tenant.UpdateAllowlistEntryInput{
		Label:   msg.GetLabel(),
		Pattern: msg.GetPattern(),
	})
	if err != nil {
		return nil, s.mapAllowlistErr(err)
	}
	return connect.NewResponse(&adminv1.UpdateAllowlistEntryResponse{Entry: toAllowlistEntryProto(entry)}), nil
}

func (s *Service) RemoveAllowlistEntry(ctx context.Context, req *connect.Request[adminv1.RemoveAllowlistEntryRequest]) (*connect.Response[adminv1.RemoveAllowlistEntryResponse], error) {
	t := tenancy.MustTenant(ctx)
	if err := s.tenant.RemoveAllowlistEntry(ctx, t, req.Msg.GetPublicId()); err != nil {
		return nil, s.mapAllowlistErr(err)
	}
	return connect.NewResponse(&adminv1.RemoveAllowlistEntryResponse{}), nil
}

func (s *Service) mapAllowlistErr(err error) error {
	var fieldErr *tenant.ErrAllowlistEntryInvalid
	if errors.As(err, &fieldErr) {
		return s.invalidArg(fieldErr.Field, fieldErr.Err.Error())
	}
	if errors.Is(err, tenant.ErrAllowlistEntryDuplicate) {
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("allowlist entry already exists"))
	}
	if errors.Is(err, tenant.ErrAllowlistEntryNotFound) || errors.Is(err, tenant.ErrPresetNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if errors.Is(err, tenant.ErrPresetEmpty) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return s.internal("allowlist mutation", err)
}

func toAllowlistEntryProto(e tenant.AllowlistEntry) *adminv1.AllowlistEntry {
	return &adminv1.AllowlistEntry{
		PublicId:  e.PublicID,
		IdeKey:    e.IDEKey,
		Label:     e.Label,
		Pattern:   e.Pattern,
		CreatedAt: time.Unix(e.CreatedAt, 0).UTC().Format(timeFormatRFC3339),
	}
}

func toAllowlistEntryProtos(in []tenant.AllowlistEntry) []*adminv1.AllowlistEntry {
	out := make([]*adminv1.AllowlistEntry, 0, len(in))
	for _, e := range in {
		out = append(out, toAllowlistEntryProto(e))
	}
	return out
}
