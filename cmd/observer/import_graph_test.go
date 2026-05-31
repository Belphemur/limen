// Phase 16 load-bearing test: the metrics observer must not transitively
// import the portal/frontend routes, Zitadel admin client, or DCR oauthproxy.
// If this test starts failing, an incorrect dependency was pulled in.

package main_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestObserver_ImportGraph_ExcludesPortalAndAdmin(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/belphemur/limen/cmd/observer").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := []string{
		"github.com/belphemur/limen/internal/portal",
		"github.com/belphemur/limen/internal/admin",
		"github.com/belphemur/limen/internal/zitadel",
		"github.com/belphemur/limen/internal/signup",
		"github.com/belphemur/limen/internal/oauthproxy",
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		for _, bad := range forbidden {
			if line == bad || strings.HasPrefix(line, bad+"/") {
				t.Errorf("cmd/observer must not import %q (got %q in dependency graph)", bad, line)
			}
		}
	}
}
