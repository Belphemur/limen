package admin

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
)

func TestAdmin_DeleteTenant_ConfirmationMismatch_InvalidArgument(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _ := mountReal(t, []string{"owner"})
	_, err := c.DeleteTenant(context.Background(), connect.NewRequest(&adminv1.DeleteTenantRequest{
		PublicIdConfirmation: "tnt_wrong",
	}))
	if err == nil {
		t.Fatal("want CodeInvalidArgument, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument: %v", got, err)
	}
}

func TestAdmin_DeleteTenant_Owner_Succeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, tnt, _ := mountReal(t, []string{"owner"})
	if _, err := c.DeleteTenant(context.Background(), connect.NewRequest(&adminv1.DeleteTenantRequest{
		PublicIdConfirmation: tnt.PublicID,
	})); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
}
