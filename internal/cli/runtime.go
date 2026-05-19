// Package cli — shared runtime boot floor.
//
// BootRuntime is the single entry point every service / CLI binary uses
// to bring up the dependencies it needs. A BootProfile bitmask selects
// which dependencies to construct; unset fields on Runtime are nil. The
// returned cleanup func releases resources in reverse order.
//
// The boot floor is intentionally small: config + logger + ctx are
// always built, and crypto/storage/signer/Zitadel/OIDC RP/upstream
// linking are opt-in. Per-suite mount helpers (mountPortal,
// mountMCPResource, mountOAuthProxy, mountUpstreamLinking) consume
// Runtime fields directly.
package cli

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/mcpspec"
	"github.com/belphemur/limen/internal/upstream/none"
	"github.com/belphemur/limen/internal/upstream/oauthstate"
	"github.com/belphemur/limen/internal/upstream/statichdr"
	"github.com/belphemur/limen/internal/valkey"
	"github.com/belphemur/limen/internal/zitadel"
)

// BootProfile is a bitmask describing which optional dependencies a
// binary needs from BootRuntime. The base floor (config, logger,
// signal-cancellable context) is always built.
type BootProfile uint32

const (
	// NeedStore opens the Postgres pools (limen_app + limen_admin).
	NeedStore BootProfile = 1 << iota
	// NeedCipher parses the token encryption key and registers the
	// AES-SIV cipher globally so SecretField encrypt/decrypt picks it
	// up.
	NeedCipher
	// NeedSigner builds the HMAC state signer used for portal cookies
	// and OAuth state.
	NeedSigner
	// NeedZitadel builds the Zitadel management client. Required by
	// the OAuth proxy (DCR) and the portal/staff admin surfaces;
	// explicitly NOT built by the MCP gateway binary.
	NeedZitadel
	// NeedOIDCRP builds the portal-facing OIDC relying party. Implies
	// NeedCipher and NeedSigner.
	NeedOIDCRP
	// NeedUpstream builds the upstream strategy registry, Valkey-backed
	// OAuth state store, upstream.Service, and launches the background
	// refresher goroutine.
	NeedUpstream
)

// AllProfiles is the union profile used by the all-in-one binary
// (cmd/limen). New BootProfile flags must be folded into this constant.
const AllProfiles = NeedStore | NeedCipher | NeedSigner | NeedZitadel | NeedOIDCRP | NeedUpstream

// Has reports whether p has every bit in f set.
func (p BootProfile) Has(f BootProfile) bool { return p&f == f }

// Runtime carries the shared runtime services BootRuntime constructed,
// keyed by the BootProfile passed in. Fields are nil when the
// corresponding profile bit was unset.
type Runtime struct {
	Ctx    context.Context
	Cancel context.CancelFunc
	Cfg    *config.Config
	Logger *zap.Logger

	Cipher           *crypto.Cipher
	Store            *storage.Store
	Signer           *auth.StateSigner
	Zitadel          *zitadel.Client
	OIDC             *auth.OIDC
	UpstreamService  *upstream.Service
	UpstreamRegistry *upstream.Registry

	// cleanups runs in reverse order of registration.
	cleanups []func()
}

func (r *Runtime) addCleanup(fn func()) {
	r.cleanups = append(r.cleanups, fn)
}

