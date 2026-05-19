package portalv1guard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Tenant is read from the URL {tenant} segment by tenancyInterceptor and
// placed in ctx — request payloads MUST NOT carry it. This guard is the
// machine-checked half of that policy.
func TestProto_NoTenantIDFieldOnRequests(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	dir := filepath.Join(root, "proto", "limen", "portal", "v1")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	// Match a proto field declaration that names `tenant_id` (any type).
	// Field syntax: `<type> tenant_id = <n>;`. We also reject `string tenant_id`,
	// `int64 tenant_id`, etc.
	fieldRe := regexp.MustCompile(`(?m)^\s*(?:repeated\s+)?[A-Za-z0-9_.]+\s+tenant_id\s*=\s*\d+\s*;`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".proto") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if loc := fieldRe.FindIndex(b); loc != nil {
			snippet := strings.TrimSpace(string(b[loc[0]:loc[1]]))
			t.Errorf("%s: forbidden tenant_id field: %q\nTenant is resolved from the URL {tenant} segment by the portal interceptor stack; payloads must not carry it.", path, snippet)
		}
	}
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", os.ErrNotExist
		}
		wd = parent
	}
}
