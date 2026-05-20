package signup

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	signupv1 "github.com/belphemur/limen/internal/signup/signupv1"
)

func errUnimplemented(slice string) error {
	return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("signup: not implemented (phase 9c %s)", slice))
}

func (s *Service) StartSignup(_ context.Context, _ *connect.Request[signupv1.StartSignupRequest]) (*connect.Response[signupv1.StartSignupResponse], error) {
	return nil, errUnimplemented("slice-4")
}

func (s *Service) CompleteSignup(_ context.Context, _ *connect.Request[signupv1.CompleteSignupRequest]) (*connect.Response[signupv1.CompleteSignupResponse], error) {
	return nil, errUnimplemented("slice-4")
}
