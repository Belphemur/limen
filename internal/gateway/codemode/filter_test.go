package codemode

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestToolsCatalogShape(t *testing.T) {
	tools := []Tool{
		{Name: "search", Description: "find stuff", Upstream: "github", InputSchema: map[string]any{"type": "object"}},
	}
	d := &fakeDispatcher{tools: tools}
	h := newTestHandler(t, d, Config{})
	got, err := h.Search(context.Background(), `codemode.tools()`)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	defs, ok := got.([]ToolListing)
	if !ok || len(defs) != 1 {
		t.Fatalf("unexpected: %#v", got)
	}
	want := ToolListing{
		Name:        "search",
		Description: "find stuff",
		Upstream:    "github",
	}
	if !reflect.DeepEqual(defs[0], want) {
		t.Errorf("entry mismatch:\n got: %#v\nwant: %#v", defs[0], want)
	}
}

// TestToolsCatalogOmitsInputSchema is the load-bearing contract test
// for the lean catalog: codemode.tools() must NOT surface inputSchema.
// Schemas are accessed via codemode.schemas().
func TestToolsCatalogOmitsInputSchema(t *testing.T) {
	tools := []Tool{
		{Name: "search", Description: "find", Upstream: "github", InputSchema: map[string]any{"type": "object"}},
	}
	d := &fakeDispatcher{tools: tools}
	h := newTestHandler(t, d, Config{})
	got, err := h.Search(context.Background(), `(() => {
		const t = codemode.tools()[0];
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

// TestToolsFilter exercises the enriched filter:
// upstream / name / description / match (string|array), allOf, regex, limit.
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
		name string
		js   string
		want []string
	}{
		{
			"upstream single",
			`codemode.tools({upstream:'atlassian'}).map(t=>t.name).sort()`,
			[]string{"createJira", "searchJira"},
		},
		{
			"upstream array (any-of)",
			`codemode.tools({upstream:['atlassian','jira']}).map(t=>t.name).sort()`,
			[]string{"createJira", "listTickets", "searchJira"},
		},
		{
			"match single",
			`codemode.tools({match:'search'}).map(t=>t.name).sort()`,
			[]string{"searchJira", "searchRepos"},
		},
		{
			"match array OR",
			`codemode.tools({match:['jira','atlassian']}).map(t=>t.name).sort()`,
			[]string{"createJira", "listTickets", "searchJira"},
		},
		{
			"name only",
			`codemode.tools({name:'jira'}).map(t=>t.name).sort()`,
			[]string{"createJira", "searchJira"},
		},
		{
			"description only",
			`codemode.tools({description:'pull request'}).map(t=>t.name)`,
			[]string{"getPullRequest"},
		},
		{
			"upstream+match AND",
			`codemode.tools({upstream:'atlassian', match:'search'}).map(t=>t.name)`,
			[]string{"searchJira"},
		},
		{
			"allOf AND across patterns",
			`codemode.tools({allOf:['pull','request']}).map(t=>t.name)`,
			[]string{"getPullRequest"},
		},
		{
			"regex on name",
			`codemode.tools({regex:true, name:'^search'}).map(t=>t.name).sort()`,
			[]string{"searchJira", "searchRepos"},
		},
		{
			"limit caps results",
			`codemode.tools({match:'search', limit:1}).length`,
			nil, // length check
		},
		{"no filter", `codemode.tools().length`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := h.Search(context.Background(), tc.js)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if tc.want == nil {
				// Length-only assertions for length-based cases above.
				switch tc.name {
				case "limit caps results":
					if got != int64(1) && got != float64(1) {
						t.Errorf("length: got %v want 1", got)
					}
				case "no filter":
					if got != int64(5) && got != float64(5) {
						t.Errorf("length: got %v want 5", got)
					}
				}
				return
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

// TestToolsFilter_InvalidRegex surfaces a clear error to JS.
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

// TestToolFieldsAreCamelCaseInJS guards the contract that
// codemode.tools() exposes properties matching the JSON tags
// (`name`, `description`, `upstream`) — not the Go struct field
// names. The default goja reflection surfaces Go field names, which
// silently breaks scripts that follow the documented shape.
func TestToolFieldsAreCamelCaseInJS(t *testing.T) {
	tools := []Tool{
		{Name: "search", Description: "find", Upstream: "github", InputSchema: map[string]any{"type": "object"}},
	}
	d := &fakeDispatcher{tools: tools}
	h := newTestHandler(t, d, Config{})

	got, err := h.Search(context.Background(), `(() => {
		const t = codemode.tools()[0];
		return {
			keys: Object.keys(t).sort(),
			name: t.name,
			description: t.description,
			upstream: t.upstream,
			pascalLeak: t.Name !== undefined || t.Upstream !== undefined,
		};
	})()`)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("want map, got %T: %#v", got, got)
	}
	if m["name"] != "search" {
		t.Errorf("name: got %v want search", m["name"])
	}
	if m["upstream"] != "github" {
		t.Errorf("upstream: got %v want github", m["upstream"])
	}
	if m["description"] != "find" {
		t.Errorf("description: got %v want find", m["description"])
	}
	if m["pascalLeak"] == true {
		t.Errorf("Go field names leaked into JS surface (Name/Upstream visible)")
	}
}
