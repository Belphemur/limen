package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"", ""},
		{"Bearer ", ""},
		{"Bearer abc", "abc"},
		{"bearer abc", "abc"},
		{"BEARER  abc  ", "abc"},
		{"Basic abc", ""},
		{"Token abc", ""},
		{"abc", ""},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		if got := extractBearerToken(req); got != tc.want {
			t.Errorf("extractBearerToken(%q) = %q want %q", tc.header, got, tc.want)
		}
	}
}

func TestAudienceContains(t *testing.T) {
	if audienceContains(nil, "x") {
		t.Error("nil aud should not contain x")
	}
	if !audienceContains([]string{"a", "limen-mcp", "b"}, "limen-mcp") {
		t.Error("expected match")
	}
	if audienceContains([]string{"a", "b"}, "limen-mcp") {
		t.Error("unexpected match")
	}
}
