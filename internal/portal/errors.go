package portal

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
)

// errInternal hides the underlying error from the wire — it is logged
// at the call site, never returned to clients.
func errInternal(_ error) error {
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

func errUnauthenticated(reason string) error {
	return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("portal: %s", reason))
}

func errPermissionDenied(reason string) error {
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("portal: %s", reason))
}

func errNotFound(reason string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("portal: %s", reason))
}

func errInvalidArgument(reason string) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("portal: %s", reason))
}

// Silence "unused" until other slices land. errInternal takes err so
// callers can log it; ignore here.
var _ = errInternal
