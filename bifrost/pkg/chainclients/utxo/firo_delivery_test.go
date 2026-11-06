package utxo

import (
	"encoding/hex"
	"errors"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

// Both customer deposits and allowance-exempt migrations must survive a lost
// handoff. Restart with early scanning disabled and use actual FIRO block parsing.
func (s *FiroSuite) TestFiroDeliveryRestartUsesChainLockFallback(c *C) {
	for _, vaultSender := range []bool{false, true} {
		for _, outcome := range []string{"missing", "rejected", "stored", "write failure"} {
			func() {
				c.Logf("vault=%t, handoff=%s", vaultSender, outcome)
				client, f, _, cleanup := newFiroInstantSendFixture(c)
				defer cleanup()
				tx := f.instantDeposit(200, 12, 1)
				if vaultSender {
					parent := f.txs[tx.Vin[0].Txid]
					parent.Vout[0].ScriptPubKey.Hex = hex.EncodeToString(f.vault.Script())
					address, err := ttypes.GetRandomPubKey().GetAddress(common.FIROChain)
					c.Assert(err, IsNil)
					vault, err := firoutil.DecodeAddress(address.String(), common.FIRONetwork())
					c.Assert(err, IsNil)
					tx.Vout[0].ScriptPubKey.Hex = hex.EncodeToString(vault.Script())
					client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{common.Address(f.vault.String()), address}, FetchedAt: time.Now()})
					client.instantSend.maxAmount = 0
				}
				path := c.MkDir()
				storage, err := blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
				c.Assert(err, IsNil)
				defer func() { _ = storage.Close() }()
				client.cfg.BlockScanner.DBPath = path
				client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 0)
				c.Assert(err, IsNil)
				readyFiroInstantSend(c, client)
				batch, err := client.instantSend.fetchBatch(instantSendAdmission{allowed: true})
				c.Assert(err, IsNil)
				c.Assert(batch.TxArray, HasLen, 1)
				c.Assert(batch.TxArray[0].BlockHeight, Equals, int64(12))
				switch outcome {
				case "rejected":
					batch.TxArray[0].OnObservedTxRejected(errors.New("inactive observer"))
				case "stored":
					batch.TxArray[0].OnObservedTxStored()
				}
				state, err := client.temporalStorage.GetInstantSendState()
				c.Assert(err, IsNil)
				reserved := uint64(common.One)
				if vaultSender {
					reserved = 0
				}
				c.Assert(state.Reserved, Equals, reserved)
				c.Assert(storage.Close(), IsNil)
				if outcome == "write failure" {
					batch.TxArray[0].OnObservedTxStored() // callback captured the closed database
				}
				storage, err = blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
				c.Assert(err, IsNil)
				client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 0)
				c.Assert(err, IsNil)
				client.cfg.InstantSend.Enabled = false
				client.cfg.BlockScanner.ScanMemPool = false
				client.initInstantSend()
				tx.Height, tx.BlockHash, tx.ChainLock, tx.Confirmations = 99, firoHash(99), true, 1
				block, err := client.getBlockFIRO(99)
				c.Assert(err, IsNil)
				batch, err = client.extractTxs(block)
				c.Assert(err, IsNil)
				want := 1
				if outcome == "stored" {
					want = 0
				}
				c.Assert(batch.TxArray, HasLen, want)
				if want == 1 {
					c.Assert(batch.TxArray[0].Tx, Equals, tx.Txid)
					c.Assert(batch.TxArray[0].BlockHeight, Equals, int64(99))
				}
				state, err = client.temporalStorage.GetInstantSendState()
				c.Assert(err, IsNil)
				c.Assert(state.Reserved, Equals, uint64(0))
				batch, err = client.extractTxs(block)
				c.Assert(err, IsNil)
				c.Assert(batch.TxArray, HasLen, 0)
			}()
		}
	}
}

func (s *FiroSuite) TestFiroInstantSendRequiresPersistentStorage(c *C) {
	client, f, _, cleanup := newFiroInstantSendFixture(c)
	defer cleanup()
	readyFiroInstantSend(c, client)
	tx := f.instantDeposit(200, 12, 1)
	client.cfg.BlockScanner.DBPath = ""
	for _, vaultSender := range []bool{false, true} {
		if vaultSender {
			f.txs[tx.Vin[0].Txid].Vout[0].ScriptPubKey.Hex = hex.EncodeToString(f.vault.Script())
		}
		batch, err := client.instantSend.fetchBatch(instantSendAdmission{allowed: true})
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 0)
		items, err := client.instantSend.claimBatch([]*types.TxInItem{{Tx: tx.Txid}}, instantSendAdmission{allowed: true})
		c.Assert(err, IsNil)
		c.Assert(items, HasLen, 0)
		marked, err := client.temporalStorage.HasObservedTx(tx.Txid)
		c.Assert(err, IsNil)
		c.Assert(marked, Equals, false)
	}
}

// Admission can finish recovery before a reserved transaction is mined. The
// first block must restart inclusion checks for transactions in earlier blocks.
func (s *FiroSuite) TestFiroFirstBlockRestartsReservationRecovery(c *C) {
	client, f, _, cleanup := newFiroInstantSendFixture(c)
	defer cleanup()
	readyFiroInstantSend(c, client)
	tx := f.instantDeposit(200, 12, 1)
	batch, err := client.instantSend.fetchBatch(instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	client.instantSend.recovery.Reset()
	_, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.recovery.loaded, Equals, true)
	tx.Height, tx.BlockHash, tx.ChainLock, tx.Confirmations = 98, firoHash(98), true, 2
	c.Assert(client.instantSend.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: 99}), IsNil)
	c.Assert(client.instantSend.recovery.loaded, Equals, false)
	_, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(0))
}
