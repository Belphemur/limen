// Package boot is the shared runtime boot floor used by every Limen
// service / CLI binary.
//
// BootRuntime constructs only the dependencies selected by a
// BootProfile bitmask. The package's imports are intentionally narrow:
// it does NOT import internal/zitadel or internal/oauthproxy, so the
// MCP gateway binary (cmd/gateway) — which must not reach the Zitadel
// management API or DCR surfaces — can depend on it freely.
//
// Constructors that DO need zitadel / OIDC RP / oauthproxy live in
// sibling packages (e.g. internal/boot/zitadelboot, the per-binary
// serve packages under internal/boot/*) and are wired in by the
// binaries that need them.
package boot

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

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
)

// BootProfile is a bitmask describing which optional dependencies a
// binary needs from BootRuntime. The base floor (config, logger,
// signal-cancellable context) is always built. Zitadel + OIDC RP are
// NOT covered by BootProfile — binaries that need them call
// zitadelboot / oidcboot helpers explicitly.
type BootProfile uint32

const (
	// NeedStore opens the Postgres pools (limen_app + limen_admin).
	NeedStore BootProfile = 1 << iota
	// NeedCipher parses the token encryption key and registers the
	// AES-SIV cipher globally so SecretField encrypt/decrypt picks it
	// up.
	NeedCipher
	// NeedSigner builds the HMAC state signer used for portal cookies
	// and OAuth state. Requires NeedCipher (shared key material).
	NeedSigner
	// NeedUpstream builds the upstream strategy registry, Valkey-backed
	// OAuth state store, upstream.Service, and launches the background
	// refresher goroutine. Requires NeedStore + NeedCipher.
	NeedUpstream
)

// AllProfiles is the union profile used by binaries that mount every
// suite (cmd/limen all-in-one). New BootProfile flags must be folded
// into this constant.
const AllProfiles = NeedStore | NeedCipher | NeedSigner | NeedUpstream

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
	UpstreamService  *upstream.Service
	UpstreamRegistry *upstream.Registry
	Valkey           valkey.Client

	// cleanups runs in reverse order of registration.
	cleanups []func()
}

// AddCleanup registers a teardown function. Cleanups run in reverse
// order of registration when the cleanup func returned by BootRuntime
// is invoked.
func (r *Runtime) AddCleanup(fn func()) {
	r.cleanups = append(r.cleanups, fn)
}

// Logger-level config is loaded from cfg.Logging.

// BootRuntime loads config, builds the logger and a signal-cancellable
// context, then constructs each dependency selected by want. The
// returned cleanup func tears everything down in reverse order;
// callers must invoke it on shutdown. Errors during boot trigger
// partial cleanup before returning.
func BootRuntime(configPath string, want BootProfile) (*Runtime, func(), error) {
	if configPath == "" {
		return nil, func() {}, fmt.Errorf("config path is required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, func() {}, fmt.Errorf("load config %q: %w", configPath, err)
	}
	logger, err := BuildLogger(cfg.Logging.Level, cfg.Logging.Development)
	if err != nil {
		return nil, func() {}, fmt.Errorf("build logger: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	rt := &Runtime{Ctx: ctx, Cancel: cancel, Cfg: cfg, Logger: logger}
	rt.AddCleanup(cancel)
	rt.AddCleanup(func() { _ = logger.Sync() })

	cleanup := func() {
		for i := len(rt.cleanups) - 1; i >= 0; i-- {
			rt.cleanups[i]()
		}
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
		rt.AddCleanup(func() { _ = store.Close() })

		if err := store.CheckSchemaVersion(ctx); err != nil {
			cleanup()
			return nil, func() {}, err
		}
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

// BuildLogger constructs a zap logger using the standard zapcore level
// names. Empty level falls back to "info". When development is true the
// human-readable development encoder is used; otherwise JSON
// production.
func BuildLogger(level string, development bool) (*zap.Logger, error) {
	var cfg zap.Config
	if development {
		cfg = zap.NewDevelopmentConfig()
	} else {
		cfg = zap.NewProductionConfig()
	}
	if level == "" {
		level = "info"
	}
	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("parse log level %q: %w", level, err)
	}
	cfg.Level = zap.NewAtomicLevelAt(lvl)
	return cfg.Build()
}

// bootUpstream builds the strategy registry + service + refresher when
// Valkey is configured. When valkey.address is empty, the upstream
// suite is disabled with a warn log.
func bootUpstream(rt *Runtime) error {
	if rt.Cfg.Valkey.Address == "" {
		rt.Logger.Warn("valkey.address empty: upstream linking disabled")
		return nil
	}
	vk, err := valkey.Open(rt.Cfg.Valkey)
	if err != nil {
		return fmt.Errorf("open valkey: %w", err)
	}
	rt.Valkey = vk
	rt.AddCleanup(vk.Close)

	stateStore := oauthstate.New(vk, rt.Cipher, oauthstate.DefaultTTL)

	registry := upstream.NewRegistry()
	registry.Register(none.New(nil))
	registry.Register(statichdr.New(rt.Store, rt.Cipher, nil))

	mcpStrat, err := mcpspec.New(rt.Store, rt.Cipher, stateStore, mcpspec.Options{
		RedirectURLFn: func(tenantPublic, upstreamPublicID string) string {
			return rt.Cfg.Server.BaseURL + "/t/" + tenantPublic + rt.Cfg.Server.UpstreamCallbackPath + "/" + upstreamPublicID + "/callback"
		},
		ProactiveWindow: rt.Cfg.UpstreamRefresh.ProactiveWindow,
		SoftwareID:      "limen-gateway",
	})
	if err != nil {
		return fmt.Errorf("build mcpspec strategy: %w", err)
	}
	registry.Register(mcpStrat)

	rt.UpstreamService = upstream.NewService(rt.Store, registry, rt.Logger)
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
