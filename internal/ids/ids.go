// Package ids defines KSUID-with-prefix identifiers used as the public
// representation of every persistent entity in Limen.
//
// Internal int64 IDs are never exposed; all external surfaces (Connect-RPC,
// HTTP, logs, the SPA) speak public IDs only.
package ids

import (
	"fmt"
	"strings"

	"github.com/segmentio/ksuid"
)

// Prefix is a stable type tag prepended to a KSUID body. Prefixes are part of
// the public ID format and must never be renamed or reused once shipped.
type Prefix string

const sep = "_"

// New returns a fresh public ID of the form "<prefix>_<27-char-ksuid>".
func New(p Prefix) string {
	return string(p) + sep + ksuid.New().String()
}

// Parse splits a public ID into its prefix and KSUID body. It returns an
// error if the input is malformed or the KSUID body is not valid.
func Parse(s string) (Prefix, ksuid.KSUID, error) {
	i := strings.LastIndex(s, sep)
	if i <= 0 || i == len(s)-1 {
		return "", ksuid.Nil, fmt.Errorf("ids: malformed public id %q", s)
	}
	p := Prefix(s[:i])
	k, err := ksuid.Parse(s[i+1:])
	if err != nil {
		return "", ksuid.Nil, fmt.Errorf("ids: invalid ksuid in %q: %w", s, err)
	}
	return p, k, nil
}

// MustParse parses s and verifies its prefix matches expected. It returns an
// error if either parsing fails or the prefix does not match.
func MustParse(expected Prefix, s string) (ksuid.KSUID, error) {
	got, k, err := Parse(s)
	if err != nil {
		return ksuid.Nil, err
	}
	if got != expected {
		return ksuid.Nil, fmt.Errorf("ids: expected prefix %q, got %q", expected, got)
	}
	return k, nil
}
