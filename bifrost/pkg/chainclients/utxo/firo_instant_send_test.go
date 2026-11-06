package utxo

import (
	"encoding/hex"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

func (f *firoFixture) instantDeposit(n, parentHeight int64, amount float64) *rpc.FiroTxResult {
	parent := f.funding(n+10000, parentHeight, amount+0.001, f.user)
	tx := f.funding(n, 0, amount, f.vault)
	tx.BlockHash, tx.Confirmations, tx.ChainLock, tx.InstantLock = "", 0, false, true
	tx.Vin = []btcjson.Vin{{Txid: parent.Txid}}
	f.mempool = append(f.mempool, tx.Txid)
	return tx
}

func newFiroInstantSendFixture(c *C) (*Client, *firoFixture, *dashHealthFetcher, func()) {
	client, f, cleanup := newFiroFixture(c)
	client.bridge = &instantSendPolicyBridge{status: ttypes.NodeStatus_Active, values: map[string]int64{
		FiroInstantSendEnabledMimirKey: 1, FiroInstantSendMaxAmountMimirKey: 2 * common.One,
	}}
	client.cfg.InstantSend.MaxMempoolAge = 2 * time.Minute
	client.cfg.UTXO.TransactionBatchSize = 20
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	_, err := client.GetHeight()
	c.Assert(err, IsNil)
	fetcher := &dashHealthFetcher{}
	fetcher.height.Store(f.locked)
	stop := startInstantSendWarmupScanner(c, client, fetcher)
	return client, f, fetcher, func() { stop(); cleanup() }
}

func readyFiroInstantSend(c *C, client *Client) {
	c.Assert(client.temporalStorage.StartInstantSendWarmup(1, nil), IsNil)
	c.Assert(client.temporalStorage.CompleteInstantSendWarmup(2), IsNil)
	client.instantSend.recovery.loaded = true
}

func (s *FiroSuite) TestFiroInstantSendAnchorAndFinalClaim(c *C) {
	client, f, _, cleanup := newFiroInstantSendFixture(c)
	defer cleanup()
	readyFiroInstantSend(c, client)
	tx := f.instantDeposit(200, 12, 1)
	parent2 := f.funding(300, 40, 0.5, f.user)
	tx.Vin = append(tx.Vin, btcjson.Vin{Txid: parent2.Txid})
	groups, err := client.instantSend.scan()
	c.Assert(err, IsNil)
	c.Assert(groups, HasLen, 1)
	c.Assert(groups[0].MemPool, Equals, false)
	c.Assert(groups[0].ConfirmationRequired, Equals, int64(0))
	c.Assert(groups[0].TxArray[0].BlockHeight, Equals, int64(40))
	// The cursor is not the reference height, and input ordering cannot change it.
	tx.Vin[0], tx.Vin[1] = tx.Vin[1], tx.Vin[0]
	again, err := client.instantSend.scan()
	c.Assert(err, IsNil)
	c.Assert(again, DeepEquals, groups)
	// Evidence can disappear while a batch waits in the queue. Recheck all parents.
	client.instantSend.txQueue = groups
	parent2.ChainLock = false
	batch, err := client.instantSend.fetchBatch(instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0)
	marked, err := client.temporalStorage.HasObservedTx(tx.Txid)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(0))
	parent2.ChainLock = true
	batch, err = client.instantSend.fetchBatch(instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].BlockHeight, Equals, int64(40))
}

func (s *FiroSuite) TestFiroInstantSendDefersUnsupportedEvidence(c *C) {
	for _, scenario := range []string{"unlocked", "special", "privacy", "unconfirmed_parent", "unlocked_parent", "fork_parent", "missing_parent", "rpc_failure", "unknown_script", "expired", "stalled_chainlock", "unhealthy_scanner"} {
		client, f, _, cleanup := newFiroInstantSendFixture(c)
		tx := f.instantDeposit(200, 12, 1)
		parent := f.txs[tx.Vin[0].Txid]
		switch scenario {
		case "unlocked":
			tx.InstantLock = false
		case "special":
			tx.Type = 9
		case "privacy":
			tx.Vin[0].Txid = firoHash(0)
		case "unconfirmed_parent":
			parent.BlockHash, parent.Confirmations, parent.ChainLock = "", 0, false
			parent.InstantLock = true
		case "unlocked_parent":
			parent.ChainLock = false
		case "fork_parent":
			parent.BlockHash = firoHash(9999)
		case "missing_parent":
			f.failTx = parent.Txid
		case "rpc_failure":
			f.failMethod = "getblockheader"
		case "unknown_script":
			tx.Vout[0].ScriptPubKey.Hex = "51"
		case "expired":
			locked := true
			f.entries = map[string]rpc.FiroMempoolEntry{tx.Txid: {InstantLock: &locked, Time: time.Now().Add(-3 * time.Minute).Unix()}}
		case "stalled_chainlock":
			client.finality.Store(&finalitySnapshot{blockHeight: 102, chainLockHeight: 99, checkedAt: time.Now()})
		case "unhealthy_scanner":
			client.blockScanner = nil
		}
		groups, err := client.instantSend.scan()
		c.Assert(err, IsNil, Commentf("%s", scenario))
		c.Assert(groups, HasLen, 0, Commentf("%s", scenario))
		cleanup()
	}
}

