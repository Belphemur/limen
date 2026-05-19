// Command limenctl is the Limen admin / day-2 operator CLI. Exposes
// migrate / create-tenant / create-upstream — no `serve` subcommand
// (use cmd/limen for that, or one of the split service binaries
// cmd/gateway, cmd/portal, cmd/staff).
package main

import "github.com/belphemur/limen/internal/cli"

func main() {
	cli.ExecuteAdmin()
}
