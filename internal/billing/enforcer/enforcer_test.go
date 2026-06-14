package enforcer

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/billing/entitlements"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/valkey"
)

// newMockRequest returns a connect.AnyRequest usable in tests.
func newMockRequest() connect.AnyRequest {
	return connect.NewRequest(&struct{}{})
}

// --- Context tests ---

func TestContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	ents := entitlements.PlanEntitlements{
		MaxActiveUsers:   10,
		MaxSAConnections: 5,
		MaxProjects:      3,
		StorageLimitMB:   1024,
		CodeMode:         true,
	}

	ctx = WithEntitlements(ctx, ents)
	got, ok := EntitlementsFromContext(ctx)
	if !ok {
		t.Fatal("expected entitlements in context")
	}
	if got != ents {
		t.Fatalf("expected %+v, got %+v", ents, got)
	}
}

func TestEntitlementsFromContext_Missing(t *testing.T) {
	ctx := context.Background()
	_, ok := EntitlementsFromContext(ctx)
	if ok {
		t.Fatal("expected no entitlements in context")
	}
}

// --- Cache tests ---

func TestCacheHitMissInvalidation(t *testing.T) {
	client := valkey.NewInMemory()
	cache := newEntitlementCache(client, nil)
	ctx := context.Background()
	ents := entitlements.PlanEntitlements{MaxActiveUsers: 42}

	// Miss
	cached, err := cache.get(ctx, 1)
	if err != nil {
		t.Fatalf("unexpected error on miss: %v", err)
	}
	if cached != nil {
		t.Fatal("expected cache miss")
	}

	// Set
	if err := cache.set(ctx, 1, ents); err != nil {
		t.Fatalf("unexpected error on set: %v", err)
	}

	// Hit
	cached, err = cache.get(ctx, 1)
	if err != nil {
		t.Fatalf("unexpected error on hit: %v", err)
	}
	if cached == nil {
		t.Fatal("expected cache hit")
	}
	if cached.MaxActiveUsers != 42 {
		t.Fatalf("expected MaxActiveUsers=42, got %d", cached.MaxActiveUsers)
	}

	// Invalidate
	if err := cache.invalidate(ctx, 1); err != nil {
		t.Fatalf("unexpected error on invalidate: %v", err)
	}

	// Miss again
	cached, err = cache.get(ctx, 1)
	if err != nil {
		t.Fatalf("unexpected error after invalidate: %v", err)
	}
	if cached != nil {
		t.Fatal("expected cache miss after invalidation")
	}
}

