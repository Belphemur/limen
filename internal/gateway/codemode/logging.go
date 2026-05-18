package codemode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func approxResultBytes(v any) int {
	if v == nil {
		return 0
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// classifyToolError maps a Dispatcher.CallTool error into (error_kind,
// outcome) tags for structured logs. Message text is redacted before it
// goes into logs; only the kind is reported in the clear.
func classifyToolError(err error) (kind string, outcome string) {
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "needs re-link"), strings.Contains(low, "needs_relink"):
		return "needs_relink", "denied_no_link"
	case strings.Contains(low, "link not found"), strings.Contains(low, "no link"):
		return "no_link", "denied_no_link"
	case strings.Contains(low, "auto_disabled"), strings.Contains(low, "auto-disabled"):
		return "auto_disabled", "denied_auto_disabled"
	case strings.Contains(low, "upstream_unavailable"), strings.Contains(low, "breaker"):
		return "upstream_unavailable", "upstream_error"
	default:
		return "upstream_error", "upstream_error"
	}
}

// redactSecrets scrubs anything that looks like a credential from a
// string before it lands in a log line. Applied at every codemode log
// call site that emits user-derived content (error messages, JS
// exception strings).
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, r := range secretREs {
		out = r.re.ReplaceAllString(out, r.repl)
	}
	return out
}

type secretRE struct {
	re   *regexp.Regexp
	repl string
}

var secretREs = func() []secretRE {
	patterns := []struct {
		pattern string
		repl    string
	}{
		{`(?i)authorization:\s*[^\s,;]+`, "authorization: [REDACTED]"},
		{`(?i)bearer\s+[A-Za-z0-9._\-]+`, "Bearer [REDACTED]"},
		{`(?i)set-cookie:\s*[^\r\n]+`, "set-cookie: [REDACTED]"},
		{`(?i)cookie:\s*[^\r\n]+`, "cookie: [REDACTED]"},
		{`(?i)"(access_token|refresh_token|api_key|client_secret|password)"\s*:\s*"[^"]*"`, `"$1":"[REDACTED]"`},
		{`(?i)\b(access_token|refresh_token|api_key|client_secret|password)=([^&\s"]+)`, `$1=[REDACTED]`},
	}
	out := make([]secretRE, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, secretRE{regexp.MustCompile(p.pattern), p.repl})
	}
	return out
}()
