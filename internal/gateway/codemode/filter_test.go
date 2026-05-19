package codemode

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// flattenJS unwraps codemode.tools() envelope → sorted tool names.
const flattenJS = `(filter) => {
	const r = codemode.tools(filter);
	const names = [];
	for (const g of r.upstreams) for (const t of g.tools) names.push(t.name);
	names.sort();
	return names;
}`

func TestToolsCatalogShape(t *testing.T) {
	tools := []Tool{
		{Name: "search", Description: "find stuff", Upstream: "github", InputSchema: map[string]any{"type": "object"}},
	}
	d := &fakeDispatcher{
		tools:     tools,
		upstreams: []UpstreamMeta{{Name: "github", Aliases: []string{}, Context: map[string]any{}}},
	}
	h := newTestHandler(t, d, Config{})
	got, err := h.Search(context.Background(), `(() => {
		const r = codemode.tools();
		return {
			upstreamKeys: Object.keys(r.upstreams[0]).sort(),
			upstreamName: r.upstreams[0].name,
			aliasesIsArray: Array.isArray(r.upstreams[0].aliases),
			contextIsObject: typeof r.upstreams[0].context === 'object',
			toolKeys: Object.keys(r.upstreams[0].tools[0]).sort(),
			toolName: r.upstreams[0].tools[0].name,
			toolDescription: r.upstreams[0].tools[0].description,
			hasHint: 'hint' in r,
		};
	})()`)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	m := got.(map[string]any)
	wantUpKeys := []any{"aliases", "context", "name", "tools"}
	if !reflect.DeepEqual(m["upstreamKeys"], wantUpKeys) {
		t.Errorf("group keys: got %v want %v", m["upstreamKeys"], wantUpKeys)
	}
	if m["upstreamName"] != "github" {
		t.Errorf("name: got %v want github", m["upstreamName"])
	}
	if m["aliasesIsArray"] != true {
		t.Errorf("aliases should be array, got %v", m["aliasesIsArray"])
	}
	if m["contextIsObject"] != true {
		t.Errorf("context should be object, got %v", m["contextIsObject"])
	}
	wantToolKeys := []any{"description", "name"}
	if !reflect.DeepEqual(m["toolKeys"], wantToolKeys) {
		t.Errorf("tool keys: got %v want %v", m["toolKeys"], wantToolKeys)
	}
	if m["toolName"] != "search" || m["toolDescription"] != "find stuff" {
		t.Errorf("tool: %#v", m)
	}
	if m["hasHint"] != false {
		t.Errorf("unfiltered call should omit hint")
	}
}

// codemode.tools() must NOT surface inputSchema. Schemas are accessed
// via codemode.schemas().
func TestToolsCatalogOmitsInputSchema(t *testing.T) {
	tools := []Tool{
		{Name: "search", Description: "find", Upstream: "github", InputSchema: map[string]any{"type": "object"}},
	}
	d := &fakeDispatcher{tools: tools}
	h := newTestHandler(t, d, Config{})
	got, err := h.Search(context.Background(), `(() => {
		const t = codemode.tools().upstreams[0].tools[0];
		return { keys: Object.keys(t).sort(), hasInputSchema: 'inputSchema' in t };
	})()`)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	m := got.(map[string]any)
	if m["hasInputSchema"] == true {
		t.Errorf("codemode.tools() leaked inputSchema; keys=%v", m["keys"])
	}
}

func TestToolsFilter(t *testing.T) {
	tools := []Tool{
		{Name: "searchJira", Description: "search issues", Upstream: "atlassian"},
		{Name: "createJira", Description: "create issue", Upstream: "atlassian"},
		{Name: "searchRepos", Description: "search github", Upstream: "github"},
		{Name: "getPullRequest", Description: "fetch a pull request", Upstream: "github"},
		{Name: "listTickets", Description: "list jira tickets", Upstream: "jira"},
	}
	d := &fakeDispatcher{tools: tools}
	h := newTestHandler(t, d, Config{})

	cases := []struct {
		name   string
		filter string
		want   []string
	}{
		{"upstream single", `{upstream:'atlassian'}`, []string{"createJira", "searchJira"}},
		{"upstream array (any-of)", `{upstream:['atlassian','jira']}`, []string{"createJira", "listTickets", "searchJira"}},
		{"match single", `{match:'search'}`, []string{"searchJira", "searchRepos"}},
		{"match array OR", `{match:['jira','atlassian']}`, []string{"createJira", "listTickets", "searchJira"}},
		{"name only", `{name:'jira'}`, []string{"createJira", "searchJira"}},
		{"description only", `{description:'pull request'}`, []string{"getPullRequest"}},
		{"upstream+match AND", `{upstream:'atlassian', match:'search'}`, []string{"searchJira"}},
		{"allOf AND across patterns", `{allOf:['pull','request']}`, []string{"getPullRequest"}},
		{"regex on name", `{regex:true, name:'^search'}`, []string{"searchJira", "searchRepos"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := h.Search(context.Background(), `(`+flattenJS+`)(`+tc.filter+`)`)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			arr, ok := got.([]any)
			if !ok {
				t.Fatalf("got %T want []any: %#v", got, got)
			}
			gotNames := make([]string, len(arr))
			for i, v := range arr {
				gotNames[i] = v.(string)
			}
			if !reflect.DeepEqual(gotNames, tc.want) {
				t.Errorf("got %v want %v", gotNames, tc.want)
			}
		})
	}
}

