package upstream

import (
	"encoding/json"
	"sort"
	"strings"
)

// DecodeAliasesJSON parses storage.Upstream.AliasesJSON into a string
// slice. A nil, empty, or malformed payload yields a nil slice so
// callers can range over the result unconditionally.
func DecodeAliasesJSON(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// AliasMinTools is the per-prefix floor; a prefix shared by fewer than
// this many tools is not promoted to an alias.
const AliasMinTools = 2

// AliasMinShare is the upstream-wide share floor: at least this
// fraction of tools must carry SOME prefix for any prefix to be
// promoted. Atlassian (5/5 prefixed) clears it; a single-prefix outlier
// in an otherwise flat upstream does not.
const AliasMinShare = 0.5

// reservedAliasNames are sandbox keys that an alias must never shadow.
// Kept in sync with the codemode reserved set (no `gateway` import in
// codemode means we re-declare here).
var reservedAliasNames = map[string]struct{}{
	"tools":   {},
	"schemas": {},
	"call":    {},
	"json":    {},
	"quota":   {},
}

// DeriveAliases inspects toolNames for an upstream and returns prefix
// aliases that meet the floors.
//
// Prefix is everything before the first `_` or `-` in a tool name.
// Aliases are promoted when:
//   - ≥AliasMinShare of the upstream's tools carry SOME prefix (so a
//     mostly-flat upstream never produces aliases just because two
//     tools happen to share a prefix), AND
//   - the prefix group has ≥AliasMinTools tools.
//
// Returns aliases sorted for deterministic ordering. Reserved sandbox
// names (see reservedAliasNames) are filtered out before returning.
func DeriveAliases(toolNames []string) []string {
	if len(toolNames) == 0 {
		return nil
	}
	counts := make(map[string]int, len(toolNames))
	prefixed := 0
	for _, name := range toolNames {
		prefix := toolPrefix(name)
		if prefix == "" {
			continue
		}
		counts[prefix]++
		prefixed++
	}
	if len(counts) == 0 {
		return nil
	}
	if float64(prefixed)/float64(len(toolNames)) < AliasMinShare {
		return nil
	}
	out := make([]string, 0, len(counts))
	for prefix, n := range counts {
		if n < AliasMinTools {
			continue
		}
		if _, reserved := reservedAliasNames[prefix]; reserved {
			continue
		}
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
}

// toolPrefix returns the substring before the first `_` or `-` in name,
// or "" if there is no separator.
func toolPrefix(name string) string {
	idx := strings.IndexAny(name, "_-")
	if idx <= 0 {
		return ""
	}
	return name[:idx]
}

// ResolveAliasCollisions drops every alias claimed by more than one
// upstream from all claimants. Canonical upstream names are not
// processed here — callers feed the function alias slices only.
//
// in maps upstream name → its alias slice (typically the output of
// DeriveAliases). The returned map has the same keys with collisions
// removed; original input is not mutated. Collisions is the sorted list
// of dropped names so callers can emit a single structured log line.
func ResolveAliasCollisions(in map[string][]string) (resolved map[string][]string, collisions []string) {
	claims := make(map[string]int, 16)
	for _, aliases := range in {
		for _, a := range aliases {
			claims[a]++
		}
	}
	colliding := make(map[string]struct{}, 4)
	for a, n := range claims {
		if n > 1 {
			colliding[a] = struct{}{}
		}
	}
	resolved = make(map[string][]string, len(in))
	for upstream, aliases := range in {
		kept := make([]string, 0, len(aliases))
		for _, a := range aliases {
			if _, drop := colliding[a]; drop {
				continue
			}
			kept = append(kept, a)
		}
		resolved[upstream] = kept
	}
	collisions = make([]string, 0, len(colliding))
	for a := range colliding {
		collisions = append(collisions, a)
	}
	sort.Strings(collisions)
	return resolved, collisions
}
