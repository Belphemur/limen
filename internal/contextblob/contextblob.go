package contextblob

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// MaxContextBlobBytes caps the serialized size of a context blob
// (defaults_json or context_json). ~20× any realistic value; bigger
// usually means a stray token paste.
const MaxContextBlobBytes = 4 * 1024

var contextKeyPattern = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)

// ValidateContextBlob parses raw and returns the decoded object on
// success. Used by admin/portal write paths and as a defense-in-depth
// read-time check.
//
// Rules:
//   - root must be a JSON object (not array, scalar, or null);
//   - serialized length must be ≤ MaxContextBlobBytes;
//   - every top-level key must match a JS-identifier pattern so scripts
//     can spread `{...group.context}` without bracket notation.
func ValidateContextBlob(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	if len(raw) > MaxContextBlobBytes {
		return nil, fmt.Errorf("context: blob is %d bytes, max %d", len(raw), MaxContextBlobBytes)
	}
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("context: invalid JSON: %w", err)
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("context: root must be a JSON object")
	}
	for k := range obj {
		if !contextKeyPattern.MatchString(k) {
			return nil, fmt.Errorf("context: key %q is not a valid JS identifier", k)
		}
	}
	return obj, nil
}

// MergeContext shallow-merges upstreamDefaults and linkContext into a
// fresh map. linkContext keys win over upstreamDefaults keys; nested
// values are taken whole from the higher-priority source. Returns a
// non-nil empty map when both inputs are empty so the JS surface never
// sees `null` for `context`.
func MergeContext(upstreamDefaults, linkContext map[string]any) map[string]any {
	out := make(map[string]any, len(upstreamDefaults)+len(linkContext))
	for k, v := range upstreamDefaults {
		out[k] = v
	}
	for k, v := range linkContext {
		out[k] = v
	}
	return out
}

// SafeLoadContextBlob is the read-time path: it unmarshals raw to a
// map, returning an empty map (and ok=false) on any failure so callers
// can log once without breaking the catalog load.
func SafeLoadContextBlob(raw []byte) (m map[string]any, ok bool) {
	if len(raw) == 0 {
		return map[string]any{}, true
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{}, false
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		return map[string]any{}, false
	}
	return obj, true
}
