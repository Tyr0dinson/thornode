package evm

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	ecrypto "github.com/ethereum/go-ethereum/crypto"
	. "gopkg.in/check.v1"

	shared "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type nonselectedKeyManager struct{ tss.ThorchainKeyManager }

func (nonselectedKeyManager) RemoteSign([]byte, common.SigningAlgo, string, int64) ([]byte, []byte, error) {
	return nil, nil, tss.ErrNotSelectedForKeysign
}

func (s *EVMSuite) newRetryClient(c *C) (*EVMClient, stypes.TxOutItem, func()) {
	manager, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	c.Assert(manager.Start(), IsNil)
	cfg := config.BifrostChainConfiguration{
		ChainID: common.AVAXChain, RPCHost: s.server.URL,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID: common.AVAXChain, StartBlockHeight: 1,
			HTTPRequestTimeout: time.Second, MaxGasLimit: 80000,
		},
	}
	cfg.EVM.TokenMaxGasMultiplier = 3
	client, err := NewEVMClient(s.thorKeys, cfg, nil, s.bridge, s.m, manager, thorclient.NewPoolMgr(s.bridge))
	c.Assert(err, IsNil)
	client.signingReady.Store(true)
	address, err := client.localPubKey.GetAddress(common.AVAXChain)
	c.Assert(err, IsNil)
	return client, stypes.TxOutItem{
		Chain: common.AVAXChain, VaultPubKey: client.localPubKey,
		ToAddress: address,
		Coins:     common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(common.One))),
		MaxGas:    common.Gas(common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(320000)))),
		GasRate:   1, Memo: "MIGRATE:100",
	}, func() { c.Assert(manager.Stop(), IsNil) }
}

func (s *EVMSuite) TestNonselectedEVMOutboundRetainsNonce(c *C) {
	client, tx, cleanup := s.newRetryClient(c)
	defer cleanup()
	var err error
	client.kw, err = shared.NewKeySignWrapper(client.kw.GetPrivKey(), common.EmptyPubKey, nonselectedKeyManager{}, big.NewInt(15), "AVAX")
	c.Assert(err, IsNil)
	raw, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, tss.ErrNotSelectedForKeysign), Equals, true)
	c.Assert(raw, IsNil)
	var saved signingCheckpoint
	c.Assert(json.Unmarshal(checkpoint, &saved), IsNil)
	c.Assert(saved.UnsignedTransferNonce, NotNil)
	c.Assert(*saved.UnsignedTransferNonce, Equals, uint64(0))
}

// approvalRPC models independent observations of submission, mining, and nonce
// advancement. All other requests use the normal suite's chain RPC fixtures.
type approvalRPC struct {
	sync.Mutex
	fallback         http.Handler
	mined            *etypes.Transaction
	logs             []etypes.Log
	receiptMissing   bool
	receiptError     bool
	receiptWrongHash bool
	receiptNoBlock   bool
	receiptStatus    uint64
	broadcastError   string
	lookupError      bool
	pendingNonce     uint64
	confirmedNonce   uint64
	known            *etypes.Transaction
	nonceError       bool
	hashError        bool
	heightError      bool
	approvalReads    []string
	submitted        []*etypes.Transaction
}

