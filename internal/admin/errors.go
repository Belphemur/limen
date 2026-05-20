package admin

import (
	"fmt"

	"connectrpc.com/connect"
)

// errUnimplemented keeps every slice-1 method body a one-liner.
// The slice tag (e.g. "slice-2") points to the phase 9c slice that
// owns the real implementation.
func errUnimplemented(slice string) error {
	return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("admin: not implemented (phase 9c %s)", slice))
}
