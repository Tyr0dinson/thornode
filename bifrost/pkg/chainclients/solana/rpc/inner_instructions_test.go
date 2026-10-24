package rpc

import (
	"encoding/json"
	"testing"
)

// TestParseInnerInstructions: inner instructions come back in execution order (by
// top-level instruction index, then invocation order) whatever order the RPC lists
// them in, and an entry without the expected shape is skipped rather than failing.
func TestParseInnerInstructions(t *testing.T) {
	var raw []interface{}
	if err := json.Unmarshal([]byte(`[
		{"index": 2, "instructions": [{"programIdIndex": 5, "accounts": [1, 2], "data": "c", "stackHeight": 2}]},
		"not an object",
		{"index": 0, "instructions": [
			{"programIdIndex": 3, "accounts": [0], "data": "a", "stackHeight": 2},
			{"programIdIndex": 4, "accounts": [0, 1], "data": "b", "stackHeight": 3}
		]},
		{"index": 1, "instructions": "not a list"}
	]`), &raw); err != nil {
		t.Fatal(err)
	}

	got := ParseInnerInstructions(raw)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %d instructions, want %d", len(got), len(want))
	}
	for i, ins := range got {
		if ins.Data != want[i] {
			t.Errorf("instruction %d: data %q, want %q", i, ins.Data, want[i])
		}
	}
	if got[1].ProgramIdIndex != 4 || len(got[1].Accounts) != 2 {
		t.Errorf("instruction 1 decoded as %+v", got[1])
	}

	if len(ParseInnerInstructions(nil)) != 0 {
		t.Error("no inner instructions should parse to none")
	}
}
