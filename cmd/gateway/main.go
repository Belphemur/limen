// Command gateway is the MCP RS hot-path Limen binary. Mounts ONLY
// /t/{tenant}/mcp/* + /healthz + /readyz. Holds no portal session
// cipher key and no Zitadel admin credential, and its transitive
// import graph excludes internal/oauthproxy and internal/zitadel
// (enforced by the import-graph test in this directory).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/belphemur/limen/internal/boot/servegateway"
)

func main() {
	cfg := flag.String("config", "config.yaml", "path to config file (also: LIMEN_CONFIG)")
	flag.Parse()
	if env := os.Getenv("LIMEN_CONFIG"); env != "" {
		*cfg = env
	}
	if err := servegateway.Run(*cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
