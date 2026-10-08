package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	dashbtcec "github.com/dashpay/dashd-go/btcec/v2"
	dashecdsa "github.com/dashpay/dashd-go/btcec/v2/ecdsa"
	dashbtcutil "github.com/dashpay/dashd-go/btcutil"
	"github.com/dashpay/dashd-go/chaincfg/chainhash"
	dashtxscript "github.com/dashpay/dashd-go/txscript"
	"github.com/dashpay/dashd-go/wire"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/dashutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type dashSelectionBridge struct{ thorclient.ThorchainBridge }

func (*dashSelectionBridge) GetMimir(string) (int64, error) { return 1, nil }

func (s *DashSuite) TestDashBlockRequiresMatchingChainLock(c *C) {
	hash := strings.Repeat("a", 64)
	locked := false
	returnedHash := hash
	returnedHeight := int64(123)
	includeLock := true
	server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
		switch method {
		case "getblockhash":
			return hash
		case "getblock":
			block := map[string]any{"hash": returnedHash, "height": returnedHeight}
			if includeLock {
				block["chainlock"] = locked
			}
			return block
		}
		c.Fatalf("unexpected RPC %s", method)
		return nil
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	_, err := client.getBlock(123)
	c.Assert(err, NotNil)
	locked = true
	block, err := client.getBlock(123)
	c.Assert(err, IsNil)
	c.Assert(block.Hash, Equals, hash)
	returnedHash = strings.Repeat("b", 64)
	_, err = client.getBlock(123)
	c.Assert(err, NotNil)
	returnedHash = hash
	returnedHeight = 124
	_, err = client.getBlock(123)
	c.Assert(err, NotNil)
	returnedHeight = 123
	includeLock = false
	_, err = client.getBlock(123)
	c.Assert(err, NotNil)
}

func (s *DashSuite) TestDashLockedBalanceAndInputSelection(c *C) {
	pubKey, err := common.NewPubKeyFromCrypto(secp256k1.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	id := strings.Repeat("a", 64)
	script := newDashP2PKHScriptHex(c, newDashTestAddress(c, 0x11))
	amount := 0.1
	txID := id
	locked := true
	rawCalls := 0
	server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
		switch method {
		case "listunspent":
			return []btcjson.ListUnspentResult{
				{TxID: id, Vout: 0, ScriptPubKey: script, Amount: amount},
				{TxID: id, Vout: 1, ScriptPubKey: script, Amount: amount},
			}
		case "getrawtransaction":
			rawCalls++
			return map[string]any{"txid": txID, "instantlock": locked, "instantlock_internal": locked}
		case "getmempoolentry":
			return map[string]any{"ancestorcount": 1, "descendantcount": 1}
		}
		c.Fatalf("unexpected RPC %s", method)
		return nil
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	client.bridge = &dashSelectionBridge{}
	client.instantSendCfg.Enabled = false
	client.poolMgr = nil
	account, err := client.GetAccount(pubKey, nil)
	c.Assert(err, IsNil)
	c.Assert(account.Coins.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(20000000))
	c.Assert(rawCalls, Equals, 1)
	client.cfg.UTXO.MinUTXOConfirmations = 1
	rawCalls = 0
	outputs, err := client.getUtxoToSpend(pubKey, btcutil.Amount(20000000))
	c.Assert(err, IsNil)
	c.Assert(outputs, HasLen, 2)
	c.Assert(rawCalls, Equals, 1)
	client.cfg.UTXO.MaxMempoolAncestors = 2
	_, err = client.getUtxoToSpend(pubKey, 1)
	c.Assert(err, NotNil)
	client.cfg.UTXO.MaxMempoolAncestors = 0
	amount = 0.00000001
	_, err = client.getUtxoToSpend(pubKey, 1)
	c.Assert(err, NotNil) // locks do not bypass dust
	amount = 0.1
	locked = false
	_, err = client.getUtxoToSpend(pubKey, 1)
	c.Assert(err, NotNil)
	txID = "wrong response"
	_, err = client.GetAccount(pubKey, nil)
	c.Assert(err, NotNil) // never report an incomplete balance
}

func (s *DashSuite) TestDashMemoRoundTrip(c *C) {
	client := newDashClientForUnitTest()
	for _, length := range []int{4, 79, 80, 81, 99, 100, constants.MaxMemoSize} {
		memo := strings.Repeat("a", length)
		scripts, err := MemoToScripts(memo, dashtxscript.MaxDataCarrierSize, dashtxscript.NullDataScript, payToPubKeyHashMemoScript)
		c.Assert(err, IsNil)
		tx := &btcjson.TxRawResult{}
		for i, script := range scripts {
			kind := "pubkeyhash"
			if i == 0 {
				kind = "nulldata"
			}
			tx.Vout = append(tx.Vout, btcjson.Vout{ScriptPubKey: btcjson.ScriptPubKeyResult{Type: kind, Hex: hex.EncodeToString(script), Asm: "untrusted RPC display"}})
		}
		decoded, err := client.getMemo(tx)
		c.Assert(err, IsNil)
		c.Assert(decoded, Equals, memo)
	}
}

func (s *DashSuite) TestDashObservationGroupsClaimOnlyDeliveredTransactions(c *C) {
	client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	groups := instantSendObservationGroups(client.GetChain(), map[int64][]*types.TxInItem{
		200: {{Tx: second, BlockHeight: 200}},
		100: {{Tx: strings.ToUpper(first), BlockHeight: 100}},
	})
	client.instantSendPendingObservations = groups
	batch, err := client.getInstantSendLocks(300)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].BlockHeight, Equals, int64(100))
	observed, err := client.temporalStorage.HasObservedTx(first)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, true)
	observed, err = client.temporalStorage.HasObservedTx(second)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)

	// The block scanner can claim a queued group before its mempool delivery.
	accepted, err := client.trackObservedTxs([]*types.TxInItem{{Tx: second}})
	c.Assert(err, IsNil)
	c.Assert(accepted, HasLen, 1)
	batch, err = client.getInstantSendLocks(300)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0)
	// Changing case cannot bypass the persistent marker.
	accepted, err = client.trackObservedTxs([]*types.TxInItem{{Tx: first}})
	c.Assert(err, IsNil)
	c.Assert(accepted, HasLen, 0)
}

