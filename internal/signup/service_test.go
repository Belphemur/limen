package signup

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	signupv1 "github.com/belphemur/limen/internal/signup/signupv1"
	"github.com/belphemur/limen/internal/signup/signupv1/signupv1connect"
)

func mount(t *testing.T) signupv1connect.SignupServiceClient {
	t.Helper()
	svc := NewService(nil, zap.NewNop())
	_, h := svc.Handler()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return signupv1connect.NewSignupServiceClient(srv.Client(), srv.URL)
}

func TestStartSignup_Unimplemented(t *testing.T) {
	_, err := mount(t).StartSignup(context.Background(), connect.NewRequest(&signupv1.StartSignupRequest{}))
	if err == nil {
		t.Fatal("want CodeUnimplemented, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnimplemented {
		t.Fatalf("want CodeUnimplemented, got %v: %v", got, err)
	}
}

func TestCompleteSignup_Unimplemented(t *testing.T) {
	_, err := mount(t).CompleteSignup(context.Background(), connect.NewRequest(&signupv1.CompleteSignupRequest{}))
	if err == nil {
		t.Fatal("want CodeUnimplemented, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnimplemented {
		t.Fatalf("want CodeUnimplemented, got %v: %v", got, err)
	}
}
