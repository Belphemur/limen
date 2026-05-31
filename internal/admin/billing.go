package admin

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/tenancy"
)

// GetActiveUserChart returns daily distinct active user counts for the chart.
func (s *Service) GetActiveUserChart(ctx context.Context, req *connect.Request[adminv1.GetActiveUserChartRequest]) (*connect.Response[adminv1.GetActiveUserChartResponse], error) {
	// Ensure tenant is present in context; RLS will enforce isolation.
	_ = tenancy.MustTenant(ctx)

	from := defaultFromDate(req.Msg.FromDate)
	to := defaultToDate(req.Msg.ToDate)

	if from.After(to) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("from_date cannot be after to_date"))
	}

	db, commit, err := s.store.Session(ctx)
	if err != nil {
		return nil, s.internal("GetActiveUserChart session", err)
	}
	defer func() { _ = commit() }()

	// Fast existence check — RLS filters to the current tenant automatically.
	var hasData bool
	if err := db.Raw(
		"SELECT EXISTS(SELECT 1 FROM active_user_months WHERE deleted_at IS NULL)",
	).Scan(&hasData).Error; err != nil {
		return nil, s.internal("GetActiveUserChart EXISTS", err)
	}
	if !hasData {
		return connect.NewResponse(&adminv1.GetActiveUserChartResponse{HasData: false}), nil
	}

	// Query daily distinct counts with zero-fill via generate_series.
	// tenant_id filter is omitted — RLS handles tenant isolation.
	type row struct {
		Date  string
		Count int32
	}
	var rows []row
	if err := db.Raw(`
		SELECT d::date::text AS date, COALESCE(t.cnt, 0)::int AS count
		FROM generate_series(?::date, ?::date, '1 day'::interval) d
		LEFT JOIN (
			SELECT month_start::date AS month_date, COUNT(DISTINCT COALESCE(user_id, service_account_id)) AS cnt
			FROM active_user_months
			WHERE month_start >= date_trunc('month', ?::date)::date
			  AND month_start <= date_trunc('month', ?::date)::date
			  AND deleted_at IS NULL
			GROUP BY month_start::date
		) t ON date_trunc('month', d)::date = t.month_date
		GROUP BY d::date, t.cnt ORDER BY d::date
	`, from, to, from, to).Scan(&rows).Error; err != nil {
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
	// Ensure tenant is present in context; RLS will enforce isolation.
	_ = tenancy.MustTenant(ctx)

	from := defaultFromDate(req.Msg.FromDate)
	to := defaultToDate(req.Msg.ToDate)

	if from.After(to) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("from_date cannot be after to_date"))
	}

	db, commit, err := s.store.Session(ctx)
	if err != nil {
		return nil, s.internal("GetSAConnectionChart session", err)
	}
	defer func() { _ = commit() }()

	// Fast existence check — RLS filters to the current tenant automatically.
	var hasData bool
	if err := db.Raw(
		"SELECT EXISTS(SELECT 1 FROM sa_connection_snapshots WHERE deleted_at IS NULL)",
	).Scan(&hasData).Error; err != nil {
		return nil, s.internal("GetSAConnectionChart EXISTS", err)
	}
	if !hasData {
		return connect.NewResponse(&adminv1.GetSAConnectionChartResponse{HasData: false}), nil
	}

	// Query daily peak concurrent connections with zero-fill via generate_series.
	// tenant_id filter is omitted — RLS handles tenant isolation.
	type row struct {
		Date string
		Peak int32
	}
	var rows []row
	if err := db.Raw(`
		SELECT d::date::text AS date, COALESCE(MAX(t.peak), 0)::int AS peak
		FROM generate_series(?::date, ?::date, '1 day'::interval) d
		LEFT JOIN (
			SELECT date_trunc('month', connected_at)::date AS month_date, MAX(concurrent_count) AS peak
			FROM sa_connection_snapshots
			WHERE connected_at >= date_trunc('month', ?::date)::date
			  AND connected_at <= date_trunc('month', ?::date)::date
			  AND deleted_at IS NULL
			GROUP BY date_trunc('month', connected_at)::date
		) t ON date_trunc('month', d)::date = t.month_date
		GROUP BY d::date, t.peak ORDER BY d::date
	`, from, to, from, to).Scan(&rows).Error; err != nil {
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
