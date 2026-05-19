// Phase 9a sanity check: the portal binary's transitive imports MUST
// include internal/oauthproxy + internal/zitadel + the OIDC RP — those
// are precisely the suites the portal owns. If the portal stops
// importing one of these, the route is no longer mounted and an
// integration regression has crept in.

package main_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPortal_ImportGraph_IncludesPortalOwnedSuites(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/belphemur/limen/cmd/portal").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	required := []string{
		"github.com/belphemur/limen/internal/oauthproxy",
		"github.com/belphemur/limen/internal/zitadel",
		"github.com/belphemur/limen/internal/auth",
	}
	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, want := range required {
		found := false
		for _, line := range deps {
			if line == want || strings.HasPrefix(line, want+"/") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("cmd/portal MUST import %q (not present in dependency graph)", want)
		}
	}
}