func (s *FiroSuite) TestFiroInstantSendAllowanceAndSettlement(c *C) {
	client, f, _, cleanup := newFiroInstantSendFixture(c)
	defer cleanup()
	readyFiroInstantSend(c, client)
	first, second := f.instantDeposit(200, 12, 1.5), f.instantDeposit(201, 12, 1.5)
	_, err := client.FetchMemPool(99) // ordinary outbounds first
	c.Assert(err, IsNil)
	batch, err := client.FetchMemPool(99)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, first.Txid)
	batch.TxArray[0].OnObservedTxStored() // this test models successful Observer delivery
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(150000000))
	// A scanner gap triggers recovery; neither eviction nor a lost lock releases funds.
	client.instantSend.lastBlockHeight = 90
	c.Assert(client.instantSend.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: 99}), IsNil)
	c.Assert(client.instantSend.recovery.loaded, Equals, false)
	f.failTx = first.Txid
	admission, err := client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, true)
	f.failTx = ""
	first.InstantLock = false
	client.instantSend.recovery.Reset()
	_, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(150000000))
	// Both reach a verified block. Settlement happens before observation dedup.
	for _, tx := range []*rpc.FiroTxResult{first, second} {
		tx.Height, tx.BlockHash, tx.ChainLock, tx.Confirmations = 99, firoHash(99), true, 1
	}
	block, err := client.getBlockFIRO(99)
	c.Assert(err, IsNil)
	client.instantSend.enabled = false
	batch, err = client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, second.Txid)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(0))
	batch, err = client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0)
}

func (s *FiroSuite) TestFiroInstantSendSnapshotAndRestart(c *C) {
	client, f, fetcher, cleanup := newFiroInstantSendFixture(c)
	defer cleanup()
	tx := f.instantDeposit(200, 12, 1)
	// Unknown native status cannot start a new snapshot.
	f.entries = map[string]rpc.FiroMempoolEntry{tx.Txid: {}}
	_, err := client.instantSend.prepareAllowance(nil)
	c.Assert(err, ErrorMatches, ".*lock status unavailable.*")
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Started, Equals, false)
	f.entries = nil
	admission, err := client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, false)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(common.One))
	fetcher.height.Store(100)
	waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 100 })
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, true)
	// Snapshot deposits remain charged but are not emitted a second time.
	batch, err := client.instantSend.fetchBatch(admission)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0)
	// A fresh handler restores the durable journal and independently verifies inclusion.
	client.initInstantSend()
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	tx.Height, tx.BlockHash, tx.ChainLock, tx.Confirmations = 99, firoHash(99), true, 1
	f.failMethod = "getblock"
	_, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, NotNil)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(common.One))
	f.failMethod = ""
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, true)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(0))
}

func (s *FiroSuite) TestFiroInstantSendBalanceIsIndependentOfAdmission(c *C) {
	client, f, cleanup := newFiroFixture(c)
	defer cleanup()
	tx := f.instantDeposit(200, 12, 1)
	u := f.listed(tx)
	u.Confirmations = 0
	f.utxos = []btcjson.ListUnspentResult{u}
	// Default disabled, no allowance journal and no observation marker.
	account, err := client.getAccountFIRO(f.pubkey)
	c.Assert(err, IsNil)
	c.Assert(account.Coins.GetCoin(common.FIROAsset).Amount.Uint64(), Equals, uint64(common.One))
	selected, err := client.getUtxoToSpendFIRO(f.pubkey, btcutil.Amount(common.One/2), 0)
	c.Assert(err, ErrorMatches, ".*insufficient confirmed FIRO inputs.*")
	c.Assert(selected, HasLen, 0)
	tx.InstantLock = false
	account, err = client.getAccountFIRO(f.pubkey)
	c.Assert(err, IsNil)
	c.Assert(account.Coins.GetCoin(common.FIROAsset).Amount.IsZero(), Equals, true)
	tx.InstantLock = true
	f.failTx = tx.Vin[0].Txid
	_, err = client.getAccountFIRO(f.pubkey)
	c.Assert(err, NotNil) // unavailable evidence must not publish a reduced balance
}