func (r *approvalRPC) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	var call struct {
		ID     json.RawMessage
		Method string
		Params []json.RawMessage
	}
	_ = json.Unmarshal(body, &call)
	r.Lock()
	defer r.Unlock()
	var result any
	switch call.Method {
	case "eth_blockNumber":
		r.approvalReads = append(r.approvalReads, "height")
		if r.heightError {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "error": map[string]any{"code": -32000, "message": "height unavailable"}})
			return
		}
		result = "0x65"
	case "eth_call":
		var args struct{ Data, Input string }
		if len(call.Params) > 0 {
			_ = json.Unmarshal(call.Params[0], &args)
		}
		if strings.HasPrefix(args.Data, "0xdd62ed3e") || strings.HasPrefix(args.Input, "0xdd62ed3e") {
			r.approvalReads = append(r.approvalReads, "allowance")
		}
		r.fallback.ServeHTTP(w, req)
		return
	case "eth_getTransactionCount":
		r.approvalReads = append(r.approvalReads, "nonce")
		if r.nonceError {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "error": map[string]any{"code": -32000, "message": "nonce unavailable"}})
			return
		}
		nonce := r.pendingNonce
		if len(call.Params) > 1 && string(call.Params[1]) == `"latest"` {
			nonce = r.confirmedNonce
		}
		result = fmt.Sprintf("0x%x", nonce)
	case "eth_getLogs":
		if r.lookupError {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "error": map[string]any{"code": -32000, "message": "lookup unavailable"}})
			return
		}
		result = r.logs
		if r.logs == nil {
			result = []etypes.Log{}
		}
	case "eth_getTransactionByHash":
		if r.hashError {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "error": map[string]any{"code": -32000, "message": "hash unavailable"}})
			return
		}
		var hash ecommon.Hash
		_ = json.Unmarshal(call.Params[0], &hash)
		if r.known != nil && r.known.Hash() == hash {
			result = r.known
		}
		if r.mined != nil && r.mined.Hash() == hash {
			b, _ := r.mined.MarshalJSON()
			var fields map[string]any
			_ = json.Unmarshal(b, &fields)
			fields["blockHash"] = ecommon.HexToHash("0xabc")
			fields["blockNumber"] = "0x65"
			result = fields
		}
	case "eth_getTransactionReceipt":
		var hash ecommon.Hash
		_ = json.Unmarshal(call.Params[0], &hash)
		if r.receiptError {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "error": map[string]any{"code": -32000, "message": "receipt unavailable"}})
			return
		}
		if r.mined != nil && r.mined.Hash() == hash && !r.receiptMissing {
			receipt := &etypes.Receipt{Type: r.mined.Type(), Status: r.receiptStatus, TxHash: r.mined.Hash(), BlockHash: ecommon.HexToHash("0xabc"), BlockNumber: big.NewInt(101), Logs: []*etypes.Log{}}
			if r.receiptWrongHash {
				receipt.TxHash = ecommon.HexToHash("0xbad")
			}
			if r.receiptNoBlock {
				receipt.BlockNumber = nil
			}
			result = receipt
		}
	case "eth_sendRawTransaction":
		var encoded string
		_ = json.Unmarshal(call.Params[0], &encoded)
		var tx etypes.Transaction
		_ = tx.UnmarshalBinary(ecommon.FromHex(encoded))
		r.submitted = append(r.submitted, &tx)
		r.known = &tx
		if r.broadcastError != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "error": map[string]any{"code": -32000, "message": r.broadcastError}})
			return
		}
		result = tx.Hash()
	default:
		r.fallback.ServeHTTP(w, req)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": result})
}

func (s *EVMSuite) approvalRetryFixture(c *C) (*EVMClient, stypes.TxOutItem, *approvalRPC, func()) {
	rpc := &approvalRPC{fallback: s.server.Config.Handler, receiptStatus: 1}
	server := httptest.NewServer(rpc)
	previous := s.server
	s.server = server
	s.mimirOverrides = map[string]string{"EVMAllowanceCheck-AVAX": "1"}
	client, tx, cleanup := s.newRetryClient(c)
	asset, err := common.NewAsset("AVAX.USDC-0xA0b86a33E6441e60DDB6B6B84b7efF8e6f3C5d0C")
	c.Assert(err, IsNil)
	tx.Coins = common.NewCoins(common.NewCoin(asset, cosmos.NewUint(1000000)))
	return client, tx, rpc, func() { cleanup(); server.Close(); s.server = previous }
}

func (s *EVMSuite) TestApprovalSearchHeightSkippedForNativeTransfer(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	tx.Coins = common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(common.One)))
	rpc.Lock()
	rpc.heightError, rpc.approvalReads = true, nil
	rpc.Unlock()

	raw, checkpoint, observation, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, IsNil)
	c.Assert(raw, NotNil)
	c.Assert(string(checkpoint), Equals, "0")
	c.Assert(observation, NotNil)
	rpc.Lock()
	defer rpc.Unlock()
	// Only the post-sign observation-height read remains; its existing fallback
	// tolerates the RPC error without delaying this native transfer.
	c.Assert(rpc.approvalReads, DeepEquals, []string{"nonce", "nonce", "nonce", "height"})
}

