package enforcer

import "fmt"

// ErrFeatureLocked is returned when a tenant tries to use a feature they
// haven't paid for. It carries structured detail the portal frontend can
// use to show upgrade prompts.
type ErrFeatureLocked struct {
	Feature string // e.g. "max-users"
	Limit   int32  // the limit (or -1 for boolean features that are disabled)
	Usage   int32  // current usage (or -1 if not applicable)
	Message string // human-readable description
}

// Error implements error.
func (e *ErrFeatureLocked) Error() string {
	return fmt.Sprintf("billing.limit.%s: %s (limit=%d, usage=%d)", e.Feature, e.Message, e.Limit, e.Usage)
}

// NewFeatureLockedError creates a connect-compatible error.
// The caller wraps with connect.NewError(connect.CodePermissionDenied, err).
func NewFeatureLockedError(feature string, limit, usage int32, msg string) *ErrFeatureLocked {
	return &ErrFeatureLocked{Feature: feature, Limit: limit, Usage: usage, Message: msg}
}

// ErrSAConnectionLimit is returned when a service account tool call would
// exceed the tenant's MaxSAConnections entitlement. Callers in the MCP
// transport should convert this to an in-band CallToolResult{IsError:true}.
type ErrSAConnectionLimit struct {
	Limit int32
	Usage int32
}

func (e *ErrSAConnectionLimit) Error() string {
	return fmt.Sprintf("SA connection limit reached (%d/%d). Try again later.", e.Usage, e.Limit)
}

func NewSAConnectionLimitError(limit, usage int32) *ErrSAConnectionLimit {
	return &ErrSAConnectionLimit{Limit: limit, Usage: usage}
}
