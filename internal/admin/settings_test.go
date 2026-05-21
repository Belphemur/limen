package admin

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
)

func TestAdmin_GetTenantSettings_LazyCreatesAndReturnsTenantShape(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, tnt, _ := mountReal(t, []string{"admin"})
	resp, err := c.GetTenantSettings(context.Background(), connect.NewRequest(&adminv1.GetTenantSettingsRequest{}))
	if err != nil {
		t.Fatalf("GetTenantSettings: %v", err)
	}
	if got := resp.Msg.GetSettings().GetName(); got != tnt.Name {
		t.Errorf("name = %q, want %q", got, tnt.Name)
	}
	if got := resp.Msg.GetSettings().GetPublicId(); got != tnt.PublicID {
		t.Errorf("public_id = %q, want %q", got, tnt.PublicID)
	}
	if got := resp.Msg.GetZitadelOrgId(); got != tnt.ZitadelOrgID {
		t.Errorf("zitadel_org_id = %q, want %q", got, tnt.ZitadelOrgID)
	}
	if len(resp.Msg.GetDcrRedirectUriAllowlist()) != 0 {
		t.Errorf("allowlist non-empty: %v", resp.Msg.GetDcrRedirectUriAllowlist())
	}
}

func TestAdmin_UpdateTenantSettings_AllowlistSet_RoundTrips(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _ := mountReal(t, []string{"admin"})
	resp, err := c.UpdateTenantSettings(context.Background(), connect.NewRequest(&adminv1.UpdateTenantSettingsRequest{
		DcrRedirectUriAllowlist:    []string{"https://app.example.com/cb"},
		DcrRedirectUriAllowlistSet: true,
	}))
	if err != nil {
		t.Fatalf("UpdateTenantSettings: %v", err)
	}
	got := resp.Msg.GetDcrRedirectUriAllowlist()
	if len(got) != 1 || got[0] != "https://app.example.com/cb" {
		t.Errorf("allowlist = %v", got)
	}

	// Confirm via Get.
	getResp, err := c.GetTenantSettings(context.Background(), connect.NewRequest(&adminv1.GetTenantSettingsRequest{}))
	if err != nil {
		t.Fatalf("GetTenantSettings: %v", err)
	}
	got = getResp.Msg.GetDcrRedirectUriAllowlist()
	if len(got) != 1 || got[0] != "https://app.example.com/cb" {
		t.Errorf("read-back allowlist = %v", got)
	}
}

func TestAdmin_UpdateTenantSettings_InvalidAllowlistEntry_InvalidArgument(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _ := mountReal(t, []string{"admin"})
	_, err := c.UpdateTenantSettings(context.Background(), connect.NewRequest(&adminv1.UpdateTenantSettingsRequest{
		DcrRedirectUriAllowlist:    []string{"https://ok.example.com/cb", "no-scheme"},
		DcrRedirectUriAllowlistSet: true,
	}))
	if err == nil {
		t.Fatal("want CodeInvalidArgument, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument: %v", got, err)
	}
}

func TestAdmin_UpdateTenantSettings_EmptyName_InvalidArgument(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _ := mountReal(t, []string{"admin"})
	_, err := c.UpdateTenantSettings(context.Background(), connect.NewRequest(&adminv1.UpdateTenantSettingsRequest{
		Name: "   ",
	}))
	if err == nil {
		t.Fatal("want CodeInvalidArgument, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
}
