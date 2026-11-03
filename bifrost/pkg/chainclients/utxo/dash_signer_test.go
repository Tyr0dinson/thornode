package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	dashbtcec "github.com/dashpay/dashd-go/btcec/v2"
	dashecdsa "github.com/dashpay/dashd-go/btcec/v2/ecdsa"
	dashbtcutil "github.com/dashpay/dashd-go/btcutil"
	"github.com/dashpay/dashd-go/chaincfg/chainhash"
	dashtxscript "github.com/dashpay/dashd-go/txscript"
	dashwire "github.com/dashpay/dashd-go/wire"
	"github.com/prometheus/client_golang/prometheus/testutil"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/dashutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (s *DashSuite) TestDashSignTxCheckpointAndBroadcast(c *C) {
	ttypes.SetupConfigForTest()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kb := keyring.NewInMemory(codec.NewProtoCodec(registry))
	_, _, err := kb.NewMnemonic(bob, keyring.English, cmd.THORChainHDPath, password, hd.Secp256k1)
	c.Assert(err, IsNil)
	keys := thorclient.NewKeysWithKeybase(kb, bob, password)
	m := GetMetricForTest(c, common.DASHChain)

	var utxos []btcjson.ListUnspentResult
	var broadcastBytes []byte
	var broadcastMaxFee float64
	var customerScript string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, thorclient.MimirEndpoint) {
			c.Assert(json.NewEncoder(w).Encode(-1), IsNil)
			return
		}
		if r.URL.Path == thorclient.ChainVersionEndpoint {
			c.Assert(json.NewEncoder(w).Encode(map[string]string{"current": ttypes.GetCurrentVersion().String()}), IsNil)
			return
		}
		if r.URL.Path == thorclient.PubKeysEndpoint {
			c.Assert(json.NewEncoder(w).Encode(map[string]any{"asgard": []any{}}), IsNil)
			return
		}
		var req rpcRequest
		c.Assert(json.NewDecoder(r.Body).Decode(&req), IsNil)
		var result any
		switch req.Method {
		case "createwallet":
			result = map[string]string{"name": ""}
		case "importaddress":
		case "getnetworkinfo":
			result = map[string]any{"relayfee": 0.00001}
		case "getblockcount":
			result = 123
		case "listunspent":
			result = utxos
		case "getrawtransaction":
			var id string
			c.Assert(json.Unmarshal(req.Params[0], &id), IsNil)
			if id == strings.Repeat("2", 64) {
				result = map[string]any{
					"txid": id, "instantlock": true,
					"vin": []any{map[string]any{"txid": strings.Repeat("3", 64), "vout": 0}},
				}
			} else {
				c.Assert(id, Equals, strings.Repeat("3", 64))
				result = map[string]any{"txid": id, "vout": []any{map[string]any{
					"n": 0, "value": 0.6, "scriptPubKey": map[string]any{"hex": customerScript, "type": "pubkeyhash"},
				}}}
			}
		case "sendrawtransaction":
			c.Assert(req.Params, HasLen, 2)
			var payload string
			c.Assert(json.Unmarshal(req.Params[0], &payload), IsNil)
			c.Assert(json.Unmarshal(req.Params[1], &broadcastMaxFee), IsNil)
			var decodeErr error
			broadcastBytes, decodeErr = hex.DecodeString(payload)
			c.Assert(decodeErr, IsNil)
			var tx dashwire.MsgTx
			c.Assert(tx.Deserialize(bytes.NewReader(broadcastBytes)), IsNil)
			result = tx.TxHash().String()
		default:
			c.Fatalf("unexpected RPC %s", req.Method)
		}
		c.Assert(json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}), IsNil)
	}))
	defer server.Close()
	bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID: "thorchain", ChainHost: server.Listener.Addr().String(),
		SignerName: bob, SignerPasswd: password, ChainHomeFolder: c.MkDir(),
	}, m, keys)
	c.Assert(err, IsNil)
	cfg := config.BifrostChainConfiguration{
		ChainID: common.DASHChain, RPCHost: server.URL, UserName: bob, Password: password,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID: common.DASHChain, StartBlockHeight: 1, HTTPRequestTimeout: time.Second,
		},
	}
	cfg.UTXO.MaxSatsPerVByte = 9765
	cfg.UTXO.MaxUTXOsToSpend = 2
	cfg.UTXO.MinUTXOConfirmations = 1
	client, err := NewClient(keys, cfg, nil, bridge, m)
	c.Assert(err, IsNil)
	defer client.Stop()
	to, err := ttypes.GetRandomPubKey().GetAddress(common.DASHChain)
	c.Assert(err, IsNil)
	memo := "OUT:" + strings.Repeat("a", 64) + "|" + strings.Repeat("b", 40)
	txOut := stypes.TxOutItem{
		Chain: common.DASHChain, VaultPubKey: client.nodePubKey, ToAddress: to,
		Coins:   common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One))),
		MaxGas:  common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(10000))},
		GasRate: 10, Memo: memo,
	}
	sourceScript, err := client.getSourceScript(txOut)
	c.Assert(err, IsNil)
	utxos = []btcjson.ListUnspentResult{
		{TxID: strings.Repeat("1", 64), Amount: 0.7, ScriptPubKey: hex.EncodeToString(sourceScript), Confirmations: 10},
		{TxID: strings.Repeat("2", 64), Amount: 0.6, ScriptPubKey: hex.EncodeToString(sourceScript), Confirmations: 9},
	}
	previous := []*dashwire.TxOut{dashwire.NewTxOut(70_000_000, sourceScript), dashwire.NewTxOut(60_000_000, sourceScript)}
	customerAddress, err := dashutil.DecodeAddress(to.String(), client.getChainCfgDASH())
	c.Assert(err, IsNil)
	pkScript, err := dashtxscript.PayToAddrScript(customerAddress)
	c.Assert(err, IsNil)
	customerScript = hex.EncodeToString(pkScript)
	// An InstantSend customer input cannot fund a signature until it meets the
	// confirmation requirement. Failure must happen before creating a checkpoint.
	utxos[1].Confirmations = 0
	signed, checkpoint, observation, err := client.SignTx(txOut, 1)
	c.Assert(err, ErrorMatches, ".*insufficient available UTXOs:.*")
	c.Assert(signed, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(observation, IsNil)
	c.Assert(broadcastBytes, IsNil)
	utxos[1].Confirmations = 9

	// Exercise construction, per-input signing, verification and serialization.
	signed, checkpoint, observation, err = client.SignTx(txOut, 1)
	c.Assert(err, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(observation, NotNil)
	var decoded dashwire.MsgTx
	c.Assert(decoded.Deserialize(bytes.NewReader(signed)), IsNil)
	c.Assert(decoded.TxIn, HasLen, 2)
	c.Assert(dashutil.VerifyTransaction(&decoded, previous), IsNil)
	c.Assert(observation.Tx, Equals, decoded.TxHash().String())
	c.Assert(observation.To, Equals, to.String())
	c.Assert(observation.Memo, Equals, memo)
	c.Assert(observation.Coins[0].Amount.Uint64(), Equals, uint64(common.One))
	// Destination, change, OP_RETURN, and two P2PKH continuation outputs.
	c.Assert(decoded.TxOut, HasLen, 5)
	c.Assert(dashtxscript.GetScriptClass(decoded.TxOut[2].PkScript), Equals, dashtxscript.NullDataTy)
	firstChunk, err := dashtxscript.PushedData(decoded.TxOut[2].PkScript)
	c.Assert(err, IsNil)
	c.Assert(firstChunk, HasLen, 1)
	c.Assert(string(firstChunk[0]), Equals, memo[:79]+"^")
	recoveredMemo := append([]byte(nil), firstChunk[0][:79]...)
	for _, output := range decoded.TxOut[3:] {
		c.Assert(dashtxscript.GetScriptClass(output.PkScript), Equals, dashtxscript.PubKeyHashTy)
		c.Assert(output.Value, Equals, common.DASHChain.P2WPKHOutputValue())
		var chunks [][]byte
		chunks, err = dashtxscript.PushedData(output.PkScript)
		c.Assert(err, IsNil)
		c.Assert(chunks, HasLen, 1)
		recoveredMemo = append(recoveredMemo, chunks[0]...)
	}
	c.Assert(string(bytes.TrimRight(recoveredMemo, "\x00")), Equals, memo)

	// A wrong local key must fail verification, retain the unsigned checkpoint,
	// and never classify this as a failure to form the TSS party.
	partyFailures := m.GetCounterVec(metrics.UTXOKeysignPartyNotFormed).WithLabelValues(common.DASHChain.String())
	before := testutil.ToFloat64(partyFailures)
	correctKey := client.nodePrivKey
	client.nodePrivKey, err = btcec.NewPrivateKey(btcec.S256())
	c.Assert(err, IsNil)
	failed, checkpoint, observation, err := client.SignTx(txOut, 1)
	c.Assert(err, ErrorMatches, "fail to verify Dash input .*")
	c.Assert(failed, IsNil)
	c.Assert(checkpoint, NotNil)
	c.Assert(observation, IsNil)
	c.Assert(testutil.ToFloat64(partyFailures), Equals, before)
	c.Assert(broadcastBytes, IsNil)

	// Resume the committed transaction even if the newly estimated fee changes.
	client.nodePrivKey = correctKey
	txOut.Checkpoint = checkpoint
	txOut.GasRate = 20
	resumed, checkpoint, observation, err := client.SignTx(txOut, 2)
	c.Assert(err, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(observation, NotNil)
	c.Assert(resumed, DeepEquals, signed)
	c.Assert(decoded.Deserialize(bytes.NewReader(resumed)), IsNil)
	c.Assert(dashutil.VerifyTransaction(&decoded, previous), IsNil)
	hash, err := client.BroadcastTx(txOut, resumed)
	c.Assert(err, IsNil)
	c.Assert(hash, Equals, decoded.TxHash().String())
	c.Assert(broadcastBytes, DeepEquals, resumed)
	c.Assert(broadcastMaxFee, Equals, float64(0.1))
}

func (s *DashSuite) TestDashInputSelectionIgnoresLocalLocks(c *C) {
	for _, tc := range []struct {
		name          string
		confirmations int64
		minimum       int64
		amount        float64
		self, vault   bool
		eligible      bool
	}{
		{name: "customer without confirmations", minimum: 1, amount: 0.1},
		{name: "confirmed customer", confirmations: 1, minimum: 1, amount: 0.1, eligible: true},
		{name: "higher confirmation minimum", confirmations: 1, minimum: 2, amount: 0.1},
		{name: "explicit zero minimum", amount: 0.1, eligible: true},
		{name: "confirmed dust", confirmations: 10, minimum: 1, amount: 0.00000001},
		{name: "self change", minimum: 1, amount: 0.1, self: true, eligible: true},
		{name: "vault transfer", minimum: 1, amount: 0.1, vault: true, eligible: true},
		{name: "self dust", minimum: 1, amount: 0.00000001, self: true, eligible: true},
		{name: "vault dust", minimum: 1, amount: 0.00000001, vault: true, eligible: true},
	} {
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		pubKey := ttypes.GetRandomPubKey()
		id := f.inbound
		if tc.vault {
			id = f.lockedOutbound
		}
		client.cfg.UTXO.MinUTXOConfirmations = tc.minimum
		client.cfg.UTXO.MaxUTXOsToSpend = 2
		client.cfg.UTXO.MaxMempoolAncestors = 3
		confirmedID := strings.Repeat("9", 64)
		script := newDashP2PKHScriptHex(c, f.asgard)
		// Deliberately list the newer output first. Selection must retain its
		// confirmation ordering regardless of local lock visibility.
		f.utxos = []btcjson.ListUnspentResult{
			{TxID: id, ScriptPubKey: script, Amount: tc.amount, Confirmations: tc.confirmations},
			{TxID: confirmedID, ScriptPubKey: script, Amount: 0.1, Confirmations: 20},
		}
		if tc.self {
			c.Assert(client.temporalStorage.SaveBlockMeta(1, &sharedutxo.BlockMeta{SelfTransactions: []string{id}}), IsNil)
		}
		for _, flag := range []string{"unlocked", "instantlock", "instantlock_internal", "chainlock"} {
			for _, key := range []string{"instantlock", "instantlock_internal", "chainlock"} {
				f.txs[id][key] = key == flag
			}
			outputs, err := client.getUtxoToSpend(pubKey, common.One/10, 0)
			c.Assert(err, IsNil, Commentf("%s, %s", tc.name, flag))
			want := 1
			if tc.eligible {
				want = 2
			}
			c.Assert(outputs, HasLen, want)
			c.Assert(outputs[0].TxID, Equals, confirmedID)
			if tc.eligible {
				c.Assert(outputs[1].TxID, Equals, id)
			}
			// If the extra output is needed to pay, it must pass the same policy.
			amount, err := btcutil.NewAmount(tc.amount)
			c.Assert(err, IsNil)
			_, err = client.getUtxoToSpend(pubKey, common.One/10+amount, 0)
			if tc.eligible {
				c.Assert(err, IsNil)
			} else {
				c.Assert(err, ErrorMatches, "insufficient available UTXOs:.*")
			}
		}
	}
}

func (s *DashSuite) TestDashInputSelectionKeepsMempoolLimits(c *C) {
	for _, source := range []string{"self", "vault", "zero minimum"} {
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		pubKey := ttypes.GetRandomPubKey()
		id := f.inbound
		client.cfg.UTXO.MinUTXOConfirmations = 1
		switch source {
		case "self":
			c.Assert(client.temporalStorage.SaveBlockMeta(1, &sharedutxo.BlockMeta{SelfTransactions: []string{id}}), IsNil)
		case "vault":
			id = f.lockedOutbound
		case "zero minimum":
			client.cfg.UTXO.MinUTXOConfirmations = 0
		}
		f.utxos = []btcjson.ListUnspentResult{{TxID: id, ScriptPubKey: newDashP2PKHScriptHex(c, f.asgard), Amount: 0.1}}
		client.cfg.UTXO.MaxMempoolAncestors = 2
		_, err := client.getUtxoToSpend(pubKey, 1, 0)
		c.Assert(err, ErrorMatches, "insufficient available UTXOs:.*mempool chain limit=1.*")
		client.cfg.UTXO.MaxMempoolAncestors = 3
		outputs, err := client.getUtxoToSpend(pubKey, 1, 0)
		c.Assert(err, IsNil)
		c.Assert(outputs, HasLen, 1)
		f.failMempoolEntry = true
		_, err = client.getUtxoToSpend(pubKey, 1, 0)
		c.Assert(err, ErrorMatches, "check Dash input mempool limits:.*")
		client.cfg.UTXO.MaxMempoolAncestors = 0
		outputs, err = client.getUtxoToSpend(pubKey, 1, 0)
		c.Assert(err, IsNil)
		c.Assert(outputs, HasLen, 1)
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
	tx := dashwire.NewMsgTx(2)
	previous := make([]*dashwire.TxOut, 24)
	for i := range previous {
		tx.AddTxIn(dashwire.NewTxIn(dashwire.NewOutPoint(&chainhash.Hash{byte(i + 1)}, 0), nil, nil))
		previous[i] = dashwire.NewTxOut(100000, source)
	}
	tx.AddTxOut(dashwire.NewTxOut(2300000, source))
	hashes, err := dashutil.SignatureHashes(tx, previous)
	c.Assert(err, IsNil)
	scripts := make([][]byte, len(hashes))
	failures := make([]error, len(hashes))
	var wg sync.WaitGroup
	for i := range hashes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scripts[i], failures[i] = client.signUTXODASH(hashes[i], stypes.TxOutItem{VaultPubKey: client.nodePubKey}, 1)
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
		script, err := client.signUTXODASH(digest, stypes.TxOutItem{VaultPubKey: vault}, 123)
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
