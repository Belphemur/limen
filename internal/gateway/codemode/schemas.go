package codemode

import "github.com/dop251/goja"

// ToolSchema is the full schema for one tool, returned by
// codemode.schemas(names). Includes Upstream so a single round-trip
// gives the LLM everything it needs to invoke the tool from
// codemode_execute.
type ToolSchema struct {
	Name        string         `json:"name"`
	Upstream    string         `json:"upstream"`
	InputSchema map[string]any `json:"inputSchema"`
}

// SchemasResult is the shape returned by codemode.schemas(names): a
// list of resolved schemas plus the names that did not match any
// visible tool. Surfacing misses (instead of silently dropping them)
// catches typos in scripts.
type SchemasResult struct {
	Found   []ToolSchema `json:"found"`
	Missing []string     `json:"missing"`
}

// exportSchemaNames coerces the first argument of codemode.schemas
// into a flat []string. Accepts a single string for one-shot lookups
// and an array of strings for batched ones. Returns nil for anything
// else so the caller can reject the call with a clear error.
func exportSchemaNames(v goja.Value) []string {
	switch x := v.Export().(type) {
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil
			}
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}

// schemasByName looks up tool schemas by exact name. Unknown names are
// returned in the Missing slice so callers can detect typos without
// scanning Found for absence.
func schemasByName(in []Tool, names []string) SchemasResult {
	byName := make(map[string]Tool, len(in))
	for _, t := range in {
		byName[t.Name] = t
	}
	out := SchemasResult{
		Found:   make([]ToolSchema, 0, len(names)),
		Missing: []string{},
	}
	for _, n := range names {
		if t, ok := byName[n]; ok {
			out.Found = append(out.Found, ToolSchema{Name: t.Name, Upstream: t.Upstream, InputSchema: t.InputSchema})
			continue
		}
		out.Missing = append(out.Missing, n)
	}
	return out
}
