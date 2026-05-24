package codemode

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// ToolEntry is a tool's per-group shape: just name + description. The
// owning upstream is implicit in the parent UpstreamGroup.
type ToolEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// UpstreamGroup is a single (visible) upstream's slice of the catalog,
// including its ambient context and any discovered aliases. Tools are
// pre-sorted by name; the group is omitted from the envelope when its
// tool list is empty after filtering.
type UpstreamGroup struct {
	Name    string         `json:"name"`
	Aliases []string       `json:"aliases"`
	Context map[string]any `json:"context"`
	Tools   []ToolEntry    `json:"tools"`
}

// EmptyHint is appended to a ToolsResult when filtering yielded zero
// tools, to help the script (or LLM author) recover without re-listing
// the whole catalog.
type EmptyHint struct {
	Tried     []string `json:"tried"`
	Available []string `json:"available"`
	Suggested []string `json:"suggested"`
}

// ToolsResult is the top-level shape returned by codemode.tools(). Hint
// is non-nil only when filters were supplied and the result is empty.
type ToolsResult struct {
	Upstreams []UpstreamGroup `json:"upstreams"`
	Hint      *EmptyHint      `json:"hint,omitempty"`
}

// UpstreamMeta is the per-upstream metadata the Dispatcher hands the
// handler at run start. The handler joins this with the tool list to
// assemble UpstreamGroup values.
type UpstreamMeta struct {
	Name    string
	Aliases []string
	Context map[string]any
}

// buildGroups joins per-upstream metadata with the tool list. Tools
// whose upstream is missing from metas still get emitted under a
// synthetic group with an empty context and no aliases — losing data
// is worse than swallowing an inconsistency.
func buildGroups(tools []Tool, metas []UpstreamMeta) []UpstreamGroup {
	byName := make(map[string]*UpstreamGroup, len(metas))
	order := make([]string, 0, len(metas))
	for _, m := range metas {
		ctx := m.Context
		if ctx == nil {
			ctx = map[string]any{}
		}
		aliases := m.Aliases
		if aliases == nil {
			aliases = []string{}
		}
		byName[m.Name] = &UpstreamGroup{
			Name:    m.Name,
			Aliases: aliases,
			Context: ctx,
		}
		order = append(order, m.Name)
	}
	for _, t := range tools {
		g, ok := byName[t.Upstream]
		if !ok {
			g = &UpstreamGroup{
				Name:    t.Upstream,
				Aliases: []string{},
				Context: map[string]any{},
			}
			byName[t.Upstream] = g
			order = append(order, t.Upstream)
		}
		g.Tools = append(g.Tools, ToolEntry{Name: t.Name, Description: t.Description})
	}
	out := make([]UpstreamGroup, 0, len(order))
	for _, name := range order {
		g := byName[name]
		sort.Slice(g.Tools, func(i, j int) bool { return g.Tools[i].Name < g.Tools[j].Name })
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// filterTools narrows groups by tool-level predicates. All fields are
// optional and combine with AND.
//
//	{
//	  upstream?:    string | string[],   // matches group.name OR any alias
//	  name?:        string | string[],   // substring(s) on tool.name
//	  description?: string | string[],   // substring(s) on tool.description
//	  match?:       string | string[],   // substring(s) on name+description
//	  allOf?:       string[],            // ALL substrings must appear in name+description
//	  regex?:       boolean,             // treat patterns as RE2 (case-insensitive)
//	  limit?:       number,              // cap total tools across all groups
//	}
//
// Groups that end up with no tools are dropped. When the post-filter
// total is zero AND filter was non-empty, an EmptyHint is attached.
func filterTools(groups []UpstreamGroup, filter map[string]any) (ToolsResult, error) {
	if len(filter) == 0 {
		return ToolsResult{Upstreams: groups}, nil
	}
	upstreams := coerceStringList(filter["upstream"])
	useRegex, _ := filter["regex"].(bool)

	nameM, err := buildMatchers(filter["name"], useRegex)
	if err != nil {
		return ToolsResult{}, err
	}
	descM, err := buildMatchers(filter["description"], useRegex)
	if err != nil {
		return ToolsResult{}, err
	}
	matchM, err := buildMatchers(filter["match"], useRegex)
	if err != nil {
		return ToolsResult{}, err
	}
	allOfM, err := buildMatchers(filter["allOf"], useRegex)
	if err != nil {
		return ToolsResult{}, err
	}
	limit := coerceInt(filter["limit"])

	out := make([]UpstreamGroup, 0, len(groups))
	total := 0
outer:
	for _, g := range groups {
		if len(upstreams) > 0 && !upstreamMatches(g, upstreams) {
			continue
		}
		kept := make([]ToolEntry, 0, len(g.Tools))
		for _, t := range g.Tools {
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
			kept = append(kept, t)
			total++
			if limit > 0 && total >= limit {
				if len(kept) > 0 {
					ng := g
					ng.Tools = kept
					out = append(out, ng)
				}
				break outer
			}
		}
		if len(kept) > 0 {
			ng := g
			ng.Tools = kept
			out = append(out, ng)
		}
	}

	res := ToolsResult{Upstreams: out}
	if total == 0 {
		res.Hint = computeEmptyHint(groups, filter)
	}
	return res, nil
}

func upstreamMatches(g UpstreamGroup, wanted []string) bool {
	for _, w := range wanted {
		if w == g.Name {
			return true
		}
		if slices.Contains(g.Aliases, w) {
			return true
		}
	}
	return false
}

func computeEmptyHint(groups []UpstreamGroup, filter map[string]any) *EmptyHint {
	if len(groups) == 0 {
		return nil
	}
	tried := coerceStringList(filter["upstream"])
	avail := make([]string, 0, len(groups)*2)
	for _, g := range groups {
		avail = append(avail, g.Name)
		avail = append(avail, g.Aliases...)
	}
	sort.Strings(avail)
	avail = dedupSorted(avail)
	suggested := suggestNames(tried, avail)
	return &EmptyHint{
		Tried:     tried,
		Available: avail,
		Suggested: suggested,
	}
}

// suggestNames returns up to 3 best fuzzy matches to any element of
// tried. Match criteria: case-insensitive substring OR Levenshtein
// distance ≤ 2. Sorted by score (lower=better), then alphabetically.
func suggestNames(tried, available []string) []string {
	if len(tried) == 0 || len(available) == 0 {
		return nil
	}
	type candidate struct {
		name  string
		score int
	}
	cands := make([]candidate, 0, len(available))
	for _, name := range available {
		nl := strings.ToLower(name)
		best := -1
		for _, t := range tried {
			tl := strings.ToLower(t)
			if strings.Contains(nl, tl) || strings.Contains(tl, nl) {
				best = 0
				continue
			}
			d := levenshtein(nl, tl)
			if d <= 2 && (best == -1 || d < best) {
				best = d
			}
		}
		if best >= 0 {
			cands = append(cands, candidate{name: name, score: best})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score < cands[j].score
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) > 3 {
		cands = cands[:3]
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.name
	}
	return out
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			ad := prev[j] + 1
			bd := curr[j-1] + 1
			cd := prev[j-1] + cost
			m := min(cd, min(bd, ad))
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func dedupSorted(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for i := 1; i < len(in); i++ {
		if in[i] != in[i-1] {
			out = append(out, in[i])
		}
	}
	return out
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
