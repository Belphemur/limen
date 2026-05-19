// Command limen is the all-in-one Limen binary entry point. Mounts the
// union of every route the split binaries (cmd/gateway, cmd/portal,
// cmd/staff) collectively serve. All real work lives in internal/cli
// (Cobra subcommand tree).
package main

import "github.com/belphemur/limen/internal/cli"

func main() {
	cli.Execute()
}
