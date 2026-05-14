package tenancy

import (
	"context"
	"testing"

	"github.com/belphemur/limen/internal/storage"
)

func TestTenantFromContext_AbsentByDefault(t *testing.T) {
	if _, ok := TenantFromContext(context.Background()); ok {
		t.Errorf("expected absence in fresh context")
	}
}

func TestWithTenant_RoundTrip(t *testing.T) {
	want := &storage.Tenant{Name: "Acme"}
	want.PublicID = "tnt_01H000000000000000000000"
	ctx := WithTenant(context.Background(), want)

	got, ok := TenantFromContext(ctx)
	if !ok {
		t.Fatal("expected tenant in context")
	}
	if got != want {
		t.Errorf("tenant pointer mismatch")
	}
}

func TestMustTenant_PanicsWithoutMiddleware(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic")
		}
	}()
	_ = MustTenant(context.Background())
}

func TestMustTenant_ReturnsBoundTenant(t *testing.T) {
	want := &storage.Tenant{Name: "Acme"}
	want.PublicID = "tnt_01H000000000000000000000"
	ctx := WithTenant(context.Background(), want)
	if got := MustTenant(ctx); got != want {
		t.Errorf("MustTenant returned different pointer")
	}
}
