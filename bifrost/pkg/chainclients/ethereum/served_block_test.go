package ethereum

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sync"

	etypes "github.com/ethereum/go-ethereum/core/types"
	. "gopkg.in/check.v1"
)

// servedBlock lets a mock node answer like a real one: a receipt always carries the
// hash of the block that lists its transaction, and that hash is the one the client
// computes from the block header it was served (the scanner checks this).
type servedBlock struct {
	mu   sync.Mutex
	hash string
}

var receiptBlockHashRE = regexp.MustCompile(`"blockHash"\s*:\s*"0x[0-9a-fA-F]{64}"`)

// serve writes a canned eth_getBlockByNumber answer and remembers its header hash.
func (b *servedBlock) serve(c *C, rw http.ResponseWriter, answer string) {
	var env struct {
		Result etypes.Header `json:"result"`
	}
	c.Assert(json.Unmarshal([]byte(answer), &env), IsNil)
	b.mu.Lock()
	b.hash = env.Result.Hash().Hex()
	b.mu.Unlock()
	_, err := rw.Write([]byte(answer))
	c.Assert(err, IsNil)
}

// receipt rewrites a canned receipt (and its logs) to belong to the served block;
// before any block was served it is returned unchanged.
func (b *servedBlock) receipt(answer string) []byte {
	b.mu.Lock()
	hash := b.hash
	b.mu.Unlock()
	if hash == "" {
		return []byte(answer)
	}
	return []byte(receiptBlockHashRE.ReplaceAllString(answer, `"blockHash":"`+hash+`"`))
}
