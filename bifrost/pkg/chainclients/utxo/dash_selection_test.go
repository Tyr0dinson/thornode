package utxo

import (
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func (s *DashSuite) TestDashInputSelectionIgnoresLocks(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	pubKey, err := common.NewPubKeyFromCrypto(secp256k1.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	script := newDashP2PKHScriptHex(c, f.asgard)
	confirmed := btcjson.ListUnspentResult{TxID: strings.Repeat("9", 64), ScriptPubKey: script, Amount: 0.2, Confirmations: 3}
	f.utxos = []btcjson.ListUnspentResult{
		{TxID: f.inbound, ScriptPubKey: script, Amount: 0.1},
		confirmed,
	}
	client.cfg.UTXO.MinUTXOConfirmations = 1
	client.cfg.UTXO.MaxUTXOsToSpend = 2 // selection must consider both inputs

	for _, lockField := range []string{"unlocked", "instantlock", "instantlock_internal", "chainlock"} {
		c.Logf("local lock state: %s", lockField)
		for _, field := range []string{"instantlock", "instantlock_internal", "chainlock"} {
			f.txs[f.inbound][field] = field == lockField
		}
		selected, err := client.getUtxoToSpend(pubKey, 15_000_000)
		c.Assert(err, IsNil)
		c.Assert(selected, DeepEquals, []btcjson.ListUnspentResult{confirmed})

		// The unconfirmed deposit cannot fund an otherwise unaffordable outbound.
		_, err = client.getUtxoToSpend(pubKey, 25_000_000)
		c.Assert(err, ErrorMatches, "insufficient available UTXOs:.*")
	}

	f.utxos[0].Confirmations = 1
	selected, err := client.getUtxoToSpend(pubKey, 25_000_000)
	c.Assert(err, IsNil)
	c.Assert(selected, DeepEquals, []btcjson.ListUnspentResult{confirmed, f.utxos[0]})

	// Locks still cannot bypass the dust requirement, even after confirmation.
	f.utxos[0].Amount = 0.00000001
	selected, err = client.getUtxoToSpend(pubKey, 15_000_000)
	c.Assert(err, IsNil)
	c.Assert(selected, DeepEquals, []btcjson.ListUnspentResult{confirmed})

	// Keep the existing configuration semantics; do not impose a new hard minimum.
	f.utxos[0].Amount = 0.1
	f.utxos[0].Confirmations = 0
	f.txs[f.inbound]["chainlock"] = false
	client.cfg.UTXO.MinUTXOConfirmations = 0
	selected, err = client.getUtxoToSpend(pubKey, 25_000_000)
	c.Assert(err, IsNil)
	c.Assert(selected, HasLen, 2)
}

func (s *DashSuite) TestDashInputSelectionPreservesVaultExceptions(c *C) {
	for _, selfTransaction := range []bool{false, true} {
		func() {
			c.Logf("recorded self transaction: %v", selfTransaction)
			client, f, cleanup := newDashMempoolFixture(c)
			defer cleanup()
			pubKey, err := common.NewPubKeyFromCrypto(secp256k1.GenPrivKey().PubKey())
			c.Assert(err, IsNil)
			id := f.unlockedOutbound // recognized through its vault sender
			if selfTransaction {
				id = f.inbound // recognized only through the stored self-transaction marker
				f.txs[id]["instantlock"] = false
				meta := sharedutxo.NewBlockMeta("previous", 100, "current")
				meta.AddSelfTransaction(id)
				c.Assert(client.temporalStorage.SaveBlockMeta(100, meta), IsNil)
			}
			f.utxos = []btcjson.ListUnspentResult{{TxID: id, ScriptPubKey: newDashP2PKHScriptHex(c, f.asgard), Amount: 0.1}}
			client.cfg.UTXO.MinUTXOConfirmations = 1
			client.cfg.UTXO.MaxUTXOsToSpend = 1
			client.cfg.UTXO.MaxMempoolAncestors = 3
			selected, err := client.getUtxoToSpend(pubKey, 1)
			c.Assert(err, IsNil)
			c.Assert(selected, DeepEquals, f.utxos)
			c.Assert(f.mempoolEntryCalls, Equals, 1)

			client.cfg.UTXO.MaxMempoolAncestors = 2
			_, err = client.getUtxoToSpend(pubKey, 1)
			c.Assert(err, ErrorMatches, "insufficient available UTXOs:.*mempool chain limit=1.*")
			f.failMempoolEntry = true
			_, err = client.getUtxoToSpend(pubKey, 1)
			c.Assert(err, ErrorMatches, "check Dash input mempool limits:.*")
		}()
	}
}
