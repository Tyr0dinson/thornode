package utxo

import (
	"github.com/btcsuite/btcd/btcjson"
	. "gopkg.in/check.v1"

	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

const (
	pendingTxA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pendingTxB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	pendingTxC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	pendingTxD = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	pendingTxP = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	pendingTxQ = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

func rpcResult(result interface{}) map[string]interface{} {
	return map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": result}
}

func rpcError(code int, msg string) map[string]interface{} {
	return map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"error":   map[string]interface{}{"code": code, "message": msg},
	}
}

func verboseTx(txid, blockhash string, vins []map[string]interface{}) map[string]interface{} {
	tx := map[string]interface{}{
		"txid": txid,
		"hex":  "00",
		"vin":  vins,
	}
	if blockhash != "" {
		tx["blockhash"] = blockhash
		tx["confirmations"] = 3
	}
	return rpcResult(tx)
}

func vin(txid string, vout int) map[string]interface{} {
	return map[string]interface{}{"txid": txid, "vout": vout, "sequence": 4294967295}
}

func (s *BitcoinSuite) trackPending(c *C, txid string, vins ...sharedutxo.PendingVaultTxOutpoint) {
	err := s.client.temporalStorage.TrackPendingVaultTx(sharedutxo.PendingVaultTx{
		TxID:            txid,
		Vins:            vins,
		FirstSeenHeight: 99,
	})
	c.Assert(err, IsNil)
}

func (s *BitcoinSuite) pendingIDs(c *C) map[string]bool {
	pending, err := s.client.temporalStorage.GetPendingVaultTxs()
	c.Assert(err, IsNil)
	ids := map[string]bool{}
	for _, tx := range pending {
		ids[tx.TxID] = true
	}
	return ids
}

func (s *BitcoinSuite) drainErrata(queue chan types.ErrataBlock) []types.ErrataTx {
	var txs []types.ErrataTx
	for {
		select {
		case block := <-queue:
			txs = append(txs, block.Txs...)
		default:
			return txs
		}
	}
}

func (s *BitcoinSuite) TestTrackPendingVaultTx(c *C) {
	btcChainRPCs["getrawtransaction-"+pendingTxA] = verboseTx(pendingTxA, "", []map[string]interface{}{vin(pendingTxP, 1)})
	defer delete(btcChainRPCs, "getrawtransaction-"+pendingTxA)

	s.client.trackPendingVaultTx(pendingTxA)
	pending, err := s.client.temporalStorage.GetPendingVaultTxs()
	c.Assert(err, IsNil)
	c.Assert(pending, HasLen, 1)
	c.Assert(pending[0].TxID, Equals, pendingTxA)
	c.Assert(pending[0].Vins, DeepEquals, []sharedutxo.PendingVaultTxOutpoint{{TxID: pendingTxP, Vout: 1}})

	// idempotent
	s.client.trackPendingVaultTx(pendingTxA)
	pending, err = s.client.temporalStorage.GetPendingVaultTxs()
	c.Assert(err, IsNil)
	c.Assert(pending, HasLen, 1)

	// already mined transactions are not watched
	btcChainRPCs["getrawtransaction-"+pendingTxB] = verboseTx(pendingTxB, "00000000000000000000000000000000000000000000000000000000000000aa", []map[string]interface{}{vin(pendingTxP, 1)})
	defer delete(btcChainRPCs, "getrawtransaction-"+pendingTxB)
	s.client.trackPendingVaultTx(pendingTxB)
	pending, err = s.client.temporalStorage.GetPendingVaultTxs()
	c.Assert(err, IsNil)
	c.Assert(pending, HasLen, 1)

	// unknown to the daemon: no watch possible
	btcChainRPCs["getrawtransaction-"+pendingTxC] = rpcError(-5, "No such mempool or blockchain transaction")
	defer delete(btcChainRPCs, "getrawtransaction-"+pendingTxC)
	s.client.trackPendingVaultTx(pendingTxC)
	pending, err = s.client.temporalStorage.GetPendingVaultTxs()
	c.Assert(err, IsNil)
	c.Assert(pending, HasLen, 1)
}

func (s *BitcoinSuite) TestCheckPendingVaultTxs(c *C) {
	errataQueue := make(chan types.ErrataBlock, 10)
	s.client.globalErrataQueue = errataQueue
	block := &btcjson.GetBlockVerboseTxResult{Height: 100}

	// watched tx mined in the scanned block: cleared, no errata
	s.trackPending(c, pendingTxA, sharedutxo.PendingVaultTxOutpoint{TxID: pendingTxP, Vout: 0})
	blockWithA := &btcjson.GetBlockVerboseTxResult{Height: 100, Tx: []btcjson.TxRawResult{{Txid: pendingTxA}}}
	s.client.checkPendingVaultTxs(blockWithA)
	c.Check(s.pendingIDs(c)[pendingTxA], Equals, false)
	c.Check(s.drainErrata(errataQueue), HasLen, 0)

	// watched tx mined elsewhere (e.g. during downtime): cleared via blockhash
	s.trackPending(c, pendingTxA, sharedutxo.PendingVaultTxOutpoint{TxID: pendingTxP, Vout: 0})
	btcChainRPCs["getrawtransaction-"+pendingTxA] = verboseTx(pendingTxA, "00000000000000000000000000000000000000000000000000000000000000aa", nil)
	s.client.checkPendingVaultTxs(block)
	c.Check(s.pendingIDs(c)[pendingTxA], Equals, false)
	c.Check(s.drainErrata(errataQueue), HasLen, 0)
	delete(btcChainRPCs, "getrawtransaction-"+pendingTxA)

	// watched tx still in mempool: kept, no errata
	s.trackPending(c, pendingTxA, sharedutxo.PendingVaultTxOutpoint{TxID: pendingTxP, Vout: 0})
	btcChainRPCs["getrawtransaction-"+pendingTxA] = verboseTx(pendingTxA, "", nil)
	s.client.checkPendingVaultTxs(block)
	c.Check(s.pendingIDs(c)[pendingTxA], Equals, true)
	c.Check(s.drainErrata(errataQueue), HasLen, 0)
	delete(btcChainRPCs, "getrawtransaction-"+pendingTxA)
	c.Assert(s.client.temporalStorage.UntrackPendingVaultTx(pendingTxA), IsNil)

	// watched tx evicted but inputs still unspent on-chain: kept, no errata
	s.trackPending(c, pendingTxA, sharedutxo.PendingVaultTxOutpoint{TxID: pendingTxP, Vout: 0})
	btcChainRPCs["getrawtransaction-"+pendingTxA] = rpcError(-5, "No such mempool or blockchain transaction")
	btcChainRPCs["gettxout"] = rpcResult(map[string]interface{}{"bestblock": "aa", "confirmations": 5, "value": 0.5})
	s.client.checkPendingVaultTxs(block)
	c.Check(s.pendingIDs(c)[pendingTxA], Equals, true)
	c.Check(s.drainErrata(errataQueue), HasLen, 0)
	delete(btcChainRPCs, "gettxout")
	c.Assert(s.client.temporalStorage.UntrackPendingVaultTx(pendingTxA), IsNil)

	// watched tx gone and its input spent on-chain by a mined transaction:
	// invalidated, errata attested, untracked. The vin's parent resolves via the
	// generic getrawtransaction fixture, which is mined.
	s.trackPending(c, pendingTxA, sharedutxo.PendingVaultTxOutpoint{TxID: pendingTxP, Vout: 0})
	btcChainRPCs["gettxout"] = rpcResult(nil)
	s.client.checkPendingVaultTxs(block)
	c.Check(s.pendingIDs(c)[pendingTxA], Equals, false)
	errata := s.drainErrata(errataQueue)
	c.Assert(errata, HasLen, 1)
	c.Check(errata[0].TxID.String(), Equals, pendingTxA)
	c.Check(errata[0].Chain.Equals(s.client.GetChain()), Equals, true)
	delete(btcChainRPCs, "getrawtransaction-"+pendingTxA)
	delete(btcChainRPCs, "gettxout")
}

func (s *BitcoinSuite) TestCheckPendingVaultTxsCascade(c *C) {
	errataQueue := make(chan types.ErrataBlock, 10)
	s.client.globalErrataQueue = errataQueue
	block := &btcjson.GetBlockVerboseTxResult{Height: 100}

	// parent is invalidated (gone, input spent on-chain); child spends the
	// parent's change output and can likewise never land: both must be
	// errata'd, the child via the descendant cascade.
	s.trackPending(c, pendingTxP, sharedutxo.PendingVaultTxOutpoint{TxID: pendingTxQ, Vout: 0})
	s.trackPending(c, pendingTxC, sharedutxo.PendingVaultTxOutpoint{TxID: pendingTxP, Vout: 1})
	btcChainRPCs["getrawtransaction-"+pendingTxP] = rpcError(-5, "No such mempool or blockchain transaction")
	btcChainRPCs["getrawtransaction-"+pendingTxC] = rpcError(-5, "No such mempool or blockchain transaction")
	defer delete(btcChainRPCs, "getrawtransaction-"+pendingTxP)
	defer delete(btcChainRPCs, "getrawtransaction-"+pendingTxC)
	// all outputs spent; the grandparent (pendingTxQ) resolves as mined via the
	// generic getrawtransaction fixture
	btcChainRPCs["gettxout"] = rpcResult(nil)
	defer delete(btcChainRPCs, "gettxout")

	s.client.checkPendingVaultTxs(block)
	errata := s.drainErrata(errataQueue)
	c.Assert(errata, HasLen, 2)
	ids := map[string]bool{}
	for _, tx := range errata {
		ids[tx.TxID.String()] = true
	}
	c.Check(ids[pendingTxP], Equals, true)
	c.Check(ids[pendingTxC], Equals, true)
	c.Check(s.pendingIDs(c), HasLen, 0)
}

func (s *BitcoinSuite) TestCheckPendingVaultTxsUnsupportedRPC(c *C) {
	errataQueue := make(chan types.ErrataBlock, 10)
	s.client.globalErrataQueue = errataQueue
	block := &btcjson.GetBlockVerboseTxResult{Height: 100}

	// a daemon without gettxout (e.g. Zebra) disables invalidation detection
	// but keeps the watch; mined clearing still works.
	s.trackPending(c, pendingTxA, sharedutxo.PendingVaultTxOutpoint{TxID: pendingTxP, Vout: 0})
	btcChainRPCs["getrawtransaction-"+pendingTxA] = rpcError(-5, "No such mempool or blockchain transaction")
	defer delete(btcChainRPCs, "getrawtransaction-"+pendingTxA)
	btcChainRPCs["gettxout"] = rpcError(-32601, "the method gettxout does not exist/is not available")
	defer delete(btcChainRPCs, "gettxout")

	s.client.checkPendingVaultTxs(block)
	c.Check(s.client.pendingVaultTxCheckUnsupported.Load(), Equals, true)
	c.Check(s.pendingIDs(c)[pendingTxA], Equals, true)
	c.Check(s.drainErrata(errataQueue), HasLen, 0)
	s.client.pendingVaultTxCheckUnsupported.Store(false)
}
