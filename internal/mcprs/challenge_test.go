package mcprs

import (
	"strings"
	"testing"
)

func TestWWWAuthenticate(t *testing.T) {
	tests := []struct {
		name        string
		metadataURL string
		errCode     string
		errDesc     string
		wantParts   []string
		notWant     []string
	}{
		{
			name:        "full challenge",
			metadataURL: "https://l.example.com/t/tnt_1/mcp/.well-known/oauth-protected-resource",
			errCode:     ErrInvalidToken,
			errDesc:     "expired",
			wantParts: []string{
				`Bearer realm="mcp"`,
				`resource_metadata="https://l.example.com/t/tnt_1/mcp/.well-known/oauth-protected-resource"`,
				`error="invalid_token"`,
				`error_description="expired"`,
			},
		},
		{
			name:        "no error fields",
			metadataURL: "https://l.example.com/x",
			wantParts: []string{
				`Bearer realm="mcp"`,
				`resource_metadata="https://l.example.com/x"`,
			},
			notWant: []string{`error=`, `error_description=`},
		},
		{
			name: "bare bearer",
			wantParts: []string{
				`Bearer realm="mcp"`,
			},
			notWant: []string{`resource_metadata=`, `error=`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WWWAuthenticate(tc.metadataURL, tc.errCode, tc.errDesc)
			for _, p := range tc.wantParts {
				if !strings.Contains(got, p) {
					t.Errorf("missing %q in %q", p, got)
				}
			}
			for _, p := range tc.notWant {
				if strings.Contains(got, p) {
					t.Errorf("unexpected %q in %q", p, got)
				}
			}
		})
	}
}
