// Command observer is the metrics consumer Limen binary. It bootstraps a
// background metrics consumer and serves /healthz + /readyz.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/belphemur/limen/internal/boot/serveobserver"
)

func main() {
	cfg := flag.String("config", "config.yaml", "path to config file (also: LIMEN_CONFIG)")
	flag.Parse()
	if env := os.Getenv("LIMEN_CONFIG"); env != "" {
		*cfg = env
	}
	if err := serveobserver.Run(*cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
