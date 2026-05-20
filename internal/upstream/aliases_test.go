package upstream

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

func TestDeriveAliases_AtlassianCamelCase(t *testing.T) {
	// Real atlassian tool names (camelCase, verb-prefixed). Expect
	// the embedded brand tokens jira/confluence/compass to be lifted
	// to aliases via camelCase tokenization + verb stripping.
	tools := []string{
		"addCommentToJiraIssue", "addWorklogToJiraIssue",
		"atlassianUserInfo",
		"createCompassComponent", "createCompassComponentRelationship",
		"createCompassCustomFieldDefinition",
		"createConfluenceFooterComment", "createConfluenceInlineComment",
		"createConfluencePage",
		"createIssueLink", "createJiraIssue", "editJiraIssue",
		"fetch",
		"getAccessibleAtlassianResources",
		"getCompassComponent", "getCompassComponents",
		"getCompassCustomFieldDefinitions",
		"getConfluenceCommentChildren", "getConfluencePage",
		"getConfluencePageDescendants", "getConfluencePageFooterComments",
		"getConfluencePageInlineComments", "getConfluenceSpaces",
		"getIssueLinkTypes", "getJiraIssue",
		"getJiraIssueRemoteIssueLinks", "getJiraIssueTypeMetaWithFields",
		"getJiraProjectIssueTypesMetadata",
		"getPagesInConfluenceSpace",
		"getTeamworkGraphContext", "getTeamworkGraphObject",
		"getTransitionsForJiraIssue", "getVisibleJiraProjects",
		"lookupJiraAccountId", "search",
		"searchConfluenceUsingCql", "searchJiraIssuesUsingJql",
		"transitionJiraIssue", "updateConfluencePage",
	}
	got := DeriveAliases(tools)
	want := []string{"confluence", "jira"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestDeriveAliases_SentryVerbOnlyHasNoAliases(t *testing.T) {
	// Sentry's catalog is uniformly <verb>_<noun>. Verbs must be
	// stripped, and no remaining noun clears the per-prefix share
	// floor — so the upstream gets zero aliases (regression for the
	// bug where create/find/get/search/update were promoted).
	tools := []string{
		"analyze_issue_with_seer", "create_dsn", "create_project",
		"create_team", "find_dsns", "find_organizations",
		"find_projects", "find_releases", "find_teams", "get_doc",
		"get_event_attachment", "get_issue_tag_values",
		"get_latest_base_snapshot", "get_profile_details",
		"get_replay_details", "get_sentry_resource", "search_docs",
		"search_events", "search_issue_events", "search_issues",
		"update_issue", "update_project", "whoami",
	}
	got := DeriveAliases(tools)
	if len(got) != 0 {
		t.Errorf("expected no aliases for verb-only catalog, got %v", got)
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
	// No usable token after verb stripping → no alias.
	tools := []string{"search", "create", "list", "delete"}
	got := DeriveAliases(tools)
	if len(got) != 0 {
		t.Errorf("expected no aliases, got %v", got)
	}
}

func TestDeriveAliases_BelowPrefixShareFloor(t *testing.T) {
	// jira: 2/20 = 10% — below the 20% per-prefix share floor.
	tools := []string{
		"jira_a", "jira_b",
		"alpha", "beta", "gamma", "delta", "epsilon", "zeta",
		"eta", "theta", "iota", "kappa", "lambda", "mu",
		"nu", "xi", "omicron", "pi", "rho", "sigma",
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
