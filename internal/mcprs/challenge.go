package mcprs

import (
	"fmt"
	"net/http"
	"strings"
)

// Standard RFC 6750 error codes surfaced in the WWW-Authenticate challenge.
const (
	ErrInvalidRequest    = "invalid_request"
	ErrInvalidToken      = "invalid_token"
	ErrInsufficientScope = "insufficient_scope"
	ErrCrossTenantDenied = "cross_tenant_denied" // non-standard but informative
)

// WWWAuthenticate builds the value of the WWW-Authenticate header for
// MCP RS responses, per RFC 6750 §3 + RFC 9728 §5.1. resourceMetadataURL
// points clients at the PRM document so they can discover the AS without
// any prior knowledge of Limen's deployment layout.
//
// errCode and errDesc are optional; when empty the corresponding
// parameter is omitted. Values are quoted with %q so embedded quotes are
// escaped per token68 rules.
func WWWAuthenticate(resourceMetadataURL, errCode, errDesc string) string {
	var b strings.Builder
	b.WriteString(`Bearer realm="mcp"`)
	if resourceMetadataURL != "" {
		fmt.Fprintf(&b, `, resource_metadata=%q`, resourceMetadataURL)
	}
	if errCode != "" {
		fmt.Fprintf(&b, `, error=%q`, errCode)
	}
	if errDesc != "" {
		fmt.Fprintf(&b, `, error_description=%q`, errDesc)
	}
	return b.String()
}

// WriteChallenge writes a 401/403 response carrying the WWW-Authenticate
// challenge and a minimal JSON error body. The body intentionally mirrors
// the header so well-behaved clients have the same information whether
// they parse the header or the body.
func WriteChallenge(w http.ResponseWriter, status int, resourceMetadataURL, errCode, errDesc string) {
	w.Header().Set("WWW-Authenticate", WWWAuthenticate(resourceMetadataURL, errCode, errDesc))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q,"error_description":%q}`+"\n", errCode, errDesc)
}
