// Command staff is the Limen staff-backoffice binary. Phase 9a ships
// only the scaffold (health endpoints + 404 for everything else); the
// actual staff routes land in Phase 12.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/belphemur/limen/internal/boot/servestaff"
)

func main() {
	cfg := flag.String("config", "config.yaml", "path to config file (also: LIMEN_CONFIG)")
	flag.Parse()
	if env := os.Getenv("LIMEN_CONFIG"); env != "" {
		*cfg = env
	}
	if err := servestaff.Run(*cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
