// Package ids defines ULID-with-prefix identifiers used as the public
// representation of every persistent entity in Limen.
//
// We use [github.com/oklog/ulid/v2] (Crockford base32, 26-char body,
// millisecond timestamp resolution). ulid.Make is monotonic within the
// same millisecond, so IDs minted from a single process sort in
// creation order even under bursts.
//
// Internal int64 IDs are never exposed; all external surfaces
// (Connect-RPC, HTTP, logs, the SPA) speak public IDs only.
package ids

import (
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"
)

// Prefix is a stable type tag prepended to a ULID body. Prefixes are part of
// the public ID format and must never be renamed or reused once shipped.
type Prefix string

const sep = "_"

// New returns a fresh public ID of the form "<prefix>_<26-char-ULID>".
func New(p Prefix) string {
	return string(p) + sep + ulid.Make().String()
}

// Parse splits a public ID into its prefix and ULID body. It returns an
// error if the input is malformed or the ULID body is not valid.
func Parse(s string) (Prefix, ulid.ULID, error) {
	i := strings.LastIndex(s, sep)
	if i <= 0 || i == len(s)-1 {
		return "", ulid.ULID{}, fmt.Errorf("ids: malformed public id %q", s)
	}
	p := Prefix(s[:i])
	u, err := ulid.Parse(s[i+1:])
	if err != nil {
		return "", ulid.ULID{}, fmt.Errorf("ids: invalid ulid in %q: %w", s, err)
	}
	return p, u, nil
}

// MustParse parses s and verifies its prefix matches expected. It returns an
// error if either parsing fails or the prefix does not match.
func MustParse(expected Prefix, s string) (ulid.ULID, error) {
	got, u, err := Parse(s)
	if err != nil {
		return ulid.ULID{}, err
	}
	if got != expected {
		return ulid.ULID{}, fmt.Errorf("ids: expected prefix %q, got %q", expected, got)
	}
	return u, nil
}
