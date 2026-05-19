// Command portal is the Limen portal binary. Mounts the customer +
// admin Connect-RPC surfaces (Phase 9b/9c land the actual services),
// the OIDC RP under /t/{tenant}/auth/*, the OAuth proxy under
// /t/{tenant}/oauth/* (DCR + AS metadata + redirector), and the
// upstream OAuth callback. Holds the most privileged secrets in the
// system (the Zitadel management credential and the portal-session
// cipher key).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/belphemur/limen/internal/boot/serveportal"
)

func main() {
	cfg := flag.String("config", "config.yaml", "path to config file (also: LIMEN_CONFIG)")
	flag.Parse()
	if env := os.Getenv("LIMEN_CONFIG"); env != "" {
		*cfg = env
	}
	if err := serveportal.Run(*cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
