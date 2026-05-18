package codemode

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyToolError(t *testing.T) {
	cases := []struct {
		msg         string
		wantKind    string
		wantOutcome string
	}{
		{"upstream: link needs re-link", "needs_relink", "denied_no_link"},
		{"needs_relink for github", "needs_relink", "denied_no_link"},
		{"link not found", "no_link", "denied_no_link"},
		{"no link for tenant", "no_link", "denied_no_link"},
		{"upstream auto_disabled at 5xx threshold", "auto_disabled", "denied_auto_disabled"},
		{"upstream auto-disabled", "auto_disabled", "denied_auto_disabled"},
		{"upstream_unavailable: breaker open", "upstream_unavailable", "upstream_error"},
		{"breaker tripped", "upstream_unavailable", "upstream_error"},
		{"random 500 from upstream", "upstream_error", "upstream_error"},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			kind, outcome := classifyToolError(errors.New(tc.msg))
			if kind != tc.wantKind || outcome != tc.wantOutcome {
				t.Errorf("got (%q, %q), want (%q, %q)", kind, outcome, tc.wantKind, tc.wantOutcome)
			}
		})
	}
}

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		in       string
		mustNot  string
		contains string
	}{
		{"Authorization: abc.def.ghi", "abc.def.ghi", "[REDACTED]"},
		{"Bearer eyJhbGciOi.payload.sig", "eyJhbGciOi", "[REDACTED]"},
		{"Cookie: session=abc123", "abc123", "[REDACTED]"},
		{"Set-Cookie: x=y; Path=/", "x=y", "[REDACTED]"},
		{`{"access_token":"abc","x":1}`, `"abc"`, "[REDACTED]"},
		{`{"refresh_token":"xyz"}`, `"xyz"`, "[REDACTED]"},
		{`{"api_key":"k"}`, `"k"`, "[REDACTED]"},
		{`{"client_secret":"s"}`, `"s"`, "[REDACTED]"},
		{`{"password":"p"}`, `"p"`, "[REDACTED]"},
		{"access_token=abc123&q=1", "abc123", "[REDACTED]"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			out := redactSecrets(tc.in)
			if tc.mustNot != "" && strings.Contains(out, tc.mustNot) {
				t.Errorf("expected %q to be scrubbed from %q, got %q", tc.mustNot, tc.in, out)
			}
			if !strings.Contains(out, tc.contains) {
				t.Errorf("expected %q in %q", tc.contains, out)
			}
		})
	}
}
