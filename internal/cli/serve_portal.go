// Package cli — portal suite.
//
// Owns the bootstrap that everything else depends on (cipher, storage,
// HMAC state signer, OIDC RP) plus the actual portal SPA + auth routes
// under /t/{tenant}/portal and /t/{tenant}/auth/*.
package cli

import (
	"fmt"

	"github.com/go-chi/chi/v5"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/transport"
)

// setupPortal parses the token encryption key, builds the AES-SIV cipher
// (registered globally so SecretField encrypt/decrypt picks it up),
// opens storage against both pools, builds the HMAC state signer, and
// constructs the portal's OIDC relying-party handler.
//
// Caller owns the returned store's lifetime — defer Close() on exit.
func setupPortal(d *serverDeps, cfg *config.Config) (*crypto.Cipher, *storage.Store, *auth.StateSigner, *auth.OIDC, error) {
	key, err := crypto.ParseKey(cfg.Security.TokenEncryptionKey)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("parse token encryption key: %w", err)
	}
	cipher, err := crypto.NewCipher(key)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("build cipher: %w", err)
	}
	crypto.SetCipher(cipher)

	store, err := storage.Open(cfg.Database)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open storage: %w", err)
	}
	signer, err := auth.NewStateSigner(key[:])
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, nil, fmt.Errorf("build state signer: %w", err)
	}
	oidcHandler, err := auth.NewOIDC(d.ctx, auth.OIDCConfig{
		Issuer:       cfg.OIDC.Issuer,
		ClientID:     cfg.OIDC.ClientID,
		ClientSecret: cfg.OIDC.ClientSecret,
		RedirectURI:  cfg.OIDC.RedirectURI,
		Scopes:       cfg.OIDC.Scopes,
		Secure:       cfg.Security.PortalSessionCookieSecure,
	}, cipher, signer, d.logger)
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, nil, fmt.Errorf("build oidc handler: %w", err)
	}
	return cipher, store, signer, oidcHandler, nil
}

// mountPortal attaches the portal SPA + OIDC auth routes.
func mountPortal(r chi.Router, d *serverDeps) {
	transport.MountPortal(r, transport.PortalDeps{
		Store:                 d.store,
		OIDC:                  d.oidc,
		Logger:                d.logger,
		PostLogoutRedirectURI: d.cfg.OIDC.PostLogoutRedirectURI,
		UpstreamService:       d.upstreamService,
	})
}