func (s *EVMSuite) TestApprovalSearchHeightSkippedWhenAllowanceDisabled(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	s.mimirOverrides = map[string]string{"EVMAllowanceCheck-AVAX": "0"}
	rpc.Lock()
	rpc.heightError, rpc.approvalReads = true, nil
	rpc.Unlock()

	raw, checkpoint, observation, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, IsNil)
	c.Assert(raw, NotNil)
	c.Assert(string(checkpoint), Equals, "0")
	c.Assert(observation, NotNil)
	rpc.Lock()
	defer rpc.Unlock()
	c.Assert(rpc.approvalReads, DeepEquals, []string{"nonce", "nonce", "nonce", "height"})
}

func (s *EVMSuite) TestApprovalSearchHeightFailureStopsApproval(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	rpc.Lock()
	rpc.heightError, rpc.approvalReads = true, nil
	rpc.Unlock()

	raw, checkpoint, observation, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, ErrorMatches, "get approval search height: height unavailable")
	c.Assert(raw, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(observation, IsNil)
	rpc.Lock()
	defer rpc.Unlock()
	c.Assert(rpc.approvalReads, DeepEquals, []string{"height"})
	c.Assert(rpc.submitted, HasLen, 0)
}

func (s *EVMSuite) TestApprovalSearchHeightPrecedesAllowanceAndNonce(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	rpc.Lock()
	rpc.approvalReads = nil
	rpc.Unlock()

	raw, checkpoint, observation, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	c.Assert(raw, IsNil)
	c.Assert(observation, IsNil)
	var saved signingCheckpoint
	c.Assert(json.Unmarshal(checkpoint, &saved), IsNil)
	c.Assert(saved.FromBlock, Equals, uint64(101))
	c.Assert(saved.Transaction, NotNil)
	c.Assert(saved.Transaction.Nonce(), Equals, uint64(0))
	rpc.Lock()
	defer rpc.Unlock()
	c.Assert(rpc.approvalReads, DeepEquals, []string{"height", "allowance", "nonce", "nonce"})
	c.Assert(rpc.submitted, HasLen, 0)
}

func mineApproval(c *C, client *EVMClient, tx stypes.TxOutItem, rpc *approvalRPC, nonce uint64) {
	var cp signingCheckpoint
	c.Assert(json.Unmarshal(tx.Checkpoint, &cp), IsNil)
	a := cp.Transaction
	unsigned := etypes.NewTransaction(nonce, *a.To(), a.Value(), a.Gas(), a.GasPrice(), a.Data())
	signed, err := etypes.SignTx(unsigned, etypes.NewLondonSigner(big.NewInt(15)), client.kw.GetPrivKey())
	c.Assert(err, IsNil)
	owner, err := tx.VaultPubKey.GetAddress(common.AVAXChain)
	c.Assert(err, IsNil)
	rpc.Lock()
	defer rpc.Unlock()
	rpc.mined = signed
	rpc.pendingNonce = nonce + 1
	rpc.confirmedNonce = nonce + 1
	rpc.logs = []etypes.Log{{Address: *signed.To(), Topics: []ecommon.Hash{client.evmScanner.erc20ABI.Events["Approval"].ID, ecommon.BytesToHash(ecommon.HexToAddress(owner.String()).Bytes())}, BlockNumber: 101, TxHash: signed.Hash(), BlockHash: ecommon.HexToHash("0xabc")}}
}

func (s *EVMSuite) TestApprovalRetryAfterPeerCompletion(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	raw, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	c.Assert(raw, IsNil)
	c.Assert(rpc.submitted, HasLen, 0) // No side effect before durable preparation.
	tx.Checkpoint = checkpoint
	// This reproduces cat's stale nonce 0 and peer approval at 1.
	// Recovery must choose exactly 2 for the transfer.
	mineApproval(c, client, tx, rpc, 1)
	_, checkpoint, _, err = client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	assertUnsignedNonce(c, checkpoint, 2)
	tx.Checkpoint = checkpoint
	raw, checkpoint, _, err = client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, IsNil)
	var transfer etypes.Transaction
	c.Assert(transfer.UnmarshalJSON(raw), IsNil)
	c.Assert(transfer.Nonce(), Equals, uint64(2))
	c.Assert(string(checkpoint), Equals, "2")
	c.Assert(rpc.submitted, HasLen, 0) // Main transfer goes through signer persistence.
}

