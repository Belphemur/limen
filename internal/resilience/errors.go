package resilience

import "fmt"

// BreakerOpenError is returned when a circuit breaker is open.
// Callers can check with errors.As to map to appropriate user-facing errors.
type BreakerOpenError struct {
	Name string // dependency name (e.g., "upstream.atlassian")
}

func (e *BreakerOpenError) Error() string {
	return fmt.Sprintf("circuit breaker %q is open", e.Name)
}
