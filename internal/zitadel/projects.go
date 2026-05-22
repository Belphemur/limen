package zitadel

import (
	"context"
	"fmt"

	zsdk "github.com/zitadel/zitadel-go/v3/pkg/client"
	filterV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	projectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	"google.golang.org/grpc/metadata"
)

// EnsureProject returns the projectID of a project named `name` inside
// orgID. If no such project exists it is created. The lookup uses an
// EQUALS name filter and the call is safe to retry: a concurrent
// CreateProject that loses the race will surface a Zitadel AlreadyExists
// error, which the caller can map to a retry.
//
// Used by the DCR proxy to give each registering MCP client (Cursor,
// Claude Desktop, …) its own Zitadel project under the tenant's
// organization. See docs/phases/phase-07b-dcr-per-client-project.md.
func (c *Client) EnsureProject(ctx context.Context, orgID, name string) (string, error) {
	if orgID == "" {
		return "", fmt.Errorf("zitadel: EnsureProject: orgID is required")
	}
	if name == "" {
		return "", fmt.Errorf("zitadel: EnsureProject: name is required")
	}

	list, err := c.api.ProjectServiceV2().ListProjects(ctx, &projectV2.ListProjectsRequest{
		Filters: []*projectV2.ProjectSearchFilter{
			{Filter: &projectV2.ProjectSearchFilter_OrganizationIdFilter{
				OrganizationIdFilter: &projectV2.ProjectOrganizationIDFilter{
					OrganizationId: orgID,
					Type:           projectV2.ProjectOrganizationIDFilter_OWNED,
				},
			}},
			{Filter: &projectV2.ProjectSearchFilter_ProjectNameFilter{
				ProjectNameFilter: &projectV2.ProjectNameFilter{
					ProjectName: name,
					Method:      filterV2.TextFilterMethod_TEXT_FILTER_METHOD_EQUALS,
				},
			}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("zitadel: list projects (org=%q name=%q): %w", orgID, name, err)
	}
	for _, p := range list.GetProjects() {
		if p.GetName() == name {
			return p.GetProjectId(), nil
		}
	}

	resp, err := c.api.ProjectServiceV2().CreateProject(ctx, &projectV2.CreateProjectRequest{
		OrganizationId: orgID,
		Name:           name,
	})
	if err != nil {
		return "", fmt.Errorf("zitadel: create project (org=%q name=%q): %w", orgID, name, err)
	}
	return resp.GetProjectId(), nil
}

// FindProjectGrantID returns the GrantId of the project grant that
// grants projectID to grantedOrgID, or "" if no such grant exists.
//
// Project grants are identified by (projectID, grantID) tuples in
// Zitadel Console URLs (/ui/console/granted-projects/<projectId>/grant/<grantId>),
// and v2 ProjectService does not expose the grant ID — only v1
// ManagementService.ListGrantedProjects returns GrantedProject.GrantId.
// The org context is selected by the x-zitadel-orgid header (the
// granted org), matching how the rest of the v1 mgmt surface scopes
// per-org reads.
func (c *Client) FindProjectGrantID(ctx context.Context, projectID, grantedOrgID string) (string, error) {
	if projectID == "" || grantedOrgID == "" {
		return "", nil
	}
	ctx = metadata.AppendToOutgoingContext(ctx, zsdk.OrgHeader, grantedOrgID)
	// v2 ProjectService.ListProjectGrants does not return the grant_id field
	// required to build /ui/console/granted-projects/<projectId>/grant/<grantId>
	// deeplinks (grants are keyed by (project_id, granted_org_id) in v2). Until
	// upstream exposes grant_id on v2 ProjectGrant, the v1 mgmt call is the only
	// path that yields it.
	resp, err := c.api.ManagementService().ListGrantedProjects(ctx, &management.ListGrantedProjectsRequest{}) //nolint:staticcheck // SA1019: v2 ProjectService has no grant_id surface; see comment above.
	if err != nil {
		return "", fmt.Errorf("zitadel: list granted projects (org=%q): %w", grantedOrgID, err)
	}
	for _, gp := range resp.GetResult() {
		if gp.GetProjectId() == projectID {
			return gp.GetGrantId(), nil
		}
	}
	return "", nil
}