func TestCacheNilClient(t *testing.T) {
	cache := newEntitlementCache(nil, nil)
	ctx := context.Background()
	ents := entitlements.PlanEntitlements{MaxActiveUsers: 99}

	// get with nil client should return nil,nil
	cached, err := cache.get(ctx, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cached != nil {
		t.Fatal("expected nil on nil client")
	}

	// set with nil client should be no-op
	if err := cache.set(ctx, 1, ents); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// invalidate with nil client should be no-op
	if err := cache.invalidate(ctx, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Enforcer tests ---

func TestForTenant_CacheHit(t *testing.T) {
	client := valkey.NewInMemory()
	e := New(nil, client, zap.NewNop())
	ctx := context.Background()
	ents := entitlements.PlanEntitlements{MaxActiveUsers: 77}

	// Seed cache directly.
	if err := e.cache.set(ctx, 1, ents); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	got, err := e.ForTenant(ctx, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.MaxActiveUsers != 77 {
		t.Fatalf("expected MaxActiveUsers=77, got %d", got.MaxActiveUsers)
	}
}

func TestForTenant_CacheMiss_DBLoad(t *testing.T) {
	client := valkey.NewInMemory()
	e := New(nil, client, zap.NewNop())
	ctx := context.Background()

	// Override DB load to return custom entitlements.
	dbEnts := entitlements.PlanEntitlements{MaxActiveUsers: 55}
	e.cache.loadFromDBFunc = func(_ context.Context, _ int64) (entitlements.PlanEntitlements, error) {
		return dbEnts, nil
	}

	got, err := e.ForTenant(ctx, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.MaxActiveUsers != 55 {
		t.Fatalf("expected MaxActiveUsers=55, got %d", got.MaxActiveUsers)
	}

	// Next call should be a cache hit.
	// Override DB load to return different value; cache should win.
	e.cache.loadFromDBFunc = func(_ context.Context, _ int64) (entitlements.PlanEntitlements, error) {
		return entitlements.PlanEntitlements{MaxActiveUsers: 99}, nil
	}
	got, err = e.ForTenant(ctx, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.MaxActiveUsers != 55 {
		t.Fatalf("expected cached value 55, got %d", got.MaxActiveUsers)
	}
}

func TestForTenant_CacheMiss_DBError_Fallback(t *testing.T) {
	e := New(nil, nil, zap.NewNop())
	ctx := context.Background()

	// Force DB load to fail.
	e.cache.loadFromDBFunc = func(_ context.Context, _ int64) (entitlements.PlanEntitlements, error) {
		return entitlements.PlanEntitlements{}, errors.New("db down")
	}

	got, err := e.ForTenant(ctx, 1)
	if err != nil {
		t.Fatalf("expected nil error on fallback, got: %v", err)
	}
	want := entitlements.DeveloperEntitlements()
	if got != want {
		t.Fatalf("expected developer defaults %+v, got %+v", want, got)
	}
}

func TestInvalidate(t *testing.T) {
	client := valkey.NewInMemory()
	e := New(nil, client, zap.NewNop())
	ctx := context.Background()
	ents := entitlements.PlanEntitlements{MaxActiveUsers: 33}

	if err := e.cache.set(ctx, 1, ents); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	if err := e.Invalidate(ctx, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cached, err := e.cache.get(ctx, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cached != nil {
		t.Fatal("expected cache miss after invalidate")
	}
}

// --- Gate tests ---

func TestCheckMaxUsers_Unlimited(t *testing.T) {
	e := entitlements.PlanEntitlements{MaxActiveUsers: -1}
	if err := CheckMaxUsers(e, 99999); err != nil {
		t.Fatalf("expected nil for unlimited, got: %v", err)
	}
}

func TestCheckMaxUsers_Allowed(t *testing.T) {
	e := entitlements.PlanEntitlements{MaxActiveUsers: 5}
	if err := CheckMaxUsers(e, 4); err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
}

func TestCheckMaxUsers_AtLimit(t *testing.T) {
	e := entitlements.PlanEntitlements{MaxActiveUsers: 5}
	err := CheckMaxUsers(e, 5)
	if err == nil {
		t.Fatal("expected error at limit")
	}
	locked, ok := err.(*ErrFeatureLocked)
	if !ok {
		t.Fatalf("expected *ErrFeatureLocked, got %T", err)
	}
	if locked.Feature != "max-users" {
		t.Fatalf("expected feature max-users, got %s", locked.Feature)
	}
	if locked.Limit != 5 {
		t.Fatalf("expected limit 5, got %d", locked.Limit)
	}
	if locked.Usage != 5 {
		t.Fatalf("expected usage 5, got %d", locked.Usage)
	}
}

func TestCheckMaxServiceAccounts_Unlimited(t *testing.T) {
	e := entitlements.PlanEntitlements{MaxServiceAccounts: -1}
	if err := CheckMaxServiceAccounts(e, 99999); err != nil {
		t.Fatalf("expected nil for unlimited, got: %v", err)
	}
}

func TestCheckMaxServiceAccounts_AtLimit(t *testing.T) {
	e := entitlements.PlanEntitlements{MaxServiceAccounts: 3}
	err := CheckMaxServiceAccounts(e, 3)
	if err == nil {
		t.Fatal("expected error at limit")
	}
	locked, ok := err.(*ErrFeatureLocked)
	if !ok {
		t.Fatalf("expected *ErrFeatureLocked, got %T", err)
	}
	if locked.Feature != "max-service-accounts" {
		t.Fatalf("expected feature max-service-accounts, got %s", locked.Feature)
	}
}

func TestCheckSAConnectionLimit_Unlimited(t *testing.T) {
	e := entitlements.PlanEntitlements{MaxSAConnections: -1}
	if err := CheckSAConnectionLimit(e, 99999); err != nil {
		t.Fatalf("expected nil for unlimited, got: %v", err)
	}
}

func TestCheckSAConnectionLimit_AtLimit(t *testing.T) {
	e := entitlements.PlanEntitlements{MaxSAConnections: 3}
	err := CheckSAConnectionLimit(e, 3)
	if err == nil {
		t.Fatal("expected error at limit")
	}
	connErr, ok := err.(*ErrSAConnectionLimit)
	if !ok {
		t.Fatalf("expected *ErrSAConnectionLimit, got %T", err)
	}
	if connErr.Limit != 3 {
		t.Fatalf("expected limit 3, got %d", connErr.Limit)
	}
	if connErr.Usage != 3 {
		t.Fatalf("expected usage 3, got %d", connErr.Usage)
	}
}

func TestCheckMaxSAConnections_Unlimited(t *testing.T) {
	e := entitlements.PlanEntitlements{MaxSAConnections: -1}
	if err := CheckMaxSAConnections(e, 99999); err != nil {
		t.Fatalf("expected nil for unlimited, got: %v", err)
	}
}

func TestCheckMaxSAConnections_AtLimit(t *testing.T) {
	e := entitlements.PlanEntitlements{MaxSAConnections: 3}
	err := CheckMaxSAConnections(e, 3)
	if err == nil {
		t.Fatal("expected error at limit")
	}
	locked, ok := err.(*ErrFeatureLocked)
	if !ok {
		t.Fatalf("expected *ErrFeatureLocked, got %T", err)
	}
	if locked.Feature != "max-sa-connections" {
		t.Fatalf("expected feature max-sa-connections, got %s", locked.Feature)
	}
}

func TestCheckMaxProjects_Unlimited(t *testing.T) {
	e := entitlements.PlanEntitlements{MaxProjects: -1}
	if err := CheckMaxProjects(e, 99999); err != nil {
		t.Fatalf("expected nil for unlimited, got: %v", err)
	}
}

func TestCheckMaxProjects_AtLimit(t *testing.T) {
	e := entitlements.PlanEntitlements{MaxProjects: 10}
	err := CheckMaxProjects(e, 10)
	if err == nil {
		t.Fatal("expected error at limit")
	}
	locked, ok := err.(*ErrFeatureLocked)
	if !ok {
		t.Fatalf("expected *ErrFeatureLocked, got %T", err)
	}
	if locked.Feature != "max-projects" {
		t.Fatalf("expected feature max-projects, got %s", locked.Feature)
	}
}

func TestCheckStorageLimit_Unlimited(t *testing.T) {
	e := entitlements.PlanEntitlements{StorageLimitMB: -1}
	if err := CheckStorageLimit(e, 99999); err != nil {
		t.Fatalf("expected nil for unlimited, got: %v", err)
	}
}

func TestCheckStorageLimit_AtLimit(t *testing.T) {
	e := entitlements.PlanEntitlements{StorageLimitMB: 1024}
	err := CheckStorageLimit(e, 1024)
	if err == nil {
		t.Fatal("expected error at limit")
	}
	locked, ok := err.(*ErrFeatureLocked)
	if !ok {
		t.Fatalf("expected *ErrFeatureLocked, got %T", err)
	}
	if locked.Feature != "max-storage" {
		t.Fatalf("expected feature max-storage, got %s", locked.Feature)
	}
}

func TestCheckFeature_Enabled(t *testing.T) {
	e := entitlements.PlanEntitlements{}
	if err := CheckFeature(e, "advanced-ai", true); err != nil {
		t.Fatalf("expected nil for enabled feature, got: %v", err)
	}
}

func TestCheckFeature_Disabled(t *testing.T) {
	e := entitlements.PlanEntitlements{}
	err := CheckFeature(e, "advanced-ai", false)
	if err == nil {
		t.Fatal("expected error for disabled feature")
	}
	locked, ok := err.(*ErrFeatureLocked)
	if !ok {
		t.Fatalf("expected *ErrFeatureLocked, got %T", err)
	}
	if locked.Feature != "advanced-ai" {
		t.Fatalf("expected feature advanced-ai, got %s", locked.Feature)
	}
	if locked.Limit != -1 || locked.Usage != -1 {
		t.Fatalf("expected limit=-1 and usage=-1 for boolean feature, got limit=%d usage=%d", locked.Limit, locked.Usage)
	}
}

// --- Error tests ---

func TestErrFeatureLocked_Error(t *testing.T) {
	err := NewFeatureLockedError("max-users", 5, 3, "user limit reached")
	want := "billing.limit.max-users: user limit reached (limit=5, usage=3)"
	if got := err.Error(); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestErrSAConnectionLimit_Error(t *testing.T) {
	err := NewSAConnectionLimitError(3, 3)
	want := "SA connection limit reached (3/3). Try again later."
	if got := err.Error(); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestErrSAConnectionLimit_UnderLimit(t *testing.T) {
	err := NewSAConnectionLimitError(5, 3)
	want := "SA connection limit reached (3/5). Try again later."
	if got := err.Error(); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

// --- Interceptor tests ---

func TestBillingInterceptor_LoadsEntitlements(t *testing.T) {
	client := valkey.NewInMemory()
	e := New(nil, client, zap.NewNop())
	ctx := context.Background()
	ents := entitlements.PlanEntitlements{MaxActiveUsers: 42}

	// Seed cache.
	if err := e.cache.set(ctx, 7, ents); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	// Build interceptor.
	interceptor := BillingInterceptor(e, zap.NewNop())

	// Create a next handler that asserts entitlements are present.
	var gotEnts entitlements.PlanEntitlements
	var gotOK bool
	next := func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		gotEnts, gotOK = EntitlementsFromContext(ctx)
		return nil, nil
	}

	// Bind tenant to context.
	tenant := &storage.Tenant{Base: storage.Base{ID: 7, PublicID: "tnt_test"}, Name: "Acme"}
	ctx = tenancy.WithTenant(ctx, tenant)

	_, err := interceptor(next)(ctx, newMockRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gotOK {
		t.Fatal("expected entitlements in context after interceptor")
	}
	if gotEnts.MaxActiveUsers != 42 {
		t.Fatalf("expected MaxActiveUsers=42, got %d", gotEnts.MaxActiveUsers)
	}
}

func TestBillingInterceptor_NoTenant(t *testing.T) {
	client := valkey.NewInMemory()
	e := New(nil, client, zap.NewNop())
	ctx := context.Background()

	interceptor := BillingInterceptor(e, zap.NewNop())

	called := false
	next := func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	}

	_, err := interceptor(next)(ctx, newMockRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next to be called even without tenant")
	}
}

// --- Entitlement mapping tests ---

func TestEntitlementLimitFromLookupKey_ServiceAccountKeys(t *testing.T) {
	tests := []struct {
		lookupKey string
		want      int32
	}{
		{"max-service-account_1", 1},
		{"max-service-account_unlimited", -1},
		{"max-sa-connection_1", 1},
		{"max-sa-connection_unlimited", -1},
	}
	for _, tt := range tests {
		got := entitlements.EntitlementLimitFromLookupKey(tt.lookupKey, true)
		if got != tt.want {
			t.Errorf("EntitlementLimitFromLookupKey(%q, true) = %d, want %d", tt.lookupKey, got, tt.want)
		}
	}
}

func TestEntitlementsFromRows_ServiceAccountKeys(t *testing.T) {
	// max-service-account_1 should leave the developer default (1).
	rows := []storage.TenantEntitlement{
		{Feature: "max-service-account_1"},
	}
	e := entitlements.EntitlementsFromRows(rows)
	if e.MaxServiceAccounts != 1 {
		t.Fatalf("expected MaxServiceAccounts=1 (default), got %d", e.MaxServiceAccounts)
	}

	// max-service-account_unlimited should override to -1.
	rows = []storage.TenantEntitlement{
		{Feature: "max-service-account_unlimited"},
	}
	e = entitlements.EntitlementsFromRows(rows)
	if e.MaxServiceAccounts != -1 {
		t.Fatalf("expected MaxServiceAccounts=-1 (unlimited), got %d", e.MaxServiceAccounts)
	}
}

func TestBillingInterceptor_DBError(t *testing.T) {
	e := New(nil, nil, zap.NewNop())
	ctx := context.Background()

	// Force DB load to fail.
	e.cache.loadFromDBFunc = func(_ context.Context, _ int64) (entitlements.PlanEntitlements, error) {
		return entitlements.PlanEntitlements{}, errors.New("db down")
	}

	interceptor := BillingInterceptor(e, zap.NewNop())

	called := false
	next := func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	}

	tenant := &storage.Tenant{Base: storage.Base{ID: 9, PublicID: "tnt_test2"}, Name: "Beta"}
	ctx = tenancy.WithTenant(ctx, tenant)

	_, err := interceptor(next)(ctx, newMockRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected next to be called even when entitlements fail to load")
	}
}