func (s *EVMSuite) TestApprovalRetryAmbiguousBroadcast(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	_, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	tx.Checkpoint = checkpoint
	_, checkpoint, _, err = client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	tx.Checkpoint = checkpoint
	rpc.broadcastError = "connection lost after submission"
	_, retry, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, ErrorMatches, "broadcast approval:.*")
	c.Assert(retry, DeepEquals, checkpoint)
	c.Assert(rpc.submitted, HasLen, 1)
	mineApproval(c, client, tx, rpc, 0)
	_, retry, _, err = client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	assertUnsignedNonce(c, retry, 1)
	c.Assert(rpc.submitted, HasLen, 1)
}

func (s *EVMSuite) TestApprovalRetryUnprovenCompletion(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	for i := 0; i < 2; i++ {
		_, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
		c.Assert(errors.Is(err, errApprovalPending), Equals, true)
		tx.Checkpoint = checkpoint
	}
	rpc.broadcastError = "nonce too low"
	_, retry, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	c.Assert(retry, DeepEquals, tx.Checkpoint)
	mineApproval(c, client, tx, rpc, 0)
	rpc.receiptStatus = etypes.ReceiptStatusFailed
	raw, retry, observation, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, IsNil)
	c.Assert(raw, IsNil)
	c.Assert(retry, IsNil)
	c.Assert(observation, IsNil)
}

func (s *EVMSuite) TestApprovalRetryRestartAndNonselection(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	_, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	path := filepath.Join(c.MkDir(), "checkpoint")
	c.Assert(os.WriteFile(path, checkpoint, 0o600), IsNil)

	// Reconstruct the client with no in-memory approval state, using only the
	// checkpoint returned to the signer before signing or broadcasting anything.
	restarted, _, stop := s.newRetryClient(c)
	defer stop()
	tx.Checkpoint, err = os.ReadFile(path)
	c.Assert(err, IsNil)
	localSigner := restarted.kw
	restarted.kw, err = shared.NewKeySignWrapper(localSigner.GetPrivKey(), common.EmptyPubKey, nonselectedKeyManager{}, big.NewInt(15), "AVAX")
	c.Assert(err, IsNil)
	_, retry, _, err := restarted.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, tss.ErrNotSelectedForKeysign), Equals, true)
	c.Assert(retry, DeepEquals, checkpoint)
	c.Assert(rpc.submitted, HasLen, 0)
	restarted.kw = localSigner
	mineApproval(c, client, tx, rpc, 0)
	_, retry, _, err = restarted.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	assertUnsignedNonce(c, retry, 1)
}

func (s *EVMSuite) TestApprovalRetryLegacyTransferNonceIsImmutable(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	tx.Checkpoint = []byte("7")
	// Even with insufficient allowance and a different live nonce, an old
	// numeric checkpoint must not trigger another approval or advance the nonce.
	rpc.pendingNonce = 12
	raw, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, IsNil)
	var transfer etypes.Transaction
	c.Assert(transfer.UnmarshalJSON(raw), IsNil)
	c.Assert(transfer.Nonce(), Equals, uint64(7))
	c.Assert(string(checkpoint), Equals, "7")
	c.Assert(rpc.submitted, HasLen, 0)
	for _, invalid := range []string{"", " ", "null", "-1", "1.5", "18446744073709551616", `{}`, `{"version":2}`} {
		tx.Checkpoint = []byte(invalid)
		raw, _, _, err = client.SignTx([]stypes.TxOutItem{tx}, 100)
		c.Assert(err, NotNil, Commentf("checkpoint %s", invalid))
		c.Assert(raw, IsNil)
	}
}

func (s *EVMSuite) TestApprovalRetryRejectsUnrelatedEvidence(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	_, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	tx.Checkpoint = checkpoint
	mineApproval(c, client, tx, rpc, 0)
	original := rpc.mined
	// An event from another call on the token is insufficient evidence.
	wrong := etypes.NewTransaction(0, *original.To(), original.Value(), original.Gas(), original.GasPrice(), []byte("different router or call"))
	rpc.mined, err = etypes.SignTx(wrong, etypes.NewLondonSigner(big.NewInt(15)), client.kw.GetPrivKey())
	c.Assert(err, IsNil)
	rpc.logs[0].TxHash = rpc.mined.Hash()
	rpc.nonceError = true
	raw, retry, observation, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, NotNil)
	c.Assert(raw, IsNil)
	c.Assert(observation, IsNil)
	c.Assert(retry, DeepEquals, tx.Checkpoint)
	rpc.nonceError = false
	raw, retry, observation, err = client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, IsNil)
	c.Assert(raw, IsNil)
	c.Assert(retry, IsNil)
	c.Assert(observation, IsNil)
	c.Assert(rpc.submitted, HasLen, 0)
	rpc.mined = original
	rpc.logs[0].TxHash = original.Hash()
	rpc.logs[0].Removed = true
	raw, retry, observation, err = client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, IsNil)
	c.Assert(raw, IsNil)
	c.Assert(retry, IsNil)
	c.Assert(observation, IsNil)
	c.Assert(rpc.submitted, HasLen, 0)
}

