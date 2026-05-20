package admin

import (
	"context"

	"connectrpc.com/connect"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
)

func (s *Service) CreateUpstream(_ context.Context, _ *connect.Request[adminv1.CreateUpstreamRequest]) (*connect.Response[adminv1.CreateUpstreamResponse], error) {
	return nil, errUnimplemented("slice-2")
}

func (s *Service) UpdateUpstream(_ context.Context, _ *connect.Request[adminv1.UpdateUpstreamRequest]) (*connect.Response[adminv1.UpdateUpstreamResponse], error) {
	return nil, errUnimplemented("slice-2")
}

func (s *Service) DeleteUpstream(_ context.Context, _ *connect.Request[adminv1.DeleteUpstreamRequest]) (*connect.Response[adminv1.DeleteUpstreamResponse], error) {
	return nil, errUnimplemented("slice-2")
}

func (s *Service) ReindexUpstreamCatalog(_ context.Context, _ *connect.Request[adminv1.ReindexUpstreamCatalogRequest]) (*connect.Response[adminv1.ReindexUpstreamCatalogResponse], error) {
	return nil, errUnimplemented("slice-2")
}

func (s *Service) PreviewUpstreamContext(_ context.Context, _ *connect.Request[adminv1.PreviewUpstreamContextRequest]) (*connect.Response[adminv1.PreviewUpstreamContextResponse], error) {
	return nil, errUnimplemented("slice-2")
}
