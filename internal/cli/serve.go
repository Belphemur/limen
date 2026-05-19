// Package cli — `limen serve` (all-in-one) entry path.
//
// The actual server-boot logic lives in internal/boot/serveall; this
// file only adapts the cobra subcommand surface.
package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/belphemur/limen/internal/boot/serveall"
)

func newServeCommand(flags *rootFlags, _ *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the Limen HTTP server (all-in-one)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return serveall.Run(flags.configPath)
		},
	}
}
