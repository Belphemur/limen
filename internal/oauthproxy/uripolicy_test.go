//go:build integration

package oauthproxy

import (
	"strings"
	"testing"
)

func TestValidateRedirectURI_Floor(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		wantErr string // substring; "" means must succeed
	}{
		// happy paths
		{"https exact", "https://app.acme.com/callback", ""},
		{"https root", "https://app.acme.com/", ""},
		{"loopback v4", "http://127.0.0.1:8765/cb", ""},
		{"loopback v6", "http://[::1]:8765/cb", ""},
		{"loopback localhost", "http://localhost/cb", ""},
		{"custom reverse-dns", "com.example.app://oauth", ""},
		{"custom scheme cursor.dev", "cursor.app://callback", ""},

		// rejects
		{"empty", "", "empty"},
		{"no scheme", "//foo", "scheme"},
		{"https with fragment", "https://app.acme.com/cb#x", "fragment"},
		{"https with userinfo", "https://u:p@app.acme.com/", "userinfo"},
		{"https ip literal", "https://1.2.3.4/", "IP literal"},
		{"https idn", "https://exämple.com/", "ASCII"},
		{"http non-loopback", "http://app.acme.com/", "loopback"},
		{"custom no dot", "cursor://x", ""},
		{"custom file scheme", "file.x://nope", ""}, // dot present → ok structurally; not in disallow set as-is
		{"banned data", "data://x", "disallowed"},
		{"banned javascript", "javascript://x", "disallowed"},
		{"banned file", "file://x", "disallowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRedirectURI(tt.uri)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestCompilePattern_Validation(t *testing.T) {
	good := []string{
		"https://app.acme.com/callback",
		"https://*.acme.com/oauth/callback",
		"https://*.acme.com/**",
		"https://app.acme.com",
		"http://127.0.0.1:*/**",
		"http://localhost/**",
		"cursor.app://**",
		"com.example.app://oauth/*",
	}
	for _, raw := range good {
		t.Run("ok/"+raw, func(t *testing.T) {
			if _, err := CompilePattern(raw); err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
		})
	}
	bad := []struct {
		raw, want string
	}{
		{"", "empty"},
		{"no-scheme", "scheme"},
		{"https://*.com/**", "≥2 fixed suffix"},
		{"https://*/**", "≥2 fixed suffix"},
		{"https://app*.acme.com/", "partial wildcards"},
		{"https://acme.com/**/x", "must be the last"},
		{"https://acme.com:abc/", "port must be"},
		{"file.x://**", ""}, // structurally ok (has dot); disallow set covers file/data/javascript explicitly
	}
	for _, tt := range bad {
		t.Run("bad/"+tt.raw, func(t *testing.T) {
			_, err := CompilePattern(tt.raw)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestPattern_Matches(t *testing.T) {
	tests := []struct {
		pattern string
		uri     string
		want    bool
	}{
		// exact
		{"https://app.acme.com/callback", "https://app.acme.com/callback", true},
		{"https://app.acme.com/callback", "https://app.acme.com/callback/", false},
		{"https://app.acme.com/callback", "https://other.acme.com/callback", false},

		// host wildcard single label
		{"https://*.acme.com/cb", "https://app.acme.com/cb", true},
		{"https://*.acme.com/cb", "https://a.b.acme.com/cb", false},
		{"https://*.acme.com/cb", "https://acme.com/cb", false},

		// path **
		{"https://*.acme.com/**", "https://app.acme.com/", true},
		{"https://*.acme.com/**", "https://app.acme.com/a/b/c", true},
		{"https://*.acme.com/**", "https://app.acme.com", true},

		// path *
		{"https://acme.com/oauth/*", "https://acme.com/oauth/cb", true},
		{"https://acme.com/oauth/*", "https://acme.com/oauth/cb/x", false},
		{"https://acme.com/oauth/*", "https://acme.com/oauth/", false},

		// loopback any port + path — `:*` includes the no-port case.
		{"http://127.0.0.1:*/**", "http://127.0.0.1:8765/cb", true},
		{"http://127.0.0.1:*/**", "http://127.0.0.1/cb", true},
		{"http://localhost/**", "http://localhost/cb", true},
		{"http://localhost/**", "http://localhost:9000/cb", false}, // pattern has no port → URI must have none either

		// custom scheme — opaque match
		{"cursor.app://**", "cursor.app://callback", true},
		{"cursor.app://**", "cursor.app://a/b", true},
		{"cursor.app://**", "other.app://callback", false},

		// case insensitive host
		{"https://app.acme.com/cb", "https://APP.ACME.COM/cb", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"|"+tt.uri, func(t *testing.T) {
			p, err := CompilePattern(tt.pattern)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if got := p.Matches(tt.uri); got != tt.want {
				t.Fatalf("Matches(%q) = %v, want %v", tt.uri, got, tt.want)
			}
		})
	}
}

func TestPatternSet_CheckRedirectURI(t *testing.T) {
	t.Run("empty set allows anything that passes the floor", func(t *testing.T) {
		var s PatternSet
		if err := s.CheckRedirectURI("https://anywhere.example/cb"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := s.CheckRedirectURI("http://app.example/cb"); err == nil {
			t.Fatal("expected floor rejection of non-loopback http")
		}
	})

	t.Run("non-empty set narrows but cannot relax the floor", func(t *testing.T) {
		s, err := CompilePatternSet([]string{
			"https://*.acme.com/**",
			"http://localhost/**",
		})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		// Floor pass + allowlist match.
		if err := s.CheckRedirectURI("https://app.acme.com/cb"); err != nil {
			t.Fatalf("expected ok, got %v", err)
		}
		// Floor passes but allowlist rejects.
		if err := s.CheckRedirectURI("https://elsewhere.example/cb"); err == nil {
			t.Fatal("expected allowlist rejection")
		}
		// Floor itself rejects — allowlist never gets a say.
		if err := s.CheckRedirectURI("https://exämple.com/cb"); err == nil {
			t.Fatal("expected floor rejection of IDN host")
		}
	})

	t.Run("duplicates and blanks are de-duplicated", func(t *testing.T) {
		s, err := CompilePatternSet([]string{
			"https://app.acme.com/cb",
			"  ",
			"https://app.acme.com/cb",
		})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if s.Len() != 1 {
			t.Fatalf("expected 1 pattern after dedupe, got %d", s.Len())
		}
	})

	t.Run("aggregates pattern errors", func(t *testing.T) {
		_, err := CompilePatternSet([]string{
			"https://*.com/**",
			"no-scheme",
		})
		if err == nil {
			t.Fatal("expected aggregated error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "≥2 fixed suffix") || !strings.Contains(msg, "scheme") {
			t.Fatalf("expected both errors in %q", msg)
		}
	})
}
