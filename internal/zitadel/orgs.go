package zitadel

import (
	"context"
	"fmt"

	orgV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
)

// Organization is the Limen-shaped result of CreateOrganization.
type Organization struct {
	ID string
	// AdminUserID is the Zitadel user ID of the first admin created
	// alongside the org (empty if no admins were provided).
	AdminUserID string
}

// SeedAdmin describes the inline human admin to create when provisioning a
// new tenant org. If ExistingUserID is set, no human is created and the
// existing user is assigned as ORG_OWNER instead.
type SeedAdmin struct {
	ExistingUserID string
	Email          string
	GivenName      string
	FamilyName     string
}

// CreateOrganization provisions a fresh Zitadel organization named `name`
// and (optionally) installs a seed admin user. If admin.ExistingUserID is
// set, that user is granted ORG_OWNER on the new org; otherwise a new
// human is created from admin.{Email,GivenName,FamilyName} and Zitadel
// sends an invite email with the default URL.
func (c *Client) CreateOrganization(ctx context.Context, name string, admin *SeedAdmin) (*Organization, error) {
	req := &orgV2.AddOrganizationRequest{Name: name}
	if admin != nil {
		entry := &orgV2.AddOrganizationRequest_Admin{}
		switch {
		case admin.ExistingUserID != "":
			entry.UserType = &orgV2.AddOrganizationRequest_Admin_UserId{
				UserId: admin.ExistingUserID,
			}
		case admin.Email != "":
			entry.UserType = &orgV2.AddOrganizationRequest_Admin_Human{
				Human: &userV2.AddHumanUserRequest{
					Profile: &userV2.SetHumanProfile{
						GivenName:  admin.GivenName,
						FamilyName: admin.FamilyName,
					},
					Email: &userV2.SetHumanEmail{Email: admin.Email},
				},
			}
		default:
			return nil, fmt.Errorf("zitadel: CreateOrganization: admin requires ExistingUserID or Email")
		}
		req.Admins = []*orgV2.AddOrganizationRequest_Admin{entry}
	}

	resp, err := c.api.OrganizationServiceV2().AddOrganization(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("zitadel: add organization %q: %w", name, err)
	}
	out := &Organization{ID: resp.GetOrganizationId()}
	if created := resp.GetCreatedAdmins(); len(created) > 0 {
		out.AdminUserID = created[0].GetUserId()
	} else if admin != nil && admin.ExistingUserID != "" {
		out.AdminUserID = admin.ExistingUserID
	}
	return out, nil
}

// SetOrgMetadata writes a single (key, value) metadata entry on the given
// Zitadel organization. Limen mirrors the tenant's internal PublicID onto
// its Zitadel org so the two systems can be cross-referenced from either
// side.
func (c *Client) SetOrgMetadata(ctx context.Context, orgID, key string, value []byte) error {
	if orgID == "" {
		return fmt.Errorf("zitadel: SetOrgMetadata: orgID is required")
	}
	if key == "" {
		return fmt.Errorf("zitadel: SetOrgMetadata: key is required")
	}
	if _, err := c.api.OrganizationServiceV2().SetOrganizationMetadata(ctx, &orgV2.SetOrganizationMetadataRequest{
		OrganizationId: orgID,
		Metadata: []*orgV2.Metadata{
			{Key: key, Value: value},
		},
	}); err != nil {
		return fmt.Errorf("zitadel: set org metadata (org=%q key=%q): %w", orgID, key, err)
	}
	return nil
}
