// Package zitadel is a thin domain wrapper around the official Zitadel Go
// SDK (github.com/zitadel/zitadel-go/v3). It hides the generated protobuf
// surface behind a small Limen-shaped API:
//
//   - Sessions   — CreateSession / GetSession / DeleteSession (used by
//     internal/auth to back the portal cookie).
//   - Users      — AddHumanUser / AddUserGrant / CreateInviteCode /
//     ListUserGrants (used by internal/cli and internal/portal to invite
//     tenant members and enforce the "at least one owner" invariant).
//   - Orgs       — CreateOrganization (used by internal/cli to bind a new
//     Limen tenant to a fresh Zitadel organization).
//
// Callers depend on this package, not on the generated types. That keeps
// the Zitadel coupling in one file and lets us swap SDK versions without
// rewriting every call site.
package zitadel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	zsdk "github.com/zitadel/zitadel-go/v3/pkg/client"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"
)

// AuthMode selects how the API client authenticates against Zitadel.
type AuthMode string

const (
	// AuthModePAT uses a Personal Access Token. Dev / testing only.
	AuthModePAT AuthMode = "pat"
	// AuthModeJWTKey uses a service-user JWT-profile private key file.
	// Recommended for production.
	AuthModeJWTKey AuthMode = "jwt_key"
)

// Config holds connection parameters for a Zitadel instance.
type Config struct {
	// Domain is the Zitadel issuer URL — e.g. "http://localhost:8081" in
	// dev or "https://auth.limen.example.com" in production. The SDK derives
	// the gRPC + REST endpoints from this.
	Domain string

	// AuthMode selects PAT vs JWT-profile authentication. Required.
	AuthMode AuthMode

	// PAT is the bearer token used when AuthMode == AuthModePAT.
	PAT string

	// JWTKeyPath is the filesystem path to a service-user JSON key file,
	// used when AuthMode == AuthModeJWTKey.
	JWTKeyPath string

	// ProjectID is the shared Limen project's resource owner — required for
	// AddUserGrant and CreateInviteCode and for requesting the project-roles
	// claim on portal tokens.
	ProjectID string

	// HTTPTimeout caps any single API request. Default 30s.
	HTTPTimeout time.Duration
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Domain) == "" {
		return errors.New("zitadel: Domain is required")
	}
	if strings.TrimSpace(c.ProjectID) == "" {
		return errors.New("zitadel: ProjectID is required")
	}
	switch c.AuthMode {
	case AuthModePAT:
		if strings.TrimSpace(c.PAT) == "" {
			return errors.New("zitadel: PAT is required when AuthMode=pat")
		}
	case AuthModeJWTKey:
		if strings.TrimSpace(c.JWTKeyPath) == "" {
			return errors.New("zitadel: JWTKeyPath is required when AuthMode=jwt_key")
		}
	default:
		return fmt.Errorf("zitadel: unknown AuthMode %q (want pat|jwt_key)", c.AuthMode)
	}
	return nil
}

// Client is the SDK-backed API handle. Build once at startup, share across
// the process. Goroutine-safe.
type Client struct {
	api       *zsdk.Client
	projectID string
}

// NewClient constructs the underlying SDK client and validates the config.
// It does not perform a network call — credentials are exercised lazily
// on the first API request.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 30 * time.Second
	}

	var authInit zsdk.TokenSourceInitializer
	switch cfg.AuthMode {
	case AuthModePAT:
		authInit = zsdk.PAT(cfg.PAT)
	case AuthModeJWTKey:
		authInit = zsdk.DefaultServiceUserAuthentication(
			cfg.JWTKeyPath,
			oidc.ScopeOpenID,
			zsdk.ScopeZitadelAPI(),
		)
	}

	api, err := zsdk.New(ctx, zitadel.New(cfg.Domain), zsdk.WithAuth(authInit))
	if err != nil {
		return nil, fmt.Errorf("zitadel: build SDK client: %w", err)
	}
	return &Client{api: api, projectID: cfg.ProjectID}, nil
}

// API exposes the raw SDK handle for callers that genuinely need a service
// not yet wrapped by this package. Prefer the typed helpers in sessions.go,
// users.go, orgs.go.
func (c *Client) API() *zsdk.Client { return c.api }

// ProjectID returns the configured Limen project id.
func (c *Client) ProjectID() string { return c.projectID }