func (s *EVMSuite) TestReviewSubmittedApprovalDoesNotRepeat(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	keyManager := &countingKeyManager{key: client.kw.GetPrivKey()}
	client.kw, _ = shared.NewKeySignWrapper(keyManager.key, common.EmptyPubKey, keyManager, big.NewInt(15), "AVAX")
	for i := 0; i < 6; i++ {
		_, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
		c.Assert(errors.Is(err, errApprovalPending), Equals, true)
		tx.Checkpoint = checkpoint
	}
	c.Assert(rpc.submitted, HasLen, 1)
	c.Assert(keyManager.calls, Equals, 1)
	// A restart polls the known hash without signing or broadcasting again.
	restarted, _, stop := s.newRetryClient(c)
	defer stop()
	restarted.kw = client.kw
	_, retry, _, err := restarted.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	c.Assert(retry, DeepEquals, tx.Checkpoint)
	c.Assert(keyManager.calls, Equals, 1)
	c.Assert(rpc.submitted, HasLen, 1)
	// Even if the token emits no Approval event, the signed hash and receipt
	// identify completion. Disallow eth_getLogs to prove it is not needed here.
	rpc.mined = rpc.known
	rpc.confirmedNonce = 1
	rpc.pendingNonce = 1
	rpc.lookupError = true
	_, retry, _, err = restarted.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	assertUnsignedNonce(c, retry, 1)
	c.Assert(keyManager.calls, Equals, 1)
}

type countingKeyManager struct {
	tss.ThorchainKeyManager
	key    *ecdsa.PrivateKey
	calls  int
	hashes [][]byte
	err    error
}

func (m *countingKeyManager) RemoteSign(hash []byte, _ common.SigningAlgo, _ string, _ int64) ([]byte, []byte, error) {
	m.calls++
	m.hashes = append(m.hashes, append([]byte(nil), hash...))
	if m.err != nil {
		return nil, nil, m.err
	}
	sig, err := ecrypto.Sign(hash, m.key)
	if err != nil {
		return nil, nil, err
	}
	return sig[:64], sig[64:], nil
}

func (s *EVMSuite) TestReviewMissingApprovalRebroadcastsExactBytes(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	keyManager := &countingKeyManager{key: client.kw.GetPrivKey()}
	client.kw, _ = shared.NewKeySignWrapper(keyManager.key, common.EmptyPubKey, keyManager, big.NewInt(15), "AVAX")
	for i := 0; i < 3; i++ {
		_, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
		c.Assert(errors.Is(err, errApprovalPending), Equals, true)
		tx.Checkpoint = checkpoint
	}
	first, err := rpc.submitted[0].MarshalBinary()
	c.Assert(err, IsNil)
	// Once signed bytes exist, even a changed fee and a later join failure
	// must never cause another keysign or replace those bytes.
	client.evmScanner.gasPrice.Store(big.NewInt(80_000_000_000))
	keyManager.err = tss.NewKeysignError(ttypes.Blame{FailReason: blame.TssSyncFail})
	rpc.known = nil // Dropped from the RPC mempool.
	_, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	c.Assert(checkpoint, DeepEquals, tx.Checkpoint)
	c.Assert(rpc.submitted, HasLen, 2)
	second, err := rpc.submitted[1].MarshalBinary()
	c.Assert(err, IsNil)
	c.Assert(second, DeepEquals, first)
	c.Assert(keyManager.calls, Equals, 1)
	rpc.hashError = true
	_, _, _, err = client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, ErrorMatches, "lookup signed approval:.*")
	c.Assert(rpc.submitted, HasLen, 2)
	c.Assert(keyManager.calls, Equals, 1)
}

