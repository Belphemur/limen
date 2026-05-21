package mcpspec

import "errors"

// Stage-tagged sentinels emitted by Provision so callers (admin RPC,
// CLI) can map each failure mode to a user-actionable message instead
// of a generic "could not reach upstream".
var (
	ErrDiscoveryFailed      = errors.New("mcpspec: discovery failed")
	ErrDCRFailed            = errors.New("mcpspec: dynamic client registration failed")
	ErrStaticClientRequired = errors.New("mcpspec: authorization server does not support dynamic client registration; configure a static OAuth client")
	ErrPersistFailed        = errors.New("mcpspec: persist registration failed")
)
