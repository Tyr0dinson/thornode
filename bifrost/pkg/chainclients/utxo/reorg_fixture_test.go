package utxo

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/btcsuite/btcd/btcjson"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
)

// rpcFixtureKey is the fixture key a mock daemon looks up first: methods whose answer
// depends on their first parameter (a txid, a height or a block hash) are keyed by
// it. JSON numbers decode as float64, so heights are formatted as integers. Mocks
// fall back to the plain method key when no keyed fixture exists.
func rpcFixtureKey(method string, params []interface{}) string {
	switch method {
	case "getrawtransaction", "getblockhash", "getblock":
		if len(params) > 0 {
			if f, ok := params[0].(float64); ok {
				return method + "-" + strconv.FormatInt(int64(f), 10)
			}
			return fmt.Sprintf("%s-%v", method, params[0])
		}
	}
	return method
}

// reorgParent makes a mock daemon answer like one on the new fork: its block at
// height is hash (getblockhash), and getblock(hash) returns the default block fixture
// relabelled as that block, whose own parent is parent. It returns a restore func.
func reorgParent(rpcs map[string]map[string]interface{}, height int64, hash, parent string) func() {
	hashKey := rpcFixtureKey("getblockhash", []interface{}{float64(height)})
	blockKey := rpcFixtureKey("getblock", []interface{}{hash})
	prevHash, prevBlock := rpcs[hashKey], rpcs[blockKey]

	rpcs[hashKey] = map[string]interface{}{"result": hash, "error": nil, "id": 1}
	block := map[string]interface{}{"error": nil, "id": 1}
	result := map[string]interface{}{}
	if base, ok := rpcs["getblock"]["result"].(map[string]interface{}); ok {
		for k, v := range base {
			result[k] = v
		}
	}
	result["hash"], result["height"], result["previousblockhash"] = hash, height, parent
	block["result"] = result
	rpcs[blockKey] = block

	return func() {
		restore := func(key string, v map[string]interface{}) {
			if v == nil {
				delete(rpcs, key)
				return
			}
			rpcs[key] = v
		}
		restore(hashKey, prevHash)
		restore(blockKey, prevBlock)
	}
}

// A "block height out of range" from a fallback daemon arrives redacted and wrapped;
// it must still be recognised, so the scanner retries the height quietly (as it does
// for the primary's) instead of logging an error and counting toward skip-ahead.
func (s *CoverageBackfillSuite) TestBlockUnavailableFromAFallbackIsRecognised(c *C) {
	outOfRange := btcjson.NewRPCError(btcjson.ErrRPCInvalidParameter, "Block height out of range")
	c.Assert(isBlockUnavailable(outOfRange), Equals, true)
	wrapped := endpoints.Redact(fmt.Errorf("endpoint b: %w", outOfRange), nil)
	c.Assert(isBlockUnavailable(wrapped), Equals, true)
	c.Assert(isBlockUnavailable(errors.New("connection refused")), Equals, false)
	c.Assert(isBlockUnavailable(btcjson.NewRPCError(btcjson.ErrRPCMisc, "other")), Equals, false)
}
