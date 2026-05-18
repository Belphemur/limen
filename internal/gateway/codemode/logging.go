package codemode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// approxResultBytesCap bounds how much CPU/RAM approxResultBytes is
// willing to spend marshalling a result purely for a log field. Past
// this size we stop counting and emit -1, signalling "very large" to
// log consumers without double-marshalling multi-MB payloads.
const approxResultBytesCap = 256 * 1024

// approxResultBytes returns the JSON-encoded length of v, capped at
// approxResultBytesCap. Returns -1 for values larger than the cap or
// that fail to encode. The cap matters because the MCP transport
// marshals the same value again at egress; we don't need to pay for
// the full encoding twice on huge results just to populate a log
// field.
func approxResultBytes(v any) int {
	if v == nil {
		return 0
	}
	w := &cappedCountWriter{limit: approxResultBytesCap}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		if errors.Is(err, errCappedWriterFull) {
			return -1
		}
		return -1
	}
	return w.n
}

var errCappedWriterFull = errors.New("codemode: result size exceeds approx cap")

// cappedCountWriter counts bytes written until it exceeds limit, then
// short-circuits with errCappedWriterFull so json.Encoder stops
// serializing the rest of the value.
type cappedCountWriter struct {
	n     int
	limit int
}

func (w *cappedCountWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	if w.n > w.limit {
		return 0, errCappedWriterFull
	}
	return len(p), nil
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
