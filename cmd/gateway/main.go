// Command gateway is Limen's binary entry point. All real work lives in
// internal/cli (Cobra subcommand tree).
package main

import "github.com/belphemur/limen/internal/cli"

func main() {
	cli.Execute()
}