func (s *DashSuite) TestDashObservationBatchPreservesOrderAndFirstItem(c *C) {
	client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	firstID, secondID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	first := &types.TxInItem{Tx: strings.ToUpper(firstID), BlockHeight: 100}
	duplicate := &types.TxInItem{Tx: firstID, BlockHeight: 200}
	second := &types.TxInItem{Tx: secondID, BlockHeight: 100}
	accepted, err := client.trackObservedTxs([]*types.TxInItem{first, duplicate, second})
	c.Assert(err, IsNil)
	c.Assert(accepted, DeepEquals, []*types.TxInItem{first, second})
	accepted, err = client.trackObservedTxs([]*types.TxInItem{duplicate, second})
	c.Assert(err, IsNil)
	c.Assert(accepted, HasLen, 0)
}

func (s *DashSuite) TestDashObservationStorageFailureRetainsPendingGroup(c *C) {
	client, closeStorage := newDashClientForRPCServer(c, "http://localhost:1")
	item := &types.TxInItem{Tx: strings.Repeat("a", 64), BlockHeight: 100}
	client.instantSendPendingObservations = instantSendObservationGroups(client.GetChain(), map[int64][]*types.TxInItem{100: {item}})
	closeStorage()
	batch, err := client.getInstantSendLocks(200)
	c.Assert(err, NotNil)
	c.Assert(batch.TxArray, HasLen, 0)
	c.Assert(client.instantSendPendingObservations, HasLen, 1)
	c.Assert(client.instantSendPendingObservations[0].TxArray, DeepEquals, []*types.TxInItem{item})

	// Once storage is available, retry the queued group without another RPC scan.
	healthy, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	client.temporalStorage = healthy.temporalStorage
	batch, err = client.getInstantSendLocks(200)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, DeepEquals, []*types.TxInItem{item})
	c.Assert(client.instantSendPendingObservations, HasLen, 0)
}

func (s *DashSuite) TestDashDifferentCyclesDeliveredSeparately(c *C) {
	ids := []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	cycles := []string{strings.Repeat("1", 64), strings.Repeat("2", 64)}
	previous := strings.Repeat("c", 64)
	vault := newDashTestAddress(c, 0x11)
	sender := newDashTestAddress(c, 0x22)
	vaultScript := newDashP2PKHScriptHex(c, vault)
	senderScript := newDashP2PKHScriptHex(c, sender)
	memo := newDashMemoScriptHex(c, "NOOP")
	secondFetched := make(chan struct{})
	var once sync.Once
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{ids[0]: dashTestMempoolEntry(), ids[1]: dashTestMempoolEntry()}
		case "getrawtransaction":
			var id string
			c.Assert(json.Unmarshal(params[0], &id), IsNil)
			if id == previous {
				return btcjson.TxRawResult{Txid: id, Vout: []btcjson.Vout{{Value: 0.10002, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: senderScript, Type: "pubkeyhash", Addresses: []string{sender}}}}}
			}
			if id == ids[0] {
				<-secondFetched
			} else {
				once.Do(func() { close(secondFetched) })
			}
			return rpc.TxResult{InstantLock: true, InstantLockInternal: true, TxRawResult: btcjson.TxRawResult{
				Txid: id, LockTime: 201, Vin: []btcjson.Vin{{Txid: previous, Sequence: 0xfffffffd}},
				Vout: []btcjson.Vout{
					{Value: 0.1, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: vaultScript, Type: "pubkeyhash", Addresses: []string{vault}}},
					{N: 1, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: memo, Type: "nulldata"}},
				},
			}}
		case "getislocks":
			var requested []string
			c.Assert(json.Unmarshal(params[0], &requested), IsNil)
			locks := make([]rpc.ISLockResult, len(requested))
			for i, id := range requested {
				index := 0
				if id == ids[1] {
					index = 1
				}
				locks[i] = rpc.ISLockResult{TxID: id, CycleHash: cycles[index]}
			}
			return locks
		case "getblock":
			var hash string
			c.Assert(json.Unmarshal(params[0], &hash), IsNil)
			height := int64(100)
			if hash == cycles[1] {
				height = 200
			}
			return btcjson.GetBlockVerboseResult{Hash: hash, Height: height, Confirmations: 1}
		}
		c.Fatalf("unexpected RPC %s", method)
		return nil
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	client.cfg.ParallelMempoolScan = 2
	client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{common.Address(vault)}, FetchedAt: time.Now()})
	for i, height := range []int64{100, 200} {
		batch, err := client.getInstantSendLocks(201)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 1)
		c.Assert(batch.TxArray[0].Tx, Equals, ids[i])
		c.Assert(batch.TxArray[0].BlockHeight, Equals, height)
	}
}

