package admin

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/tenancy"
)

// GetActiveUserChart returns daily distinct active user counts for the chart.
func (s *Service) GetActiveUserChart(ctx context.Context, req *connect.Request[adminv1.GetActiveUserChartRequest]) (*connect.Response[adminv1.GetActiveUserChartResponse], error) {
	t := tenancy.MustTenant(ctx)

	from := defaultFromDate(req.Msg.FromDate)
	to := defaultToDate(req.Msg.ToDate)

	// Fast existence check
	var hasData bool
	if err := s.store.RawDB().Raw(
		"SELECT EXISTS(SELECT 1 FROM active_user_months WHERE tenant_id = ? AND deleted_at IS NULL)", t.ID,
	).Scan(&hasData).Error; err != nil {
		return nil, s.internal("GetActiveUserChart EXISTS", err)
	}
	if !hasData {
		return connect.NewResponse(&adminv1.GetActiveUserChartResponse{HasData: false}), nil
	}

	// Query daily distinct counts with zero-fill via generate_series
	type row struct {
		Date  string
		Count int32
	}
	var rows []row
	if err := s.store.RawDB().Raw(`
		SELECT d::date::text AS date, COALESCE(SUM(cnt), 0)::int AS count
		FROM generate_series(?::date, ?::date - 1, '1 day'::interval) d
		LEFT JOIN (
			SELECT month_start::date AS date, COUNT(DISTINCT COALESCE(user_id, service_account_id)) AS cnt
			FROM active_user_months
			WHERE tenant_id = ? AND month_start >= ? AND month_start < ? AND deleted_at IS NULL
			GROUP BY month_start::date
		) t ON d::date = t.date
		GROUP BY d::date ORDER BY d::date
	`, from, to, t.ID, from, to).Scan(&rows).Error; err != nil {
		return nil, s.internal("GetActiveUserChart query", err)
	}

	points := make([]*adminv1.GetActiveUserChartResponse_DataPoint, len(rows))
	for i, r := range rows {
		points[i] = &adminv1.GetActiveUserChartResponse_DataPoint{
			Date:            r.Date,
			ActiveUserCount: r.Count,
		}
	}
	return connect.NewResponse(&adminv1.GetActiveUserChartResponse{Days: points, HasData: true}), nil
}

// GetSAConnectionChart returns daily peak concurrent SA connections for the chart.
func (s *Service) GetSAConnectionChart(ctx context.Context, req *connect.Request[adminv1.GetSAConnectionChartRequest]) (*connect.Response[adminv1.GetSAConnectionChartResponse], error) {
	t := tenancy.MustTenant(ctx)

	from := defaultFromDate(req.Msg.FromDate)
	to := defaultToDate(req.Msg.ToDate)

	var hasData bool
	if err := s.store.RawDB().Raw(
		"SELECT EXISTS(SELECT 1 FROM sa_connection_snapshots WHERE tenant_id = ? AND deleted_at IS NULL)", t.ID,
	).Scan(&hasData).Error; err != nil {
		return nil, s.internal("GetSAConnectionChart EXISTS", err)
	}
	if !hasData {
		return connect.NewResponse(&adminv1.GetSAConnectionChartResponse{HasData: false}), nil
	}

	type row struct {
		Date string
		Peak int32
	}
	var rows []row
	if err := s.store.RawDB().Raw(`
		SELECT d::date::text AS date, COALESCE(MAX(peak), 0)::int AS peak
		FROM generate_series(?::date, ?::date - 1, '1 day'::interval) d
		LEFT JOIN (
			SELECT connected_at::date AS date, MAX(concurrent_count) AS peak
			FROM sa_connection_snapshots
			WHERE tenant_id = ? AND connected_at >= ? AND connected_at < ? AND deleted_at IS NULL
			GROUP BY connected_at::date
		) t ON d::date = t.date
		GROUP BY d::date ORDER BY d::date
	`, from, to, t.ID, from, to).Scan(&rows).Error; err != nil {
		return nil, s.internal("GetSAConnectionChart query", err)
	}

	points := make([]*adminv1.GetSAConnectionChartResponse_DataPoint, len(rows))
	for i, r := range rows {
		points[i] = &adminv1.GetSAConnectionChartResponse_DataPoint{
			Date:            r.Date,
			PeakConnections: r.Peak,
		}
	}
	return connect.NewResponse(&adminv1.GetSAConnectionChartResponse{Days: points, HasData: true}), nil
}

// defaultFromDate parses a timestamp or returns 30 days ago.
func defaultFromDate(ts *timestamppb.Timestamp) time.Time {
	if ts != nil && ts.IsValid() {
		return ts.AsTime().Truncate(24 * time.Hour)
	}
	return time.Now().Add(-30 * 24 * time.Hour).Truncate(24 * time.Hour)
}

// defaultToDate parses a timestamp or returns today.
func defaultToDate(ts *timestamppb.Timestamp) time.Time {
	if ts != nil && ts.IsValid() {
		return ts.AsTime().Truncate(24 * time.Hour)
	}
	return time.Now().Truncate(24 * time.Hour)
}
