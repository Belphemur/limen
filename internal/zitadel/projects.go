package zitadel

import (
	"context"
	"fmt"

	filterV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	projectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
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