func (s *DashSuite) TestDashSignsPreparedInputs(c *C) {
	client := newDashClientForUnitTest()
	key, pubKey := btcec.PrivKeyFromBytes(btcec.S256(), []byte{1})
	client.nodePrivKey = key
	var err error
	client.nodePubKey, err = common.NewPubKeyFromCrypto(secp256k1.PubKey(pubKey.SerializeCompressed()))
	c.Assert(err, IsNil)
	address, err := dashbtcutil.NewAddressPubKeyHash(dashbtcutil.Hash160(pubKey.SerializeCompressed()), client.getChainCfgDASH())
	c.Assert(err, IsNil)
	source, err := dashtxscript.PayToAddrScript(address)
	c.Assert(err, IsNil)
	tx := wire.NewMsgTx(2)
	previous := make([]*wire.TxOut, 24)
	for i := range previous {
		tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&chainhash.Hash{byte(i + 1)}, 0), nil, nil))
		previous[i] = wire.NewTxOut(100000, source)
	}
	tx.AddTxOut(wire.NewTxOut(2300000, source))
	hashes, err := dashutil.SignatureHashes(tx, previous)
	c.Assert(err, IsNil)
	scripts := make([][]byte, len(hashes))
	failures := make([]error, len(hashes))
	var wg sync.WaitGroup
	for i := range hashes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scripts[i], failures[i] = client.signUTXODASH(hashes[i], types.TxOutItem{VaultPubKey: client.nodePubKey}, 1)
		}()
	}
	wg.Wait()
	for i, script := range scripts {
		c.Assert(failures[i], IsNil)
		tx.TxIn[i].SignatureScript = script
	}
	c.Assert(dashutil.VerifyTransaction(tx, previous), IsNil)
	tx.TxOut[0].Value++
	c.Assert(dashutil.VerifyTransaction(tx, previous), NotNil)
}

type dashTestKeyManager struct {
	tss.ThorchainKeyManager
	sign func([]byte, common.SigningAlgo, string, int64) ([]byte, []byte, error)
}

func (k *dashTestKeyManager) RemoteSign(digest []byte, algo common.SigningAlgo, pubKey string, height int64) ([]byte, []byte, error) {
	return k.sign(digest, algo, pubKey, height)
}

func (s *DashSuite) TestDashTSSSigning(c *C) {
	client := newDashClientForUnitTest()
	key, pubKey := dashbtcec.PrivKeyFromBytes([]byte{1})
	vault, err := common.NewPubKeyFromCrypto(secp256k1.PubKey(pubKey.SerializeCompressed()))
	c.Assert(err, IsNil)
	digest := bytes.Repeat([]byte{2}, 32)
	compact, err := dashecdsa.SignCompact(key, digest, true)
	c.Assert(err, IsNil)
	remoteErr := errors.New("TSS failed")
	for _, tc := range []struct {
		raw                []byte
		remoteErr, wantErr error
		valid              bool
	}{
		{raw: compact[1:], valid: true},
		{wantErr: tss.ErrNotSelectedForKeysign},
		{raw: []byte{1}},
		{raw: bytes.Repeat([]byte{1}, 64)}, // Correct shape, wrong signature.
		{remoteErr: remoteErr, wantErr: remoteErr},
	} {
		client.tssKeySigner = &dashTestKeyManager{sign: func(got []byte, algo common.SigningAlgo, pubKey string, height int64) ([]byte, []byte, error) {
			c.Assert(got, DeepEquals, digest)
			c.Assert(algo, Equals, common.SigningAlgoSecp256k1)
			c.Assert(pubKey, Equals, vault.String())
			c.Assert(height, Equals, int64(123))
			return tc.raw, nil, tc.remoteErr
		}}
		script, err := client.signUTXODASH(digest, types.TxOutItem{VaultPubKey: vault}, 123)
		if tc.valid {
			c.Assert(err, IsNil)
			want, scriptErr := dashutil.SignatureScript(digest, dashecdsa.Sign(key, digest), pubKey)
			c.Assert(scriptErr, IsNil)
			c.Assert(script, DeepEquals, want)
		} else {
			c.Assert(err, NotNil)
			c.Assert(script, IsNil)
			if tc.wantErr != nil {
				c.Assert(errors.Is(err, tc.wantErr), Equals, true)
			}
		}
	}
}