func TestToolsFilter_LimitCapsTotal(t *testing.T) {
	tools := []Tool{
		{Name: "a1", Upstream: "u1"}, {Name: "a2", Upstream: "u1"},
		{Name: "b1", Upstream: "u2"}, {Name: "b2", Upstream: "u2"},
	}
	d := &fakeDispatcher{tools: tools}
	h := newTestHandler(t, d, Config{})
	got, err := h.Search(context.Background(), `(() => {
		const r = codemode.tools({limit: 3});
		let total = 0;
		for (const g of r.upstreams) total += g.tools.length;
		return total;
	})()`)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got != int64(3) && got != float64(3) {
		t.Errorf("total: got %v want 3", got)
	}
}

// Empty filter result with non-empty filter attaches a hint containing
// tried / available / suggested.
func TestToolsFilter_EmptyHint(t *testing.T) {
	tools := []Tool{
		{Name: "searchJira", Upstream: "atlassian"},
		{Name: "searchRepos", Upstream: "github"},
	}
	d := &fakeDispatcher{
		tools: tools,
		upstreams: []UpstreamMeta{
			{Name: "atlassian", Aliases: []string{"jira", "confluence"}, Context: map[string]any{}},
			{Name: "github", Aliases: []string{}, Context: map[string]any{}},
		},
	}
	h := newTestHandler(t, d, Config{})
	got, err := h.Search(context.Background(), `(() => {
		const r = codemode.tools({upstream: 'jiraa'});
		return {
			upstreams: r.upstreams.length,
			hasHint: r.hint != null,
			tried: r.hint && Array.from(r.hint.tried),
			available: r.hint && Array.from(r.hint.available),
			suggested: r.hint && Array.from(r.hint.suggested),
		};
	})()`)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	m := got.(map[string]any)
	if m["upstreams"] != int64(0) && m["upstreams"] != float64(0) {
		t.Errorf("expected zero groups, got %v", m["upstreams"])
	}
	if m["hasHint"] != true {
		t.Fatalf("expected hint, got %#v", m)
	}
	sugg, _ := m["suggested"].([]any)
	if len(sugg) == 0 || sugg[0] != "jira" {
		t.Errorf("expected 'jira' as top suggestion, got %v", m["suggested"])
	}
}

// Filtering against an alias should match the canonical group.
func TestToolsFilter_AliasMatchesUpstream(t *testing.T) {
	tools := []Tool{
		{Name: "jira_search", Upstream: "atlassian"},
		{Name: "jira_create", Upstream: "atlassian"},
		{Name: "confluence_search", Upstream: "atlassian"},
		{Name: "confluence_get", Upstream: "atlassian"},
	}
	d := &fakeDispatcher{
		tools: tools,
		upstreams: []UpstreamMeta{
			{Name: "atlassian", Aliases: []string{"jira", "confluence"}, Context: map[string]any{}},
		},
	}
	h := newTestHandler(t, d, Config{})
	got, err := h.Search(context.Background(), `(`+flattenJS+`)({upstream: 'jira'})`)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	arr := got.([]any)
	if len(arr) != 4 {
		t.Errorf("alias should expand to canonical group; got %d tools: %v", len(arr), arr)
	}
}

func TestToolsFilter_InvalidRegex(t *testing.T) {
	d := &fakeDispatcher{tools: []Tool{{Name: "x", Upstream: "u"}}}
	h := newTestHandler(t, d, Config{})
	_, err := h.Search(context.Background(), `codemode.tools({regex:true, name:'('})`)
	if err == nil {
		t.Fatal("expected error for invalid regex, got nil")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Errorf("error %q should mention invalid regex", err.Error())
	}
}

// Tool entries expose camelCase `name`/`description` matching JSON tags.
func TestToolFieldsAreCamelCaseInJS(t *testing.T) {
	tools := []Tool{
		{Name: "search", Description: "find", Upstream: "github", InputSchema: map[string]any{"type": "object"}},
	}
	d := &fakeDispatcher{tools: tools}
	h := newTestHandler(t, d, Config{})

	got, err := h.Search(context.Background(), `(() => {
		const t = codemode.tools().upstreams[0].tools[0];
		return {
			keys: Object.keys(t).sort(),
			name: t.name,
			description: t.description,
			pascalLeak: t.Name !== undefined || t.Description !== undefined,
		};
	})()`)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	m := got.(map[string]any)
	if m["name"] != "search" {
		t.Errorf("name: got %v want search", m["name"])
	}
	if m["description"] != "find" {
		t.Errorf("description: got %v want find", m["description"])
	}
	if m["pascalLeak"] == true {
		t.Errorf("Go field names leaked into JS surface")
	}
}

// Group-level context is surfaced verbatim and spreadable.
func TestToolsContext_SurfacedAndSpreadable(t *testing.T) {
	d := &fakeDispatcher{
		tools: []Tool{{Name: "search", Upstream: "atlassian"}},
		upstreams: []UpstreamMeta{
			{Name: "atlassian", Aliases: []string{}, Context: map[string]any{"cloudId": "abc", "defaultProject": "OP"}},
		},
	}
	h := newTestHandler(t, d, Config{})
	got, err := h.Search(context.Background(), `(() => {
		const g = codemode.tools().upstreams[0];
		const merged = {...g.context, extra: 1};
		return merged;
	})()`)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	m := got.(map[string]any)
	if m["cloudId"] != "abc" || m["defaultProject"] != "OP" || m["extra"] != int64(1) {
		t.Errorf("spread context: %#v", m)
	}
}
