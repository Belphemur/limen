// Phase 9a load-bearing test: the MCP gateway hot-path binary must
// not transitively import the OAuth proxy DCR surface or the Zitadel
// management client. If this test starts failing, a new import was
// added that pulls one of those packages into cmd/gateway — either
// move the new code to a sibling boot package that only the portal /
// all-in-one binary imports, or split the leaf package along the
// gateway/portal boundary.

package main_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestGateway_ImportGraph_ExcludesOauthproxyAndZitadel(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/belphemur/limen/cmd/gateway").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := []string{
		"github.com/belphemur/limen/internal/oauthproxy",
		"github.com/belphemur/limen/internal/zitadel",
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		for _, bad := range forbidden {
			if line == bad || strings.HasPrefix(line, bad+"/") {
				t.Errorf("cmd/gateway must not import %q (got %q in dependency graph)", bad, line)
			}
		}
	}
}
