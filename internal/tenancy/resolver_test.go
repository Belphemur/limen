package tenancy

import (
	"context"
	"testing"

	"github.com/belphemur/limen/internal/storage"
)

func TestValidateSlug(t *testing.T) {
	tests := []struct {
		name    string
		slug    string
		wantErr error
	}{
		{"single char", "a", nil},
		{"alnum", "acme1", nil},
		{"hyphenated", "acme-corp", nil},
		{"max len 32", "abcdefghijklmnopqrstuvwxyz012345", nil},

		{"empty", "", ErrInvalidSlug},
		{"uppercase", "Acme", ErrInvalidSlug},
		{"leading hyphen", "-acme", ErrInvalidSlug},
		{"trailing hyphen", "acme-", ErrInvalidSlug},
		{"too long 33", "abcdefghijklmnopqrstuvwxyz0123456", ErrInvalidSlug},
		{"underscore", "acme_corp", ErrInvalidSlug},
		{"dot", "acme.corp", ErrInvalidSlug},
		{"space", "acme corp", ErrInvalidSlug},
		{"unicode", "café", ErrInvalidSlug},

		{"reserved api", "api", ErrReservedSlug},
		{"reserved auth", "auth", ErrReservedSlug},
		{"reserved oauth", "oauth", ErrReservedSlug},
		{"reserved oidc", "oidc", ErrReservedSlug},
		{"reserved admin", "admin", ErrReservedSlug},
		{"reserved t", "t", ErrReservedSlug},
		{"reserved health", "health", ErrReservedSlug},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSlug(tt.slug)
			if tt.wantErr == nil && err != nil {
				t.Errorf("ValidateSlug(%q) = %v, want nil", tt.slug, err)
			}
			if tt.wantErr != nil && err != tt.wantErr {
				t.Errorf("ValidateSlug(%q) = %v, want %v", tt.slug, err, tt.wantErr)
			}
		})
	}
}

func TestIsReserved(t *testing.T) {
	cases := map[string]bool{
		"auth":  true,
		"admin": true,
		"acme":  false,
		"":      false,
	}
	for slug, want := range cases {
		if got := IsReserved(slug); got != want {
			t.Errorf("IsReserved(%q) = %v, want %v", slug, got, want)
		}
	}
}

func TestStaffSlugIsStructurallyExcluded(t *testing.T) {
	// _staff must not match the customer regex — Phase 12 relies on this.
	if SlugPattern.MatchString("_staff") {
		t.Errorf("SlugPattern accepted _staff; it must be structurally excluded")
	}
}

func TestTenantFromContext_AbsentByDefault(t *testing.T) {
	if _, ok := TenantFromContext(context.Background()); ok {
		t.Errorf("expected absence in fresh context")
	}
}

func TestWithTenant_RoundTrip(t *testing.T) {
	want := &storage.Tenant{Slug: "acme", Name: "Acme"}
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
	want := &storage.Tenant{Slug: "acme"}
	ctx := WithTenant(context.Background(), want)
	if got := MustTenant(ctx); got != want {
		t.Errorf("MustTenant returned different pointer")
	}
}
