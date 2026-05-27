package session

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/belphemur/limen/internal/auth"
	"github.com/zitadel/oidc/v3/pkg/oidc"
)

type mockImpersonationAdapter struct {
	payload auth.CookiePayloadV2
}

func (m *mockImpersonationAdapter) ResolvePortalSession(_ context.Context, _ http.Header, _ string) (*oidc.IDTokenClaims, string, *http.Cookie, error) {
	return nil, "", nil, nil
}

func (m *mockImpersonationAdapter) ResolveImpersonationSession(_ context.Context, _ http.Header, _ string) (*oidc.IDTokenClaims, string, *http.Cookie, error) {
	exp := oidc.FromTime(m.payload.ExpiresAt)
	claims := &oidc.IDTokenClaims{
		Claims: map[string]any{},
	}
	claims.Issuer = "https://auth.example.com"
	claims.Subject = m.payload.Subject
	claims.Expiration = exp
	claims.Email = m.payload.Email
	claims.GivenName = m.payload.FirstName
	claims.FamilyName = m.payload.LastName

	if len(m.payload.Roles) > 0 {
		roleMap := make(map[string]any)
		for _, r := range m.payload.Roles {
			roleMap[r] = map[string]any{}
		}
		claims.Claims["urn:zitadel:iam:org:project:roles"] = roleMap
	}

	// Populate the Claims map so the read path can bypass struct fields.
	claims.Claims["email"] = m.payload.Email
	claims.Claims["given_name"] = m.payload.FirstName
	claims.Claims["family_name"] = m.payload.LastName
	claims.Claims["name"] = m.payload.FirstName + " " + m.payload.LastName

	claims.Claims["actor_user_id"] = m.payload.ActorUserID
	claims.Claims["actor_email"] = m.payload.ActorEmail
	claims.Claims["actor_first_name"] = m.payload.ActorFirstName
	claims.Claims["actor_last_name"] = m.payload.ActorLastName
	claims.Claims["impersonation_reason"] = m.payload.Reason
	claims.Claims["target_user_type"] = "service_account"
	claims.Claims["impersonation_expires_at"] = m.payload.ExpiresAt.Format(time.RFC3339)

	return claims, m.payload.AccessToken, nil, nil
}

func TestOIDCImpersonationResolver_BuildsSessionFromSyntheticClaims(t *testing.T) {
	payload := auth.CookiePayloadV2{
		Version:        auth.CookieVersionV2,
		AccessToken:    "at_test",
		Subject:        "sub_sa_001",
		Email:          "sa@example.com",
		FirstName:      "Service",
		LastName:       "Account",
		Roles:          []string{"admin", "member"},
		ActorUserID:    "actor_001",
		ActorEmail:     "actor@example.com",
		ActorFirstName: "Alice",
		ActorLastName:  "Admin",
		Reason:         "debugging",
		UserType:       auth.ImpersonatedUserTypeServiceAccount,
		Impersonated:   true,
		ExpiresAt:      time.Now().Add(time.Hour),
	}

	adapter := &mockImpersonationAdapter{payload: payload}
	resolver := OIDCImpersonationResolver(adapter)

	sess, _, err := resolver(context.Background(), http.Header{}, "test-tenant")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess == nil {
		t.Fatal("expected session, got nil")
	}

	if sess.Subject != payload.Subject {
		t.Errorf("Subject: got %q, want %q", sess.Subject, payload.Subject)
	}
	if sess.Email != payload.Email {
		t.Errorf("Email: got %q, want %q", sess.Email, payload.Email)
	}
	if sess.FirstName != payload.FirstName {
		t.Errorf("FirstName: got %q, want %q", sess.FirstName, payload.FirstName)
	}
	if sess.LastName != payload.LastName {
		t.Errorf("LastName: got %q, want %q", sess.LastName, payload.LastName)
	}
	if len(sess.Roles) != 2 {
		t.Errorf("Roles: got %v, want 2 roles", sess.Roles)
	}
	if !sess.IsImpersonating {
		t.Error("IsImpersonating should be true")
	}
	if sess.ActorUserID != payload.ActorUserID {
		t.Errorf("ActorUserID: got %q, want %q", sess.ActorUserID, payload.ActorUserID)
	}
}
