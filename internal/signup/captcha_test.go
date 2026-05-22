package signup

import (
	"context"
	"testing"
)

func TestDevBypassVerifier(t *testing.T) {
	v := DevBypassVerifier{}
	if err := v.Verify(context.Background(), "dev-captcha-bypass", ""); err != nil {
		t.Fatalf("want nil for sentinel, got %v", err)
	}
	if err := v.Verify(context.Background(), "", ""); err == nil {
		t.Fatal("want error on empty token")
	}
	if err := v.Verify(context.Background(), "anything-else", ""); err == nil {
		t.Fatal("want error on unknown token")
	}
}
