// Package oidcboot constructs the portal-facing OIDC relying party.
// Sibling of boot so binaries that don't host the portal (cmd/gateway)
// never link the OIDC RP code.
package oidcboot

import (
	"fmt"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/boot"
)

// Build returns the OIDC handler. Requires rt.Cipher + rt.Signer to be
// populated (i.e. boot.NeedCipher + NeedSigner in the profile).
func Build(rt *boot.Runtime) (*auth.OIDC, error) {
	if rt.Cipher == nil || rt.Signer == nil {
		return nil, fmt.Errorf("oidcboot: requires NeedCipher + NeedSigner in BootProfile")
	}
	h, err := auth.NewOIDC(rt.Ctx, auth.OIDCConfig{
		Issuer:              rt.Cfg.OIDC.Issuer,
		ClientID:            rt.Cfg.OIDC.ClientID,
		ClientSecret:        rt.Cfg.OIDC.ClientSecret,
		RedirectURI:         rt.Cfg.OIDC.RedirectURI,
		AllowedRedirectURIs: rt.Cfg.OIDC.AllowedRedirectURIs,
		Scopes:              rt.Cfg.OIDC.Scopes,
		Secure:              rt.Cfg.Security.PortalSessionCookieSecure,
	}, rt.Cipher, rt.Signer, rt.Logger)
	if err != nil {
		return nil, fmt.Errorf("build oidc handler: %w", err)
	}
	return h, nil
}
