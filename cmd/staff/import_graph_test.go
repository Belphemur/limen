// Phase 9a sanity check: the staff binary excludes internal/oauthproxy
// (DCR is portal-owned) and the MCP gateway hot-path packages — staff
// surfaces never serve MCP traffic. internal/zitadel IS expected
// (impersonation + audit land in Phase 12 via the admin client).

package main_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestStaff_ImportGraph_ExcludesOauthproxyAndMCPHotPath(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/belphemur/limen/cmd/staff").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := []string{
		"github.com/belphemur/limen/internal/oauthproxy",
		"github.com/belphemur/limen/internal/gateway/codemode",
		"github.com/belphemur/limen/internal/portal",
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		for _, bad := range forbidden {
			if line == bad || strings.HasPrefix(line, bad+"/") {
				t.Errorf("cmd/staff must not import %q (got %q in dependency graph)", bad, line)
			}
		}
	}
}
