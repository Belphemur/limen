package codemode

import (
	"fmt"
	"regexp"
	"strings"
)

// ToolListing is the lean shape returned by codemode.tools(). It
// intentionally OMITS inputSchema so the catalog stays cheap to scan;
// callers pull schemas on demand with codemode.schemas(names).
type ToolListing struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Upstream    string `json:"upstream"`
}

func toListings(in []Tool) []ToolListing {
	out := make([]ToolListing, len(in))
	for i, t := range in {
		out[i] = ToolListing{Name: t.Name, Description: t.Description, Upstream: t.Upstream}
	}
	return out
}

// filterListings narrows the lean catalog. All fields are optional and
// combine with AND. Within a single field, multiple patterns OR together
// (except `allOf`, which AND-combines its own list).
//
//	{
//	  upstream?:    string | string[],   // exact, any-of
//	  name?:        string | string[],   // substring(s) on name only
//	  description?: string | string[],   // substring(s) on description only
//	  match?:       string | string[],   // substring(s) on name+description
//	  allOf?:       string[],            // ALL substrings must appear in name+description
//	  regex?:       boolean,             // treat patterns as RE2 (case-insensitive)
//	  limit?:       number,              // cap results
//	}
//
// Returns an error (surfaced to JS) only when `regex: true` and one of
// the patterns fails to compile.
func filterListings(in []ToolListing, filter map[string]any) ([]ToolListing, error) {
	if len(filter) == 0 {
		return in, nil
	}
	upstreams := coerceStringList(filter["upstream"])
	useRegex, _ := filter["regex"].(bool)

	nameM, err := buildMatchers(filter["name"], useRegex)
	if err != nil {
		return nil, err
	}
	descM, err := buildMatchers(filter["description"], useRegex)
	if err != nil {
		return nil, err
	}
	matchM, err := buildMatchers(filter["match"], useRegex)
	if err != nil {
		return nil, err
	}
	allOfM, err := buildMatchers(filter["allOf"], useRegex)
	if err != nil {
		return nil, err
	}

	limit := coerceInt(filter["limit"])

	out := in[:0:0]
	for _, t := range in {
		if len(upstreams) > 0 && !containsString(upstreams, t.Upstream) {
			continue
		}
		if len(nameM) > 0 && !anyMatch(nameM, t.Name) {
			continue
		}
		if len(descM) > 0 && !anyMatch(descM, t.Description) {
			continue
		}
		hay := t.Name + " " + t.Description
		if len(matchM) > 0 && !anyMatch(matchM, hay) {
			continue
		}
		if len(allOfM) > 0 && !allMatch(allOfM, hay) {
			continue
		}
		out = append(out, t)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// matcher captures one filter pattern as either a lowercased substring
// or a compiled RE2 regex (always case-insensitive).
type matcher struct {
	sub string
	re  *regexp.Regexp
}

func (m matcher) match(s string) bool {
	if m.re != nil {
		return m.re.MatchString(s)
	}
	return strings.Contains(strings.ToLower(s), m.sub)
}

func buildMatchers(v any, useRegex bool) ([]matcher, error) {
	pats := coerceStringList(v)
	if len(pats) == 0 {
		return nil, nil
	}
	out := make([]matcher, 0, len(pats))
	for _, p := range pats {
		if useRegex {
			re, err := regexp.Compile("(?i)" + p)
			if err != nil {
				return nil, fmt.Errorf("codemode.tools: invalid regex %q: %w", p, err)
			}
			out = append(out, matcher{re: re})
			continue
		}
		out = append(out, matcher{sub: strings.ToLower(p)})
	}
	return out, nil
}

func anyMatch(ms []matcher, s string) bool {
	for _, m := range ms {
		if m.match(s) {
			return true
		}
	}
	return false
}

func allMatch(ms []matcher, s string) bool {
	for _, m := range ms {
		if !m.match(s) {
			return false
		}
	}
	return true
}

// coerceStringList accepts a string, a []any of strings, or a []string
// and returns a flat []string. Empty / nil / non-string inputs become nil.
func coerceStringList(v any) []string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []string:
		out := make([]string, 0, len(x))
		for _, s := range x {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func coerceInt(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}
