package admin

import (
	"context"

	"connectrpc.com/connect"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/idepresets"
	"github.com/belphemur/limen/internal/tenancy"
)

func (s *Service) ListIDEPresets(ctx context.Context, _ *connect.Request[adminv1.ListIDEPresetsRequest]) (*connect.Response[adminv1.ListIDEPresetsResponse], error) {
	_ = tenancy.MustTenant(ctx)
	tx, commit, err := s.store.Session(ctx)
	if err != nil {
		return nil, s.internal("open session", err)
	}
	defer func() { _ = commit() }()
	presets, err := idepresets.List(ctx, tx)
	if err != nil {
		return nil, s.internal("list ide presets", err)
	}
	out := make([]*adminv1.IDEPreset, 0, len(presets))
	for _, p := range presets {
		out = append(out, &adminv1.IDEPreset{
			Key:         p.Key,
			DisplayName: p.DisplayName,
			Icon:        p.Icon,
			Patterns:    append([]string(nil), p.Patterns...),
			SortOrder:   int32(p.SortOrder),
		})
	}
	return connect.NewResponse(&adminv1.ListIDEPresetsResponse{Presets: out}), nil
}

func (s *Service) ApplyIDEPreset(ctx context.Context, req *connect.Request[adminv1.ApplyIDEPresetRequest]) (*connect.Response[adminv1.ApplyIDEPresetResponse], error) {
	t := tenancy.MustTenant(ctx)
	res, err := s.tenant.ApplyIDEPreset(ctx, t, req.Msg.GetIdeKey())
	if err != nil {
		return nil, s.mapAllowlistErr(err)
	}
	return connect.NewResponse(&adminv1.ApplyIDEPresetResponse{
		Added:          int32(res.Added),
		AlreadyPresent: int32(res.AlreadyPresent),
	}), nil
}

func (s *Service) RemoveIDEPreset(ctx context.Context, req *connect.Request[adminv1.RemoveIDEPresetRequest]) (*connect.Response[adminv1.RemoveIDEPresetResponse], error) {
	t := tenancy.MustTenant(ctx)
	removed, err := s.tenant.RemoveIDEPreset(ctx, t, req.Msg.GetIdeKey())
	if err != nil {
		return nil, s.mapAllowlistErr(err)
	}
	return connect.NewResponse(&adminv1.RemoveIDEPresetResponse{Removed: int32(removed)}), nil
}