// BootRuntime loads config, builds the logger and a signal-cancellable
// context, then constructs each dependency selected by want. The
// returned cleanup func tears everything down in reverse order; callers
// must invoke it on shutdown. Errors during boot trigger partial
// cleanup before returning.
func BootRuntime(flags *rootFlags, want BootProfile) (*Runtime, func(), error) {
	cfg, err := loadConfig(flags)
	if err != nil {
		return nil, func() {}, err
	}
	logger, err := buildServeLogger(cfg.Logging.Level, cfg.Logging.Development)
	if err != nil {
		return nil, func() {}, fmt.Errorf("build logger: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	rt := &Runtime{Ctx: ctx, Cancel: cancel, Cfg: cfg, Logger: logger}
	rt.addCleanup(cancel)
	rt.addCleanup(func() { _ = logger.Sync() })

	cleanup := func() {
		// Reverse-order teardown.
		for i := len(rt.cleanups) - 1; i >= 0; i-- {
			rt.cleanups[i]()
		}
	}

	// OIDC RP requires cipher + signer; promote the implied bits.
	if want.Has(NeedOIDCRP) {
		want |= NeedCipher | NeedSigner
	}

	if want.Has(NeedCipher) {
		key, err := crypto.ParseKey(cfg.Security.TokenEncryptionKey)
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("parse token encryption key: %w", err)
		}
		cipher, err := crypto.NewCipher(key)
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("build cipher: %w", err)
		}
		crypto.SetCipher(cipher)
		rt.Cipher = cipher

		if want.Has(NeedSigner) {
			signer, err := auth.NewStateSigner(key[:])
			if err != nil {
				cleanup()
				return nil, func() {}, fmt.Errorf("build state signer: %w", err)
			}
			rt.Signer = signer
		}
	} else if want.Has(NeedSigner) {
		cleanup()
		return nil, func() {}, fmt.Errorf("NeedSigner requires NeedCipher (key material is shared)")
	}

	if want.Has(NeedStore) {
		store, err := storage.Open(cfg.Database)
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("open storage: %w", err)
		}
		rt.Store = store
		rt.addCleanup(func() { _ = store.Close() })
	}

	if want.Has(NeedZitadel) {
		zclient, err := zitadel.NewClient(ctx, zitadel.Config{
			Domain:      cfg.Zitadel.Domain,
			AuthMode:    zitadel.AuthMode(cfg.Zitadel.AuthMode),
			PAT:         cfg.Zitadel.PAT,
			JWTKeyPath:  cfg.Zitadel.JWTKeyPath,
			ProjectID:   cfg.Zitadel.ProjectID,
			HTTPTimeout: cfg.Zitadel.HTTPTimeout,
		})
		if err != nil {
			// Best-effort: log and continue. Matches prior behavior
			// of mountOAuthProxy, which silently skipped DCR when
			// the admin client couldn't be built. Callers that hard
			// require Zitadel can check rt.Zitadel == nil.
			logger.Warn("zitadel admin client unavailable", zap.Error(err))
		} else {
			rt.Zitadel = zclient
		}
	}

	if want.Has(NeedOIDCRP) {
		oidcHandler, err := auth.NewOIDC(ctx, auth.OIDCConfig{
			Issuer:       cfg.OIDC.Issuer,
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			RedirectURI:  cfg.OIDC.RedirectURI,
			Scopes:       cfg.OIDC.Scopes,
			Secure:       cfg.Security.PortalSessionCookieSecure,
		}, rt.Cipher, rt.Signer, logger)
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("build oidc handler: %w", err)
		}
		rt.OIDC = oidcHandler
	}

	if want.Has(NeedUpstream) {
		if !want.Has(NeedStore) || !want.Has(NeedCipher) {
			cleanup()
			return nil, func() {}, fmt.Errorf("NeedUpstream requires NeedStore + NeedCipher")
		}
		if err := bootUpstream(rt); err != nil {
			cleanup()
			return nil, func() {}, err
		}
	}

	return rt, cleanup, nil
}

// bootUpstream builds the strategy registry + service + refresher when
// Valkey is configured. When valkey.address is empty, the upstream
// suite is disabled with a warn log (matches prior setupUpstreamLinking
// behavior).
func bootUpstream(rt *Runtime) error {
	if rt.Cfg.Valkey.Address == "" {
		rt.Logger.Warn("valkey.address empty: upstream linking disabled")
		return nil
	}
	vk, err := valkey.Open(rt.Cfg.Valkey)
	if err != nil {
		return fmt.Errorf("open valkey: %w", err)
	}
	rt.addCleanup(vk.Close)

	stateStore := oauthstate.New(vk, rt.Cipher, oauthstate.DefaultTTL)

	registry := upstream.NewRegistry()
	registry.Register(none.New(nil))
	registry.Register(statichdr.New(rt.Store, rt.Cipher, nil))

	mcpStrat, err := mcpspec.New(rt.Store, rt.Cipher, stateStore, mcpspec.Options{
		RedirectURLFn: func(tenantPublic, upstreamName string) string {
			return rt.Cfg.Server.BaseURL + "/t/" + tenantPublic + "/upstream/" + upstreamName + "/callback"
		},
		ProactiveWindow: rt.Cfg.UpstreamRefresh.ProactiveWindow,
		SoftwareID:      "limen-gateway",
	})
	if err != nil {
		return fmt.Errorf("build mcpspec strategy: %w", err)
	}
	registry.Register(mcpStrat)

	rt.UpstreamService = upstream.NewService(rt.Store, registry)
	rt.UpstreamRegistry = registry

	refresher := upstream.NewRefresher(rt.Store, registry, upstream.RefresherOptions{
		Interval:      rt.Cfg.UpstreamRefresh.Interval,
		RefreshWindow: rt.Cfg.UpstreamRefresh.RefreshWindow,
		HealthThresholds: upstream.HealthThresholds{
			FailThreshold:     rt.Cfg.UpstreamRefresh.FailThreshold,
			FailWindow:        rt.Cfg.UpstreamRefresh.FailWindow,
			NeedsRelinkWindow: rt.Cfg.UpstreamRefresh.NeedsRelinkWindow,
		},
		CatalogInterval: rt.Cfg.UpstreamRefresh.CatalogInterval,
		Logger:          rt.Logger,
	})
	go refresher.Run(rt.Ctx)
	rt.Logger.Info("upstream refresher started",
		zap.Duration("interval", rt.Cfg.UpstreamRefresh.Interval),
		zap.Duration("refresh_window", rt.Cfg.UpstreamRefresh.RefreshWindow),
		zap.Duration("catalog_interval", rt.Cfg.UpstreamRefresh.CatalogInterval))
	return nil
}