func (s *EVMSuite) TestReviewUnsignedTransferReleasedByChainNonce(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	tx.Coins = common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(common.One)))
	keyManager := &countingKeyManager{key: client.kw.GetPrivKey(), err: tss.ErrNotSelectedForKeysign}
	client.kw, _ = shared.NewKeySignWrapper(keyManager.key, common.EmptyPubKey, keyManager, big.NewInt(15), "AVAX")
	_, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, tss.ErrNotSelectedForKeysign), Equals, true)
	assertUnsignedNonce(c, checkpoint, 0)
	tx.Checkpoint = checkpoint
	// Both unchanged and unavailable nonce evidence must retain the item.
	_, _, _, err = client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, tss.ErrNotSelectedForKeysign), Equals, true)
	rpc.nonceError = true
	_, _, _, err = client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, ErrorMatches, "check checkpoint confirmed nonce:.*")
	rpc.nonceError = false
	rpc.pendingNonce = 1
	calls := keyManager.calls
	// Reconstructing the client preserves the no-signature evidence.
	restarted, _, stop := s.newRetryClient(c)
	defer stop()
	restarted.kw = client.kw
	raw, saved, obs, err := restarted.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, IsNil)
	c.Assert(raw, IsNil)
	c.Assert(saved, IsNil)
	c.Assert(obs, IsNil)
	c.Assert(keyManager.calls, Equals, calls)
}

func (s *EVMSuite) TestReviewLegacyNonceRequiresConfirmation(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	tx.Coins = common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(common.One)))
	tx.Checkpoint = []byte("0")
	keyManager := &countingKeyManager{key: client.kw.GetPrivKey(), err: tss.ErrNotSelectedForKeysign}
	client.kw, _ = shared.NewKeySignWrapper(keyManager.key, common.EmptyPubKey, keyManager, big.NewInt(15), "AVAX")
	rpc.pendingNonce = 1
	_, saved, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, tss.ErrNotSelectedForKeysign), Equals, true)
	c.Assert(string(saved), Equals, "0") // Never relabel possible exposure as unsigned.
	rpc.confirmedNonce = 1
	raw, saved, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(err, IsNil)
	c.Assert(raw, IsNil)
	c.Assert(saved, IsNil)
	c.Assert(keyManager.calls, Equals, 1)
}

func (s *EVMSuite) TestReviewEmptyCheckpointIsRejected(c *C) {
	client, tx, _, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	for _, checkpoint := range [][]byte{[]byte(""), []byte(" "), []byte("\n\t")} {
		tx.Checkpoint = checkpoint
		raw, saved, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
		c.Assert(err, ErrorMatches, "empty transaction checkpoint")
		c.Assert(raw, IsNil)
		c.Assert(saved, IsNil)
	}
}

func assertUnsignedNonce(c *C, checkpoint []byte, nonce uint64) {
	var saved signingCheckpoint
	c.Assert(json.Unmarshal(checkpoint, &saved), IsNil)
	c.Assert(saved.UnsignedTransferNonce, NotNil)
	c.Assert(*saved.UnsignedTransferNonce, Equals, nonce)
}

// Different local fees must converge after party formation fails, without
// changing the approval's nonce or call or exposing a replacement signature.
func (s *EVMSuite) TestApprovalGasSplitConvergesAfterJoinFailure(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	checkpoints := make([][]byte, 2)
	for i, fee := range []int64{50_000_000_000, 80_000_000_000} {
		client.evmScanner.gasPrice.Store(big.NewInt(fee))
		_, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
		c.Assert(errors.Is(err, errApprovalPending), Equals, true)
		checkpoints[i] = checkpoint
	}
	client.evmScanner.gasPrice.Store(big.NewInt(80_000_000_000))
	var hashes [][]byte
	for _, checkpoint := range checkpoints {
		manager := &countingKeyManager{key: client.kw.GetPrivKey(), err: tss.NewKeysignError(ttypes.Blame{FailReason: blame.TssSyncFail})}
		var err error
		client.kw, err = shared.NewKeySignWrapper(manager.key, common.EmptyPubKey, manager, big.NewInt(15), "AVAX")
		c.Assert(err, IsNil)
		tx.Checkpoint = checkpoint
		var original signingCheckpoint
		c.Assert(json.Unmarshal(checkpoint, &original), IsNil)
		raw, next, observation, signErr := client.SignTx([]stypes.TxOutItem{tx}, 100)
		c.Assert(signErr, NotNil)
		c.Assert(raw, IsNil)
		c.Assert(observation, IsNil)
		var saved signingCheckpoint
		c.Assert(json.Unmarshal(next, &saved), IsNil)
		c.Assert(saved.Version, Equals, original.Version)
		c.Assert(saved.Outbound, Equals, original.Outbound)
		c.Assert(saved.FromBlock, Equals, original.FromBlock)
		c.Assert(saved.SignedApproval, IsNil)
		c.Assert(saved.UnsignedTransferNonce, IsNil)
		approval := original.Transaction
		expected := etypes.NewTransaction(approval.Nonce(), *approval.To(), approval.Value(), approval.Gas(), big.NewInt(80_000_000_000+client.evmScanner.ApprovalTxRateDustWei().Int64()), approval.Data())
		c.Assert(saved.Transaction.Hash(), Equals, expected.Hash())
		c.Assert(manager.calls, Equals, 1)
		// Carry the returned checkpoint into another actual SignTx attempt.
		tx.Checkpoint = next
		_, retained, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
		c.Assert(err, NotNil)
		c.Assert(retained, DeepEquals, next)
		c.Assert(manager.calls, Equals, 2)
		hashes = append(hashes, manager.hashes[1])
	}
	c.Assert(hashes[0], DeepEquals, hashes[1])
	c.Assert(rpc.submitted, HasLen, 0)
}

