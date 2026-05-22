// Package protoview converts upstream package value types into the
// generated portalv1 proto shapes shared by PortalService and
// AdminService.
//
// Lives in its own subpackage so cmd/gateway (which imports
// internal/upstream) does not transitively pull internal/portal/* —
// the import-graph guard in cmd/gateway/import_graph_test.go forbids
// that and would catch a regression at build time.
package protoview

import (
	"time"

	"github.com/belphemur/limen/internal/portal/portalv1"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
)

// ToSummaryProto renders one UserUpstreamSummary row as its proto
// equivalent. The same converter is used by:
//
//   - PortalService.ListUpstreams — per-caller link state.
//   - AdminService.CreateUpstream / UpdateUpstream / ReindexUpstreamCatalog —
//     the freshly-mutated upstream rendered for the admin SPA.
//
// For the admin's "no calling user" case (e.g. immediately after
// CreateUpstream, before any link exists) callers construct a
// UserUpstreamSummary with LinkState = LinkStateNone and feed it
// through here. One converter, zero branches.
func ToSummaryProto(r upstream.UserUpstreamSummary) *portalv1.UpstreamSummary {
	up := r.Upstream
	displayName := up.DisplayName
	if displayName == "" {
		displayName = up.Identifier
	}
	out := &portalv1.UpstreamSummary{
		PublicId:        up.PublicID,
		Identifier:      up.Identifier,
		DisplayName:     displayName,
		McpUrl:          up.McpServerURL,
		StrategyType:    up.StrategyType,
		StrategySubMode: r.StrategySubMode,
		RequiresLink:    r.RequiresLink,
		LinkState:       linkStateProto(r.LinkState),
		LastErrorReason: r.LastErrorReason,
		Aliases:         r.Aliases,
		Tools:           toToolProtos(r.Tools),
		HasUserOverride: r.HasUserOverride,
	}
	if r.Link != nil && r.Link.LastFailureAt != nil {
		out.LastErrorAt = r.Link.LastFailureAt.UTC().Format(time.RFC3339)
	}
	return out
}

func toToolProtos(rows []storage.UpstreamTool) []*portalv1.UpstreamTool {
	if len(rows) == 0 {
		return nil
	}
	out := make([]*portalv1.UpstreamTool, 0, len(rows))
	for i := range rows {
		out = append(out, &portalv1.UpstreamTool{
			Name:        rows[i].Name,
			Description: rows[i].Description,
		})
	}
	return out
}

func linkStateProto(s upstream.LinkState) portalv1.LinkState {
	switch s {
	case upstream.LinkStateNone:
		return portalv1.LinkState_LINK_STATE_NONE
	case upstream.LinkStateConnected:
		return portalv1.LinkState_LINK_STATE_CONNECTED
	case upstream.LinkStateDisabled:
		return portalv1.LinkState_LINK_STATE_DISABLED
	case upstream.LinkStateAutoDisabled:
		return portalv1.LinkState_LINK_STATE_AUTO_DISABLED
	case upstream.LinkStateNeedsRelink:
		return portalv1.LinkState_LINK_STATE_NEEDS_RELINK
	default:
		return portalv1.LinkState_LINK_STATE_UNSPECIFIED
	}
}
