package zitadel

import (
	"context"
	"fmt"

	zsdk "github.com/zitadel/zitadel-go/v3/pkg/client"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	objectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	settingsV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/settings/v2"
	"google.golang.org/grpc/metadata"
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

// DisableOrgRegistration disables self-registration on the given org
// by setting AllowRegister=false in the login policy. All other login
// policy fields are preserved. The call is idempotent — re-running it
// on an org where registration is already disabled is a no-op.
//
// v2 has no equivalent for this operation as of v3.29.0 (SettingsServiceV2
// is read-only; AddCustomLoginPolicy / UpdateCustomLoginPolicy / GetLoginPolicy
// are only available on the v1 ManagementService).
func (c *Client) DisableOrgRegistration(ctx context.Context, orgID string) error {
	ctx = metadata.AppendToOutgoingContext(ctx, zsdk.OrgHeader, orgID)

	p, err := c.api.ManagementService().GetLoginPolicy(ctx, &management.GetLoginPolicyRequest{})
	if err != nil {
		return fmt.Errorf("zitadel: get login policy (org=%s): %w", orgID, err)
	}
	cur := p.GetPolicy()
	if !cur.GetAllowRegister() {
		return nil // already disabled — idempotent
	}

	if p.GetIsDefault() {
		// No custom policy yet — create one with current values + AllowRegister=false.
		_, err = c.api.ManagementService().AddCustomLoginPolicy(ctx, &management.AddCustomLoginPolicyRequest{
			AllowUsernamePassword:      cur.GetAllowUsernamePassword(),
			AllowRegister:              false,
			AllowExternalIdp:           cur.GetAllowExternalIdp(),
			ForceMfa:                   cur.GetForceMfa(),
			ForceMfaLocalOnly:          cur.GetForceMfaLocalOnly(),
			PasswordlessType:           cur.GetPasswordlessType(),
			HidePasswordReset:          cur.GetHidePasswordReset(),
			IgnoreUnknownUsernames:     cur.GetIgnoreUnknownUsernames(),
			AllowDomainDiscovery:       cur.GetAllowDomainDiscovery(),
			DisableLoginWithEmail:      cur.GetDisableLoginWithEmail(),
			DisableLoginWithPhone:      cur.GetDisableLoginWithPhone(),
			DefaultRedirectUri:         cur.GetDefaultRedirectUri(),
			PasswordCheckLifetime:      cur.GetPasswordCheckLifetime(),
			ExternalLoginCheckLifetime: cur.GetExternalLoginCheckLifetime(),
			MfaInitSkipLifetime:        cur.GetMfaInitSkipLifetime(),
			SecondFactorCheckLifetime:  cur.GetSecondFactorCheckLifetime(),
			MultiFactorCheckLifetime:   cur.GetMultiFactorCheckLifetime(),
			SecondFactors:              cur.GetSecondFactors(),
			MultiFactors:               cur.GetMultiFactors(),
		})
		if err != nil {
			return fmt.Errorf("zitadel: add custom login policy (org=%s): %w", orgID, err)
		}
	} else {
		// Custom policy exists — update AllowRegister only.
		_, err = c.api.ManagementService().UpdateCustomLoginPolicy(ctx, &management.UpdateCustomLoginPolicyRequest{
			AllowUsernamePassword:      cur.GetAllowUsernamePassword(),
			AllowRegister:              false,
			AllowExternalIdp:           cur.GetAllowExternalIdp(),
			ForceMfa:                   cur.GetForceMfa(),
			ForceMfaLocalOnly:          cur.GetForceMfaLocalOnly(),
			PasswordlessType:           cur.GetPasswordlessType(),
			HidePasswordReset:          cur.GetHidePasswordReset(),
			IgnoreUnknownUsernames:     cur.GetIgnoreUnknownUsernames(),
			AllowDomainDiscovery:       cur.GetAllowDomainDiscovery(),
			DisableLoginWithEmail:      cur.GetDisableLoginWithEmail(),
			DisableLoginWithPhone:      cur.GetDisableLoginWithPhone(),
			DefaultRedirectUri:         cur.GetDefaultRedirectUri(),
			PasswordCheckLifetime:      cur.GetPasswordCheckLifetime(),
			ExternalLoginCheckLifetime: cur.GetExternalLoginCheckLifetime(),
			MfaInitSkipLifetime:        cur.GetMfaInitSkipLifetime(),
			SecondFactorCheckLifetime:  cur.GetSecondFactorCheckLifetime(),
			MultiFactorCheckLifetime:   cur.GetMultiFactorCheckLifetime(),
		})
		if err != nil {
			return fmt.Errorf("zitadel: update custom login policy (org=%s): %w", orgID, err)
		}
	}
	return nil
}
