package zitadel

import (
	"context"
	"fmt"

	objectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	settingsV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/settings/v2"
)

// PasswordComplexitySettings represents the requirements of password complexity policy.
type PasswordComplexitySettings struct {
	MinLength         uint64
	RequiresUppercase bool
	RequiresLowercase bool
	RequiresNumber    bool
	RequiresSymbol    bool
}

// GetPasswordComplexitySettings retrieves the password complexity requirements for the given orgID,
// falling back to the instance-wide policy if it's empty or not custom-configured on the org.
func (c *Client) GetPasswordComplexitySettings(ctx context.Context, orgID string) (*PasswordComplexitySettings, error) {
	var reqCtx *objectV2.RequestContext
	if orgID != "" {
		reqCtx = &objectV2.RequestContext{
			ResourceOwner: &objectV2.RequestContext_OrgId{
				OrgId: orgID,
			},
		}
	} else {
		reqCtx = &objectV2.RequestContext{
			ResourceOwner: &objectV2.RequestContext_Instance{
				Instance: true,
			},
		}
	}

	req := &settingsV2.GetPasswordComplexitySettingsRequest{
		Ctx: reqCtx,
	}

	resp, err := c.api.SettingsServiceV2().GetPasswordComplexitySettings(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("zitadel: get password complexity settings (org=%q): %w", orgID, err)
	}

	s := resp.GetSettings()
	if s == nil {
		return &PasswordComplexitySettings{
			MinLength: 8, // fallback default
		}, nil
	}

	return &PasswordComplexitySettings{
		MinLength:         s.GetMinLength(),
		RequiresUppercase: s.GetRequiresUppercase(),
		RequiresLowercase: s.GetRequiresLowercase(),
		RequiresNumber:    s.GetRequiresNumber(),
		RequiresSymbol:    s.GetRequiresSymbol(),
	}, nil
}
