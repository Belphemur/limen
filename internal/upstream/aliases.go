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

// AliasMinPrefixShare is the per-prefix share floor: a candidate
// prefix must appear in at least this fraction of the upstream's
// tools to be promoted. This rejects verb-heavy CRUD catalogs (e.g.
// sentry, where the most-shared noun "issue" appears in only 4/23
// tools = 17%) while still letting multi-brand upstreams like
// atlassian promote jira (9/39 ≈ 23%) and confluence (11/39 ≈ 28%).
const AliasMinPrefixShare = 0.20

// commonToolVerbs is the stop-list applied to the FIRST token of a
// tool name. The intent is to strip CRUD/lookup/transition verbs so
// the candidate prefix is the noun or brand that follows. Lowercased
// for case-insensitive matching after camelCase tokenization.
var commonToolVerbs = map[string]struct{}{
	"add": {}, "analyze": {}, "create": {}, "delete": {}, "edit": {},
	"fetch": {}, "find": {}, "get": {}, "list": {}, "lookup": {},
	"remove": {}, "search": {}, "set": {}, "transition": {}, "update": {},
}

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

// DeriveAliases inspects toolNames for an upstream and returns
// brand-like prefix aliases that meet the floors.
//
// Each tool name is tokenized on `_`, `-`, and camelCase boundaries.
// A leading CRUD/lookup verb is stripped; the next token (lowercased)
// is the candidate prefix. Aliases are promoted when:
//   - the candidate appears in ≥AliasMinTools tools, AND
//   - the candidate's share of the upstream's tools is
//     ≥AliasMinPrefixShare.
//
// Returns aliases sorted for deterministic ordering. Reserved sandbox
// names (see reservedAliasNames) are filtered out before returning.
func DeriveAliases(toolNames []string) []string {
	if len(toolNames) == 0 {
		return nil
	}
	counts := make(map[string]int, len(toolNames))
	for _, name := range toolNames {
		prefix := candidatePrefix(name)
		if prefix == "" {
			continue
		}
		counts[prefix]++
	}
	if len(counts) == 0 {
		return nil
	}
	total := float64(len(toolNames))
	out := make([]string, 0, len(counts))
	for prefix, n := range counts {
		if n < AliasMinTools {
			continue
		}
		if float64(n)/total < AliasMinPrefixShare {
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

// candidatePrefix tokenizes a tool name on `_`, `-`, and camelCase
// boundaries, drops a leading CRUD/lookup verb, and returns the next
// token lowercased. Returns "" when no usable token remains (e.g.
// a single-word verb like "search" or "fetch", or a hyphen-only
// connector).
func candidatePrefix(name string) string {
	tokens := tokenizeToolName(name)
	if len(tokens) == 0 {
		return ""
	}
	first := strings.ToLower(tokens[0])
	if _, isVerb := commonToolVerbs[first]; isVerb {
		if len(tokens) < 2 {
			return ""
		}
		return strings.ToLower(tokens[1])
	}
	return first
}

// tokenizeToolName splits name on `_`, `-`, and lower→upper camelCase
// transitions. Empty fragments are dropped. Runs of uppercase letters
// (e.g. "URL", "ID") are kept as a single token.
func tokenizeToolName(name string) []string {
	if name == "" {
		return nil
	}
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		if r == '_' || r == '-' {
			flush()
			continue
		}
		// camelCase boundary: lower→upper.
		if i > 0 && isUpper(r) && isLower(runes[i-1]) {
			flush()
		}
		cur.WriteRune(r)
	}
	flush()
	return tokens
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }

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