func (s *EVMSuite) TestApprovalTerminalFailureRetainsCheckpointWithChangedFee(c *C) {
	client, tx, rpc, cleanup := s.approvalRetryFixture(c)
	defer cleanup()
	client.evmScanner.gasPrice.Store(big.NewInt(50_000_000_000))
	_, saved, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	c.Assert(errors.Is(err, errApprovalPending), Equals, true)
	tx.Checkpoint = saved
	manager := &countingKeyManager{key: client.kw.GetPrivKey(), err: tss.NewKeysignError(ttypes.Blame{Round: "SignRound9Message"})}
	client.kw, err = shared.NewKeySignWrapper(manager.key, common.EmptyPubKey, manager, big.NewInt(15), "AVAX")
	c.Assert(err, IsNil)
	// A changed fee must not hide the terminal-round error or reprice its checkpoint.
	client.evmScanner.gasPrice.Store(big.NewInt(80_000_000_000))
	raw, retained, observation, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
	var terminal tss.KeysignError
	c.Assert(errors.As(err, &terminal), Equals, true)
	c.Assert(terminal.IsTerminalRound(), Equals, true)
	c.Assert(retained, DeepEquals, saved)
	c.Assert(raw, IsNil)
	c.Assert(observation, IsNil)
	c.Assert(manager.calls, Equals, 1)
	c.Assert(rpc.submitted, HasLen, 0)
}

