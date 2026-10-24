package rpc

import (
	"encoding/json"
	"sort"
)

// rpcInnerInstructions is one entry of a transaction's meta.innerInstructions: the
// instructions invoked (CPIs) while executing top-level instruction Index, in order.
type rpcInnerInstructions struct {
	Index        int              `json:"index"`
	Instructions []RPCInstruction `json:"instructions"`
}

// ParseInnerInstructions decodes meta.innerInstructions into one list in execution
// order: by top-level instruction index, then in invocation order within it. As for
// token balances, the meta field stays untyped in RPCMeta so an unexpected entry can
// never fail getTransaction decoding; an entry without the expected shape is skipped,
// and callers treat what they cannot see as not there.
func ParseInnerInstructions(raw []interface{}) []RPCInstruction {
	entries := make([]rpcInnerInstructions, 0, len(raw))
	for _, entry := range raw {
		b, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		var ii rpcInnerInstructions
		if err := json.Unmarshal(b, &ii); err != nil {
			continue
		}
		entries = append(entries, ii)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Index < entries[j].Index })
	var out []RPCInstruction
	for _, e := range entries {
		out = append(out, e.Instructions...)
	}
	return out
}
