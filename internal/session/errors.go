package session

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
)

func errUnauthenticated(reason string) error {
	return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("session: %s", reason))
}

func errPermissionDenied(reason string) error {
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("session: %s", reason))
}

func errNotFound(reason string) error {
	return connect.NewError(connect.CodeNotFound, errors.New("session: "+reason))
}