func (s *EVMSuite) TestApprovalRepricingRecoversEarlierExposedVariant(c *C) {
	for _, reverted := range []bool{false, true} {
		func() {
			client, tx, rpc, cleanup := s.approvalRetryFixture(c)
			defer cleanup()
			client.evmScanner.gasPrice.Store(big.NewInt(50_000_000_000))
			_, original, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
			c.Assert(errors.Is(err, errApprovalPending), Equals, true)
			tx.Checkpoint = original
			localSigner := client.kw
			manager := &countingKeyManager{key: localSigner.GetPrivKey(), err: tss.NewKeysignError(ttypes.Blame{Round: "SignRound9Message"})}
			client.kw, err = shared.NewKeySignWrapper(manager.key, common.EmptyPubKey, manager, big.NewInt(15), "AVAX")
			c.Assert(err, IsNil)
			_, retained, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
			var terminal tss.KeysignError
			c.Assert(errors.As(err, &terminal), Equals, true)
			c.Assert(terminal.IsTerminalRound(), Equals, true)
			c.Assert(retained, DeepEquals, original)
			// A later join failure does not erase earlier exposure. Change only
			// the fee at the same nonce and retain the original search boundary.
			client.evmScanner.gasPrice.Store(big.NewInt(80_000_000_000))
			manager.err = tss.NewKeysignError(ttypes.Blame{FailReason: blame.TssSyncFail})
			_, repriced, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
			c.Assert(errors.Is(err, errApprovalPending), Equals, true)
			c.Assert(repriced, Not(DeepEquals), original)
			mineApproval(c, client, tx, rpc, 0) // The peer still has the old fee variant.
			if reverted {
				rpc.Lock()
				rpc.receiptStatus, rpc.logs = etypes.ReceiptStatusFailed, nil
				rpc.Unlock()
			}
			restarted, _, stop := s.newRetryClient(c)
			defer stop()
			restarted.kw = client.kw
			for _, savedSigned := range []bool{false, true} {
				if savedSigned && !reverted {
					break
				}
				checkpoint := repriced
				if savedSigned {
					var saved signingCheckpoint
					c.Assert(json.Unmarshal(checkpoint, &saved), IsNil)
					saved.SignedApproval, err = etypes.SignTx(saved.Transaction, etypes.NewLondonSigner(big.NewInt(15)), manager.key)
					c.Assert(err, IsNil)
					checkpoint, err = json.Marshal(saved)
					c.Assert(err, IsNil)
				}
				path := filepath.Join(c.MkDir(), "repriced-checkpoint")
				c.Assert(os.WriteFile(path, checkpoint, 0o600), IsNil)
				tx.Checkpoint, err = os.ReadFile(path)
				c.Assert(err, IsNil)
				if reverted {
					rpc.nonceError = true
					_, retained, _, lookupErr := restarted.SignTx([]stypes.TxOutItem{tx}, 100)
					c.Assert(lookupErr, NotNil)
					c.Assert(retained, DeepEquals, checkpoint)
					rpc.nonceError = false
				}
				raw, next, observation, signErr := restarted.SignTx([]stypes.TxOutItem{tx}, 100)
				c.Assert(raw, IsNil)
				c.Assert(observation, IsNil)
				if reverted {
					c.Assert(signErr, IsNil)
					c.Assert(next, IsNil)
				} else {
					c.Assert(errors.Is(signErr, errApprovalPending), Equals, true)
					assertUnsignedNonce(c, next, 1)
					restarted.kw = localSigner
					tx.Checkpoint = next
					raw, _, _, err = restarted.SignTx([]stypes.TxOutItem{tx}, 100)
					c.Assert(err, IsNil)
					var transfer etypes.Transaction
					c.Assert(transfer.UnmarshalJSON(raw), IsNil)
					c.Assert(transfer.Nonce(), Equals, uint64(1))
				}
				c.Assert(manager.calls, Equals, 2)
				c.Assert(rpc.submitted, HasLen, 0)
			}
		}()
	}
}

func (s *EVMSuite) TestApprovalRetirementRequiresProvenMinedFailure(c *C) {
	for _, tc := range []struct {
		name                                  string
		missing, rpcError, wrongHash, noBlock bool
		status                                uint64
		retire                                bool
	}{
		{name: "mined revert", status: etypes.ReceiptStatusFailed, retire: true},
		{name: "receipt missing", missing: true},
		{name: "receipt RPC failed", rpcError: true},
		{name: "wrong receipt hash", wrongHash: true},
		{name: "receipt not mined", noBlock: true},
		{name: "unknown receipt status", status: 2},
	} {
		func() {
			client, tx, rpc, cleanup := s.approvalRetryFixture(c)
			defer cleanup()
			for i := 0; i < 2; i++ {
				_, checkpoint, _, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
				c.Assert(errors.Is(err, errApprovalPending), Equals, true)
				tx.Checkpoint = checkpoint
			}
			mineApproval(c, client, tx, rpc, 0)
			rpc.Lock()
			rpc.receiptStatus = tc.status
			rpc.receiptMissing, rpc.receiptError = tc.missing, tc.rpcError
			rpc.receiptWrongHash, rpc.receiptNoBlock = tc.wrongHash, tc.noBlock
			rpc.Unlock()
			raw, checkpoint, observation, err := client.SignTx([]stypes.TxOutItem{tx}, 100)
			c.Assert(raw, IsNil, Commentf("%s", tc.name))
			c.Assert(observation, IsNil, Commentf("%s", tc.name))
			if tc.retire {
				c.Assert(err, IsNil, Commentf("%s", tc.name))
				c.Assert(checkpoint, IsNil, Commentf("%s", tc.name))
			} else {
				c.Assert(err, NotNil, Commentf("%s", tc.name))
				c.Assert(checkpoint, DeepEquals, tx.Checkpoint, Commentf("%s", tc.name))
			}
			c.Assert(rpc.submitted, HasLen, 0)
		}()
	}
}
