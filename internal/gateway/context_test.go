package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateContextBlob_AcceptsEmpty(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(""), []byte("{}")} {
		got, err := ValidateContextBlob(raw)
		if err != nil {
			t.Fatalf("raw=%q: unexpected error: %v", raw, err)
		}
		if got == nil || len(got) != 0 {
			t.Errorf("raw=%q: want empty non-nil map, got %#v", raw, got)
		}
	}
}

func TestValidateContextBlob_AcceptsObjectRoot(t *testing.T) {
	raw := []byte(`{"cloudId":"abc","defaultProject":"OP"}`)
	got, err := ValidateContextBlob(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["cloudId"] != "abc" || got["defaultProject"] != "OP" {
		t.Errorf("unexpected decoded value: %#v", got)
	}
}

func TestValidateContextBlob_RejectsNonObjectRoot(t *testing.T) {
	cases := map[string]string{
		"array":  `[1,2,3]`,
		"string": `"hello"`,
		"number": `42`,
		"bool":   `true`,
		"null":   `null`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateContextBlob([]byte(raw)); err == nil {
				t.Fatalf("expected error for %s root", name)
			}
		})
	}
}

func TestValidateContextBlob_RejectsInvalidJSON(t *testing.T) {
	if _, err := ValidateContextBlob([]byte(`{not-json`)); err == nil {
		t.Fatal("expected JSON error")
	}
}

func TestValidateContextBlob_RejectsOversize(t *testing.T) {
	// Build a blob that is just over the cap.
	pad := strings.Repeat("a", MaxContextBlobBytes)
	raw := []byte(`{"k":"` + pad + `"}`)
	if len(raw) <= MaxContextBlobBytes {
		t.Fatalf("test setup: raw not oversize (%d)", len(raw))
	}
	if _, err := ValidateContextBlob(raw); err == nil {
		t.Fatal("expected oversize error")
	}
}

func TestValidateContextBlob_RejectsBadKeyShape(t *testing.T) {
	cases := map[string]string{
		"hyphen":        `{"cloud-id":"abc"}`,
		"space":         `{"cloud id":"abc"}`,
		"leading-digit": `{"1cloud":"abc"}`,
		"dot":           `{"cloud.id":"abc"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateContextBlob([]byte(raw)); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestValidateContextBlob_AcceptsValidKeyShapes(t *testing.T) {
	raw := []byte(`{"cloudId":"x","default_project":"y","_priv":1,"$alias":"z","a1":2}`)
	if _, err := ValidateContextBlob(raw); err != nil {
		t.Fatalf("unexpected error for valid keys: %v", err)
	}
}

func TestMergeContext_LinkOverridesDefaults(t *testing.T) {
	defaults := map[string]any{"cloudId": "abc", "defaultProject": "FALLBACK"}
	link := map[string]any{"defaultProject": "OP"}
	got := MergeContext(defaults, link)
	want := map[string]any{"cloudId": "abc", "defaultProject": "OP"}
	if !equalJSON(t, got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestMergeContext_EmptyBothSides(t *testing.T) {
	got := MergeContext(nil, nil)
	if got == nil {
		t.Fatal("expected non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestMergeContext_NestedValuesTakenWhole(t *testing.T) {
	defaults := map[string]any{"opts": map[string]any{"a": 1, "b": 2}}
	link := map[string]any{"opts": map[string]any{"c": 3}}
	got := MergeContext(defaults, link)
	inner, ok := got["opts"].(map[string]any)
	if !ok {
		t.Fatalf("opts should be a map: %#v", got["opts"])
	}
	if _, hasA := inner["a"]; hasA {
		t.Errorf("expected shallow merge to drop defaults.opts.a, got %#v", inner)
	}
	if inner["c"] != 3 {
		t.Errorf("expected link.opts.c to win, got %#v", inner)
	}
}

func TestSafeLoadContextBlob_DiscardsInvalid(t *testing.T) {
	cases := map[string][]byte{
		"bad-json": []byte(`{not-json`),
		"array":    []byte(`[1,2,3]`),
		"scalar":   []byte(`42`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			m, ok := SafeLoadContextBlob(raw)
			if ok {
				t.Errorf("expected ok=false for %s", name)
			}
			if m == nil || len(m) != 0 {
				t.Errorf("expected empty map, got %#v", m)
			}
		})
	}
}

func TestSafeLoadContextBlob_AcceptsValid(t *testing.T) {
	m, ok := SafeLoadContextBlob([]byte(`{"k":"v"}`))
	if !ok {
		t.Fatal("expected ok=true")
	}
	if m["k"] != "v" {
		t.Errorf("got %v", m)
	}
}

func equalJSON(t *testing.T, a, b any) bool {
	t.Helper()
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