func (s *FiroSuite) TestFiroInstantSendPolicyAndVaultExemption(c *C) {
	client, f, _, cleanup := newFiroInstantSendFixture(c)
	defer cleanup()
	readyFiroInstantSend(c, client)
	tx := f.instantDeposit(200, 12, 1)
	bridge, ok := client.bridge.(*instantSendPolicyBridge)
	c.Assert(ok, Equals, true)
	delete(bridge.values, FiroInstantSendEnabledMimirKey)
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.enabled, Equals, false)
	c.Assert(client.instantSend.minAmount, Equals, uint64(1000000))
	// The launch Mimir overrides an accidentally enabled startup environment.
	client.cfg.InstantSend.Enabled = true
	bridge.values[FiroInstantSendEnabledMimirKey] = 0
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.enabled, Equals, false)
	client.cfg.InstantSend.Enabled = false
	bridge.values[FiroInstantSendEnabledMimirKey] = 1
	bridge.values[FiroInstantSendMinAmountMimirKey] = 2 * common.One
	bridge.values[FiroInstantSendMaxAmountMimirKey] = 0
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	// Customer deposits cannot use a zero allowance.
	batch, err := client.instantSend.fetchBatch(instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0)
	parent := f.funding(400, 15, 2, f.vault)
	tx.Vin[0].Txid = parent.Txid
	other, err := ttypes.GetRandomPubKey().GetAddress(common.FIROChain)
	c.Assert(err, IsNil)
	otherVault, err := firoutil.DecodeAddress(other.String(), common.FIRONetwork())
	c.Assert(err, IsNil)
	tx.Vout[0].ScriptPubKey.Hex = hex.EncodeToString(otherVault.Script())
	client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{common.Address(f.vault.String()), other}, FetchedAt: time.Now()})
	groups, err := client.instantSend.scan()
	c.Assert(err, IsNil)
	c.Assert(groups, HasLen, 1)
	// Vault migrations bypass both amounts, but never halt settings.
	bridge.values["HaltFIROChain"] = 1
	client.instantSend.txQueue = groups
	_, err = client.instantSend.fetchBatch(instantSendAdmission{})
	c.Assert(err, Equals, errInstantSendPaused)
	delete(bridge.values, "HaltFIROChain")
	batch, err = client.instantSend.fetchBatch(instantSendAdmission{})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(0))
	// Payments from a vault to a customer stay on the ordinary early outbound path.
	tx2 := f.funding(401, 0, 1, f.user)
	tx2.BlockHash, tx2.Confirmations, tx2.ChainLock, tx2.InstantLock = "", 0, false, true
	tx2.Vin = []btcjson.Vin{{Txid: parent.Txid}}
	f.mempool = []string{tx2.Txid}
	groups, err = client.instantSend.scan()
	c.Assert(err, IsNil)
	c.Assert(groups, HasLen, 0)
	batch, err = client.instantSend.backend.outbounds(99)
	c.Assert(err, IsNil)
	c.Assert(batch.MemPool, Equals, true)
	c.Assert(batch.TxArray, HasLen, 1)
}

// Simulate settlement completing while a fresh FIRO parent proof is in flight.
type firoClaimSettlementBackend struct {
	instantSendBackend
	h *instantSendHandler
}

func (b firoClaimSettlementBackend) checkClaimFinality(item *types.TxInItem, tx *rpc.TxResult) (valid, fromVault bool, err error) {
	valid, fromVault, err = b.instantSendBackend.checkClaimFinality(item, tx)
	if err == nil && valid {
		err = b.h.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: 100, Tx: []btcjson.TxRawResult{tx.TxRawResult}})
	}
	return valid, fromVault, err
}

func (s *FiroSuite) TestFiroInstantSendRejectsClaimInvalidatedBySettlement(c *C) {
	client, f, _, cleanup := newFiroInstantSendFixture(c)
	defer cleanup()
	readyFiroInstantSend(c, client)
	tx := f.instantDeposit(200, 12, 1)
	client.instantSend.backend = firoClaimSettlementBackend{client.instantSend.backend, &client.instantSend}
	batch, err := client.instantSend.fetchBatch(instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0)
	marked, err := client.temporalStorage.HasObservedTx(tx.Txid)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
}
