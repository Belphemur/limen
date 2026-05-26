package zitadel

import (
	"context"
	"fmt"
	"strings"

	filterV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	projectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// IsAlreadyExists reports whether err is an idempotent "already exists"
// / "duplicate" condition from Zitadel. Some Zitadel handlers return
// FailedPrecondition or Internal with an "already exists" message
// rather than the canonical AlreadyExists code. Exported so callers
// outside this package (e.g. signup's slug-collision retry loop) can
// classify CreateOrganization errors uniformly.
func IsAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if s, ok := status.FromError(err); ok {
		switch s.Code() {
		case codes.AlreadyExists:
			return true
		case codes.FailedPrecondition, codes.Internal:
			low := strings.ToLower(s.Message())
			if strings.Contains(low, "already exists") || strings.Contains(low, "duplicate") {
				return true
			}
		}
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "already exists") || strings.Contains(low, "alreadyexists")
}

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

// EnsureProjectGrant grants the configured Limen project to grantedOrgID
// with the given roleKeys, idempotently. Required before any user
// authorization can be created for users in a non-owning organization
// (every tenant org self-served through signup).
//
// v2-only: uses ProjectServiceV2.CreateProjectGrant and tolerates the
// idempotent "already exists" condition that Zitadel returns when the
// grant is re-created.
func (c *Client) EnsureProjectGrant(ctx context.Context, grantedOrgID string, roleKeys []string) error {
	if grantedOrgID == "" {
		return fmt.Errorf("zitadel: EnsureProjectGrant: grantedOrgID is required")
	}
	_, err := c.api.ProjectServiceV2().CreateProjectGrant(ctx, &projectV2.CreateProjectGrantRequest{
		ProjectId:             c.projectID,
		GrantedOrganizationId: grantedOrgID,
		RoleKeys:              roleKeys,
	})
	if err != nil && !IsAlreadyExists(err) {
		return fmt.Errorf("zitadel: create project grant (project=%q org=%q): %w", c.projectID, grantedOrgID, err)
	}
	return nil
}
