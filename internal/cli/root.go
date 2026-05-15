// Package cli builds the Limen command tree on top of Cobra + Viper.
//
// Commands:
//
//	limen serve         — runs the HTTP server (default for cmd/gateway)
//	limen create-tenant — provisions a Zitadel org + Limen tenant row + seed owner
//	limen migrate       — runs storage AutoMigrate + the embedded SQL migrations
//
// Day-2 user management (invites, role changes, password resets, MFA,
// IdP federation) is delegated to the Zitadel Console — see Phase 4's
// _Self-service delegation_ table. The CLI is intentionally minimal.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/config"
)

// rootFlags carries flag values shared by every subcommand.
type rootFlags struct {
	configPath string
}

// NewRootCommand builds the root cobra.Command. Subcommands receive a
// pointer to the same rootFlags so they can lazily load config when
// invoked. A nop logger is replaced by a real one inside each subcommand.
func NewRootCommand() *cobra.Command {
	flags := &rootFlags{}

	root := &cobra.Command{
		Use:           "limen",
		Short:         "Limen — multi-tenant MCP gateway",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&flags.configPath, "config", "config.yaml", "path to config file (also: LIMEN_CONFIG)")

	v := viper.New()
	v.SetEnvPrefix("LIMEN")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()
	_ = v.BindPFlag("config", root.PersistentFlags().Lookup("config"))

	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if v.IsSet("config") {
			flags.configPath = v.GetString("config")
		}
		return nil
	}

	root.AddCommand(newServeCommand(flags, v))
	root.AddCommand(newCreateTenantCommand(flags, v))
	root.AddCommand(newCreateUpstreamCommand(flags, v))
	root.AddCommand(newMigrateCommand(flags, v))

	return root
}

// Execute runs the root command and exits with a non-zero status on
// failure, printing the error to stderr. Used by cmd/gateway/main.go.
func Execute() {
	if err := NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// loadConfig parses the YAML file at flags.configPath.
func loadConfig(flags *rootFlags) (*config.Config, error) {
	if flags.configPath == "" {
		return nil, fmt.Errorf("--config is required")
	}
	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return nil, fmt.Errorf("load config %q: %w", flags.configPath, err)
	}
	return cfg, nil
}

// newCLILogger returns a stderr-only dev logger. The serve command builds
// its own production logger; the rest log to stderr so stdout stays clean
// for any structured output a subcommand emits.
func newCLILogger() *zap.Logger {
	cfg := zap.NewDevelopmentConfig()
	cfg.OutputPaths = []string{"stderr"}
	cfg.ErrorOutputPaths = []string{"stderr"}
	logger, err := cfg.Build()
	if err != nil {
		return zap.NewNop()
	}
	return logger
}
