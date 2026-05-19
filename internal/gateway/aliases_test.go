package gateway

import (
	"reflect"
	"sort"
	"testing"
)

func TestDeriveAliases_AtlassianTwoBrands(t *testing.T) {
	tools := []string{
		"jira_searchIssues", "jira_createIssue", "jira_listProjects",
		"confluence_search", "confluence_getPage",
	}
	got := DeriveAliases(tools)
	want := []string{"confluence", "jira"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestDeriveAliases_HyphenSeparator(t *testing.T) {
	tools := []string{"foo-list", "foo-get", "foo-create", "foo-delete"}
	got := DeriveAliases(tools)
	if !reflect.DeepEqual(got, []string{"foo"}) {
		t.Errorf("got %v want [foo]", got)
	}
}

func TestDeriveAliases_NoPrefixDominates(t *testing.T) {
	// No separator anywhere → no alias.
	tools := []string{"search", "create", "list", "delete"}
	got := DeriveAliases(tools)
	if len(got) != 0 {
		t.Errorf("expected no aliases, got %v", got)
	}
}

func TestDeriveAliases_BelowShareFloor(t *testing.T) {
	// jira: 2/10 = 20% — below 50%.
	tools := []string{
		"jira_a", "jira_b",
		"search", "create", "list", "delete", "foo", "bar", "baz", "qux",
	}
	got := DeriveAliases(tools)
	if len(got) != 0 {
		t.Errorf("expected no aliases, got %v", got)
	}
}

func TestDeriveAliases_BelowToolFloor(t *testing.T) {
	// Even at 100% share, a single tool with a prefix never promotes.
	tools := []string{"jira_a"}
	got := DeriveAliases(tools)
	if len(got) != 0 {
		t.Errorf("expected no aliases, got %v", got)
	}
}

func TestDeriveAliases_FiltersReserved(t *testing.T) {
	tools := []string{"tools_a", "tools_b", "tools_c"}
	got := DeriveAliases(tools)
	if len(got) != 0 {
		t.Errorf("expected reserved prefix to be filtered, got %v", got)
	}
}

func TestResolveAliasCollisions_DropsClaimedByMany(t *testing.T) {
	in := map[string][]string{
		"github":    {"git"},
		"gitlab":    {"git"},
		"atlassian": {"jira", "confluence"},
	}
	got, collisions := ResolveAliasCollisions(in)
	want := map[string][]string{
		"github":    {},
		"gitlab":    {},
		"atlassian": {"jira", "confluence"},
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("upstream %s: got %v want %v", k, got[k], v)
		}
	}
	sort.Strings(collisions)
	if !reflect.DeepEqual(collisions, []string{"git"}) {
		t.Errorf("collisions: got %v want [git]", collisions)
	}
}

func TestResolveAliasCollisions_NoCollisions(t *testing.T) {
	in := map[string][]string{
		"a": {"x"},
		"b": {"y"},
	}
	got, collisions := ResolveAliasCollisions(in)
	if len(collisions) != 0 {
		t.Errorf("unexpected collisions: %v", collisions)
	}
	if !reflect.DeepEqual(got["a"], []string{"x"}) || !reflect.DeepEqual(got["b"], []string{"y"}) {
		t.Errorf("aliases changed unexpectedly: %v", got)
	}
}
