package zitadel

import "testing"

func TestConfig_Validate(t *testing.T) {
	base := Config{
		Domain:    "https://auth.example.com",
		AuthMode:  AuthModePAT,
		PAT:       "tok",
		ProjectID: "proj-1",
	}
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr bool
	}{
		{"valid PAT", func(c *Config) {}, false},
		{"valid JWT key", func(c *Config) {
			c.AuthMode = AuthModeJWTKey
			c.PAT = ""
			c.JWTKeyPath = "/etc/limen/zitadel.json"
		}, false},

		{"missing Domain", func(c *Config) { c.Domain = "" }, true},
		{"whitespace Domain", func(c *Config) { c.Domain = "   " }, true},
		{"missing ProjectID", func(c *Config) { c.ProjectID = "" }, true},

		{"PAT mode without PAT", func(c *Config) { c.PAT = "" }, true},
		{"JWT mode without path", func(c *Config) {
			c.AuthMode = AuthModeJWTKey
			c.PAT = ""
			c.JWTKeyPath = ""
		}, true},

		{"unknown auth mode", func(c *Config) {
			c.AuthMode = AuthMode("oauth2")
		}, true},
		{"empty auth mode", func(c *Config) {
			c.AuthMode = ""
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base
			tt.mutate(&c)
			err := c.validate()
			if tt.wantErr && err == nil {
				t.Errorf("want error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("want nil, got %v", err)
			}
		})
	}
}
