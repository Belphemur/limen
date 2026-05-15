package cli

import (
	"context"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
)

// serverDeps bundles the cross-suite wiring built early in runServe so
// the per-suite mount helpers don't each grow a long parameter list.
type serverDeps struct {
	ctx    context.Context
	cfg    *config.Config
	logger *zap.Logger

	cipher *crypto.Cipher
	signer *auth.StateSigner
	store  *storage.Store
	oidc   *auth.OIDC
}
