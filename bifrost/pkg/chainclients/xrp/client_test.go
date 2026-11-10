package xrp

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"

	addresscodec "github.com/Peersyst/xrpl-go/address-codec"
	binarycodec "github.com/Peersyst/xrpl-go/binary-codec"
	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/Peersyst/xrpl-go/xrpl/rpc/testutil"
	txtypes "github.com/Peersyst/xrpl-go/xrpl/transaction/types"
)

func TestPackage(t *testing.T) { TestingT(t) }

type XrpTestSuite struct {
	thordir  string
	thorKeys *thorclient.Keys
	bridge   thorclient.ThorchainBridge
	m        *metrics.Metrics
}

var _ = Suite(&XrpTestSuite{})

func testXrpScannerWithLastFee(scanner *XrpBlockScanner, fee sdkmath.Uint) *XrpBlockScanner {
	scanner.setLastFee(fee)
	return scanner
}

var m *metrics.Metrics

func GetMetricForTest(c *C) *metrics.Metrics {
	if m == nil {
		var err error
		m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
			Enabled:      false,
			ListenPort:   9000,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			Chains:       common.Chains{common.XRPChain},
		})
		c.Assert(m, NotNil)
		c.Assert(err, IsNil)
	}
	return m
}

func (s *XrpTestSuite) SetUpSuite(c *C) {
	cosmosSDKConfg := cosmos.GetConfig()
	cosmosSDKConfg.SetBech32PrefixForAccount("sthor", "sthorpub")

	s.m = GetMetricForTest(c)
	c.Assert(s.m, NotNil)
	ns := strconv.Itoa(time.Now().Nanosecond())
	c.Assert(os.Setenv("NET", "stagenet"), IsNil)

	s.thordir = filepath.Join(os.TempDir(), ns, ".thorcli")
	cfg := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: s.thordir,
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic(cfg.SignerName, cKeys.English, cmd.THORChainHDPath, cfg.SignerPasswd, hd.Secp256k1)
	c.Assert(err, IsNil)
	s.thorKeys = thorclient.NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd)
	s.bridge, err = thorclient.NewThorchainBridge(cfg, s.m, s.thorKeys)
	c.Assert(err, IsNil)
}

func (s *XrpTestSuite) TearDownSuite(c *C) {
	c.Assert(os.Unsetenv("NET"), IsNil)
	if err := os.RemoveAll(s.thordir); err != nil {
		c.Error(err)
	}
}

func (s *XrpTestSuite) TestGetAddress(c *C) {
	response := `{
		"result": {
			"account_data": {
				"Flags": 8388608,
				"LedgerEntryType": "AccountRoot",
				"Account": "rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ",
				"Balance": "999999999960",
				"OwnerCount": 0,
				"PreviousTxnID": "4294BEBE5B569A18C0A2702387C9B1E7146DC3A5850C1E87204951C6FDAA4C42",
				"PreviousTxnLgrSeq": 3,
				"Sequence": 6
			},
			"ledger_current_index": 4,
			"queue_data": {
				"txn_count": 5,
				"auth_change_queued": true,
				"lowest_sequence": 6,
				"highest_sequence": 10,
				"max_spend_drops_total": "500",
				"transactions": [
					{
						"auth_change": false,
						"fee": "100",
						"fee_level": "2560",
						"max_spend_drops": "100",
						"seq": 6
					},
					{
						"auth_change": true,
						"fee": "100",
						"fee_level": "2560",
						"max_spend_drops": "100",
						"seq": 10
					}
				]
			},
			"validated": false
		},
		"warning": "none",
		"warnings":
		[{
			"id": 1,
			"message": "message"
		}]
	}`

	mc := &testutil.JSONRPCMockClient{}
	mc.DoFunc = testutil.MockResponse(response, 200, mc)

	cfg, err := rpc.NewClientConfig("http://testnode/", rpc.WithHTTPClient(mc))
	c.Assert(err, IsNil)

	xrpclient := Client{
		cfg:       config.BifrostChainConfiguration{ChainID: common.XRPChain},
		rpcClient: rpc.NewClient(cfg),
	}

	addr := "rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ"
	xrp, _ := common.NewAsset("XRP.XRP")
	expectedCoins := common.NewCoins(
		common.NewCoin(xrp, cosmos.NewUint(99999999996000)),
	)

	acc, err := xrpclient.GetAccountByAddress(addr, big.NewInt(0))
	c.Assert(err, IsNil)
	c.Check(acc.AccountNumber, Equals, int64(0))
	c.Check(acc.Sequence, Equals, int64(6))
	c.Check(acc.Coins.EqualsEx(expectedCoins), Equals, true)

	pk := common.PubKey("sthorpub1addwnpepqtrvka83xluqq522k8at4d84gnthj5ryqhrf0fa24yze4yhk7j0fk4876fz")
	acc, err = xrpclient.GetAccount(pk, big.NewInt(0))
	c.Assert(err, IsNil)
	c.Check(acc.AccountNumber, Equals, int64(0))
	c.Check(acc.Sequence, Equals, int64(6))
	c.Check(acc.Coins.EqualsEx(expectedCoins), Equals, true)

	resultAddr := xrpclient.GetAddress(pk)

	c.Logf(resultAddr)
	c.Check(addr, Equals, resultAddr)
}

func (s *XrpTestSuite) TestProcessOutboundTx(c *C) {
	client, err := NewClient(
		s.thorKeys,
		config.BifrostChainConfiguration{
			ChainID:      common.XRPChain,
			ChainNetwork: "1234",
			RPCHost:      "http://testnode/",
			BlockScanner: config.BifrostBlockScannerConfiguration{
				StartBlockHeight: 1, // avoids querying thorchain for block height
			},
		},
		nil,
		s.bridge,
		s.m,
	)
	c.Assert(err, IsNil)
	c.Check(client.networkID, Equals, uint32(1234))

	vaultPubKey, err := common.NewPubKey("sthorpub1addwnpepqtrvka83xluqq522k8at4d84gnthj5ryqhrf0fa24yze4yhk7j0fk4876fz")
	c.Assert(err, IsNil)
	outAsset, err := common.NewAsset("XRP.XRP")
	c.Assert(err, IsNil)
	toAddress, err := common.NewAddress("rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ")
	c.Assert(err, IsNil)

	paymentAmount := uint64(24528300)
	txOut := stypes.TxOutItem{
		Chain:       common.XRPChain,
		ToAddress:   toAddress,
		VaultPubKey: vaultPubKey,
		Coins:       common.Coins{common.NewCoin(outAsset, cosmos.NewUint(paymentAmount))},
		Memo:        "memo",
		MaxGas:      common.Gas{common.NewCoin(outAsset, cosmos.NewUint(23582400))},
		GasRate:     750000,
		InHash:      "hash",
	}

	msg, err := client.processOutboundTx(txOut)
	c.Assert(err, IsNil)

	c.Check(msg.Amount, Equals, txtypes.XRPCurrencyAmount(paymentAmount/100))
	c.Check(msg.BaseTx.Account, Equals, txtypes.Address("rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ"))
	c.Check(msg.Destination, Equals, txtypes.Address(toAddress.String()))
	c.Check(msg.BaseTx.NetworkID, Equals, uint32(1234))

	signingPubKey, err := vaultPubKey.Secp256K1()
	c.Assert(err, IsNil)
	signingPubKeyHex := hex.EncodeToString(signingPubKey.SerializeCompressed())
	c.Check(msg.BaseTx.SigningPubKey, Equals, signingPubKeyHex)
}

func (s *XrpTestSuite) TestXAddresses(c *C) {
	client, err := NewClient(
		s.thorKeys,
		config.BifrostChainConfiguration{
			ChainID:      common.XRPChain,
			ChainNetwork: "1234",
			RPCHost:      "http://testnode/",
			BlockScanner: config.BifrostBlockScannerConfiguration{
				StartBlockHeight: 1, // avoids querying thorchain for block height
			},
		},
		nil,
		s.bridge,
		s.m,
	)
	c.Assert(err, IsNil)
	c.Check(client.networkID, Equals, uint32(1234))

	vaultPubKey, err := common.NewPubKey("sthorpub1addwnpepqtrvka83xluqq522k8at4d84gnthj5ryqhrf0fa24yze4yhk7j0fk4876fz")
	c.Assert(err, IsNil)
	outAsset, err := common.NewAsset("XRP.XRP")
	c.Assert(err, IsNil)
	classicAddr := "rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ"
	destTag := uint32(177)
	toAddress, err := addresscodec.ClassicAddressToXAddress(classicAddr, destTag, true, false)
	c.Assert(err, IsNil)

	paymentAmount := uint64(24528300)
	txOut := stypes.TxOutItem{
		Chain:       common.XRPChain,
		ToAddress:   common.Address(toAddress),
		VaultPubKey: vaultPubKey,
		Coins:       common.Coins{common.NewCoin(outAsset, cosmos.NewUint(paymentAmount))},
		Memo:        "memo",
		MaxGas:      common.Gas{common.NewCoin(outAsset, cosmos.NewUint(23582400))},
		GasRate:     750000,
		InHash:      "hash",
	}

	msg, err := client.processOutboundTx(txOut)
	c.Assert(err, IsNil)

	c.Check(msg.Amount, Equals, txtypes.XRPCurrencyAmount(paymentAmount/100))
	c.Check(msg.BaseTx.Account, Equals, txtypes.Address("rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ"))
	c.Check(msg.Destination, Not(Equals), txtypes.Address(classicAddr))
}

func (s *XrpTestSuite) TestGetGasEstimateForSolvency(c *C) {
	client := &Client{
		cfg: config.BifrostChainConfiguration{
			ChainID: common.XRPChain,
		},
		xrpScanner: &XrpBlockScanner{},
	}
	client.xrpScanner.setLastFee(sdkmath.NewUint(12)) // drops
	got := client.GetGasEstimateForSolvency()
	c.Assert(got.Equal(cosmos.NewUint(1200)), Equals, true) // 12 drops => 1200 (1e8 units)

	client.xrpScanner.setLastFee(sdkmath.ZeroUint())
	got = client.GetGasEstimateForSolvency()
	c.Assert(got.IsZero(), Equals, true)
}

func (s *XrpTestSuite) TestSign(c *C) {
	client, err := NewClient(
		s.thorKeys,
		config.BifrostChainConfiguration{
			ChainID:      common.XRPChain,
			ChainNetwork: "1234",
			RPCHost:      "http://testnode/",
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ChainID:          common.XRPChain,
				StartBlockHeight: 1, // avoids querying thorchain for block height
			},
		},
		nil,
		s.bridge,
		s.m,
	)
	c.Assert(err, IsNil)
	c.Check(client.networkID, Equals, uint32(1234))

	priv, err := s.thorKeys.GetPrivateKey()
	c.Assert(err, IsNil)
	temp, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	c.Assert(err, IsNil)
	vaultPubKey, err := common.NewPubKeyFromCrypto(temp)
	c.Assert(err, IsNil)
	outAsset, err := common.NewAsset("XRP.XRP")
	c.Assert(err, IsNil)
	toAddress, err := common.NewAddress("rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ")
	c.Assert(err, IsNil)
	txOut := stypes.TxOutItem{
		Chain:       common.GAIAChain,
		ToAddress:   toAddress,
		VaultPubKey: vaultPubKey,
		Coins:       common.Coins{common.NewCoin(outAsset, cosmos.NewUint(2452835200))},
		Memo:        "memo",
		MaxGas:      common.Gas{common.NewCoin(outAsset, cosmos.NewUint(23582400))},
		GasRate:     750000,
		InHash:      "hash",
	}

	msg, err := client.processOutboundTx(txOut)
	c.Assert(err, IsNil)

	msg.Sequence = 1
	msg.Fee = txtypes.XRPCurrencyAmount(txOut.GasRate)
	if txOut.Memo != "" {
		msg.BaseTx.Memos = []txtypes.MemoWrapper{
			{
				Memo: txtypes.Memo{
					MemoData: hex.EncodeToString([]byte(txOut.Memo)),
				},
			},
		}
	}

	// Sign the message (verifies signature within)
	_, err = client.signMsg(msg, vaultPubKey, 0)
	c.Assert(err, IsNil)
}

// TestSignTxSignsValidatedSequence is the serialize-resync regression,
// driven end-to-end through SignTx (not the signingMetadata helper): a fresh
// (checkpoint-less) outbound must be signed at the VALIDATED account sequence, and
// two nodes that read the same validated sequence must produce byte-identical signing
// payloads so the keysign party forms. The sequence comes only from the validated
// read — there is no node-local cache any more — so this catches a regression that
// signs from anything else.
func (s *XrpTestSuite) TestSignTxSignsValidatedSequence(c *C) {
	const validatedSeq = int64(6)
	// The validated read returns 6 (the sequence both nodes must sign at); the current
	// read returns a DIFFERENT, lower value (5) so the test actually proves the signed
	// sequence is the validated one — a regression that signed from the current/open
	// ledger would produce 5. Current is not ahead of validated, so the serial-drain gate
	// does not defer.
	const currentSeq = int64(5)

	outAsset, err := common.NewAsset("XRP.XRP")
	c.Assert(err, IsNil)
	toAddress, err := common.NewAddress("rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ")
	c.Assert(err, IsNil)

	// signOutbound stands up a node pointed at the shared validated account and signs a
	// fixed outbound through the real SignTx, returning the signed blob.
	signOutbound := func() []byte {
		client, err := NewClient(
			s.thorKeys,
			config.BifrostChainConfiguration{
				ChainID:      common.XRPChain,
				ChainNetwork: "1234",
				RPCHost:      "http://testnode/",
				BlockScanner: config.BifrostBlockScannerConfiguration{
					ChainID:          common.XRPChain,
					StartBlockHeight: 1,
				},
			},
			nil,
			s.bridge,
			s.m,
		)
		c.Assert(err, IsNil)

		mc := &testutil.JSONRPCMockClient{}
		mc.DoFunc = func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			resp := acctInfoResp(int(currentSeq), false)
			if strings.Contains(string(body), `"validated"`) {
				resp = acctInfoResp(int(validatedSeq), true)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(resp)))}, nil
		}
		rcfg, err := rpc.NewClientConfig("http://testnode/", rpc.WithHTTPClient(mc))
		c.Assert(err, IsNil)
		client.rpcClient = rpc.NewClient(rcfg)
		client.xrpScanner.rpcClient = rpc.NewClient(rcfg)

		priv, err := s.thorKeys.GetPrivateKey()
		c.Assert(err, IsNil)
		temp, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
		c.Assert(err, IsNil)
		vaultPubKey, err := common.NewPubKeyFromCrypto(temp)
		c.Assert(err, IsNil)

		txOut := stypes.TxOutItem{
			Chain:       common.XRPChain,
			ToAddress:   toAddress,
			VaultPubKey: vaultPubKey,
			Coins:       common.Coins{common.NewCoin(outAsset, cosmos.NewUint(2452835200))},
			Memo:        "memo",
			MaxGas:      common.Gas{common.NewCoin(outAsset, cosmos.NewUint(23582400))},
			GasRate:     750000,
			InHash:      "hash",
		}

		signedTx, _, txIn, signErr := client.SignTx(txOut, 100)
		c.Assert(signErr, IsNil)
		c.Check(signedTx, NotNil)
		c.Check(txIn, NotNil)
		return signedTx
	}

	signingPayload := func(signedTx []byte) string {
		flatTx, err := binarycodec.Decode(hex.EncodeToString(signedTx))
		c.Assert(err, IsNil)
		// SignTx must use the validated sequence.
		c.Check(fmt.Sprintf("%v", flatTx["Sequence"]), Equals, fmt.Sprintf("%d", validatedSeq),
			Commentf("signed Sequence = %v, want the validated %d", flatTx["Sequence"], validatedSeq))
		// The signing payload is the blob minus the signature, re-encoded for signing —
		// this is what every keysign party must agree on.
		delete(flatTx, "TxnSignature")
		payload, err := binarycodec.EncodeForSigning(flatTx)
		c.Assert(err, IsNil)
		return payload
	}

	// Two nodes reading the same validated sequence derive identical signing payloads.
	payloadA := signingPayload(signOutbound())
	payloadB := signingPayload(signOutbound())
	c.Check(payloadA, Equals, payloadB)
}

// Test signature verification with a signature that needs padding
func (s *XrpTestSuite) TestSignatureRequiringPadding(c *C) {
	signBytesHex := "5354580012000021000004d224000000016140000000017645e06840000000000b71b07321030cef2112503d3a56d2d48b3a0f0f6503e4353400f450f9dbf344d182e7c7069c8114d4b66bcf790babd0032c5dbfdc14ff1c643a4f488314fdffd00f2f2d215ecdad483b99be1dad2259b9c3f9ea7d046d656d6fe1f1"
	signatureHex := "30440221008e9bc0a8d7927f1874d318bc2a57691b5321d618dd57857d64402be0e0bc0007021f4ab281ef93b0c448a9e75d6c07e9cf05ee705a3c51aafa61992510b65a03ae"
	compressedPubKeyHex := "030cef2112503d3a56d2d48b3a0f0f6503e4353400f450f9dbf344d182e7c7069c"

	signBytes, err := hex.DecodeString(signBytesHex)
	c.Assert(err, IsNil)
	signature, err := hex.DecodeString(signatureHex)
	c.Assert(err, IsNil)
	compressedPubKey, err := hex.DecodeString(compressedPubKeyHex)
	c.Assert(err, IsNil)

	verified := verifySignature(signBytes, signature, compressedPubKey)
	c.Check(verified, Equals, true)
}

// xrpClientWithTxResponse stands up a client whose RPC returns `resp` (a JSON-RPC
// result string) for every request — enough to drive xrpTxStatusOf / the BroadcastTx
// top-level on-chain check, which issue a single tx lookup.
func (s *XrpTestSuite) xrpClientWithTxResponse(c *C, resp string) *Client {
	client, err := NewClient(
		s.thorKeys,
		config.BifrostChainConfiguration{
			ChainID:      common.XRPChain,
			ChainNetwork: "1234",
			RPCHost:      "http://testnode/",
			BlockScanner: config.BifrostBlockScannerConfiguration{ChainID: common.XRPChain, StartBlockHeight: 1},
		},
		nil, s.bridge, s.m,
	)
	c.Assert(err, IsNil)
	mc := &testutil.JSONRPCMockClient{}
	mc.DoFunc = testutil.MockResponse(resp, 200, mc)
	rcfg, err := rpc.NewClientConfig("http://testnode/", rpc.WithHTTPClient(mc))
	c.Assert(err, IsNil)
	client.rpcClient = rpc.NewClient(rcfg)
	return client
}

// TestXrpTxStatusOf covers the on-chain oracle that decides whether a broadcast is truly
// sent: only a validated tesSUCCESS counts; a validated tec* is a failure, txnNotFound is
// a definite absence, and anything else (not-yet-validated / RPC error) is unknown.
func (s *XrpTestSuite) TestXrpTxStatusOf(c *C) {
	cases := []struct {
		name string
		resp string
		want xrpTxStatus
	}{
		{"validated success", `{"result":{"validated":true,"meta":{"TransactionResult":"tesSUCCESS"},"hash":"ABC"}}`, xrpTxValidatedSuccess},
		{"validated tec", `{"result":{"validated":true,"meta":{"TransactionResult":"tecPATH_DRY"},"hash":"ABC"}}`, xrpTxValidatedFailed},
		{"not found", `{"result":{"error":"txnNotFound","status":"error"}}`, xrpTxNotFound},
		{"seen not validated", `{"result":{"validated":false,"hash":"ABC"}}`, xrpTxUnknown},
		{"other rpc error", `{"result":{"error":"amendmentBlocked","status":"error"}}`, xrpTxUnknown},
		// fail-safe: validated but the result is missing / non-string / unrecognized must be
		// Unknown (never a definite failure that could drive a double-paying re-sign).
		{"validated meta no result", `{"result":{"validated":true,"meta":{},"hash":"ABC"}}`, xrpTxUnknown},
		{"validated result non-string", `{"result":{"validated":true,"meta":{"TransactionResult":7},"hash":"ABC"}}`, xrpTxUnknown},
	}
	for _, tc := range cases {
		client := s.xrpClientWithTxResponse(c, tc.resp)
		c.Check(client.xrpTxStatusOf("ABC"), Equals, tc.want, Commentf("%s", tc.name))
	}
}

// signedBlobBytes returns a minimal but hashable signed XRP tx blob (SignTxBlob requires a
// signature field), for driving BroadcastTx.
func (s *XrpTestSuite) signedBlobBytes(c *C) []byte {
	blobHex, err := binarycodec.Encode(map[string]any{
		"TransactionType": "Payment",
		"Sequence":        uint32(6),
		"SigningPubKey":   "02" + "11223344556677889900AABBCCDDEEFF11223344556677889900AABBCCDDEEFF",
		"TxnSignature":    "3045022100AA",
	})
	c.Assert(err, IsNil)
	b, err := hex.DecodeString(blobHex)
	c.Assert(err, IsNil)
	return b
}

// xrpClientWithRPCByMethod stands up a client whose RPC returns submitResp for a "submit"
// request and txResp for a "tx" request, so BroadcastTx's submit + on-chain confirm steps
// can be driven independently.
func (s *XrpTestSuite) xrpClientWithRPCByMethod(c *C, submitResp, txResp string) *Client {
	client, err := NewClient(
		s.thorKeys,
		config.BifrostChainConfiguration{
			ChainID:      common.XRPChain,
			ChainNetwork: "1234",
			RPCHost:      "http://testnode/",
			BlockScanner: config.BifrostBlockScannerConfiguration{ChainID: common.XRPChain, StartBlockHeight: 1},
		},
		nil, s.bridge, s.m,
	)
	c.Assert(err, IsNil)
	mc := &testutil.JSONRPCMockClient{}
	mc.DoFunc = func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		resp := txResp
		if strings.Contains(string(body), `"submit"`) {
			resp = submitResp
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(resp)))}, nil
	}
	rcfg, err := rpc.NewClientConfig("http://testnode/", rpc.WithHTTPClient(mc))
	c.Assert(err, IsNil)
	client.rpcClient = rpc.NewClient(rcfg)
	return client
}

// TestBroadcastTxTefPastSeqNotFoundRetries is the double-pay guard: a submit that returns
// tefPAST_SEQ while the tx lookup says txnNotFound (e.g. a pruned/lagging node that dropped
// a tx it actually delivered) must NOT re-sign (that could pay twice) — it returns a plain
// retry error, never ErrResignRequired.
func (s *XrpTestSuite) TestBroadcastTxTefPastSeqNotFoundRetries(c *C) {
	submit := `{"result":{"engine_result":"tefPAST_SEQ","status":"success"}}`
	notFound := `{"result":{"error":"txnNotFound","status":"error"}}`
	client := s.xrpClientWithRPCByMethod(c, submit, notFound)
	_, err := client.BroadcastTx(stypes.TxOutItem{Chain: common.XRPChain}, s.signedBlobBytes(c))
	c.Check(err, NotNil)
	c.Check(errors.Is(err, stypes.ErrResignRequired), Equals, false, Commentf("tefPAST_SEQ+notfound must NOT re-sign (double-pay risk), got %v", err))
}

// TestBroadcastTxValidatedFailedResigns: a tx already validated with a tec* result (the
// sequence burned, nothing delivered) must NOT be cached as sent — BroadcastTx returns
// ErrResignRequired so the signer re-signs at a fresh sequence.
func (s *XrpTestSuite) TestBroadcastTxValidatedFailedResigns(c *C) {
	client := s.xrpClientWithTxResponse(c, `{"result":{"validated":true,"meta":{"TransactionResult":"tecUNFUNDED_PAYMENT"},"hash":"ABC"}}`)
	_, err := client.BroadcastTx(stypes.TxOutItem{Chain: common.XRPChain}, s.signedBlobBytes(c))
	c.Check(errors.Is(err, stypes.ErrResignRequired), Equals, true, Commentf("tec* outbound must trigger re-sign, got %v", err))
}

// TestBroadcastTxValidatedSuccessCaches: a tx already validated with tesSUCCESS is truly
// sent — BroadcastTx returns its hash with no error and does not re-submit.
func (s *XrpTestSuite) TestBroadcastTxValidatedSuccessCaches(c *C) {
	client := s.xrpClientWithTxResponse(c, `{"result":{"validated":true,"meta":{"TransactionResult":"tesSUCCESS"},"hash":"ABC"}}`)
	hash, err := client.BroadcastTx(stypes.TxOutItem{Chain: common.XRPChain}, s.signedBlobBytes(c))
	c.Check(err, IsNil)
	c.Check(hash != "", Equals, true)
}

// acctInfoResp builds an account_info result with the given sequence + validated flag.
func acctInfoResp(seq int, validated bool) string {
	v := "false"
	if validated {
		v = "true"
	}
	return `{"result":{"account_data":{"Account":"rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ","Balance":"999999999960","Flags":0,"LedgerEntryType":"AccountRoot","OwnerCount":0,"PreviousTxnID":"4294BEBE5B569A18C0A2702387C9B1E7146DC3A5850C1E87204951C6FDAA4C42","PreviousTxnLgrSeq":3,"Sequence":` + strconv.Itoa(seq) + `},"ledger_index":4,"validated":` + v + `}}`
}

// xrpClientWithAccountByLedger stands up a client whose account_info returns validatedSeq
// for a validated-ledger read and currentSeq for a current-ledger read — enough to drive
// the serial-drain gate in signingMetadata.
func (s *XrpTestSuite) xrpClientWithAccountByLedger(c *C, validatedSeq, currentSeq int) (*Client, common.PubKey) {
	client, err := NewClient(
		s.thorKeys,
		config.BifrostChainConfiguration{
			ChainID:      common.XRPChain,
			ChainNetwork: "1234",
			RPCHost:      "http://testnode/",
			BlockScanner: config.BifrostBlockScannerConfiguration{ChainID: common.XRPChain, StartBlockHeight: 1},
		},
		nil, s.bridge, s.m,
	)
	c.Assert(err, IsNil)
	mc := &testutil.JSONRPCMockClient{}
	mc.DoFunc = func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		resp := acctInfoResp(currentSeq, false)
		if strings.Contains(string(body), `"validated"`) {
			resp = acctInfoResp(validatedSeq, true)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(resp)))}, nil
	}
	rcfg, err := rpc.NewClientConfig("http://testnode/", rpc.WithHTTPClient(mc))
	c.Assert(err, IsNil)
	client.rpcClient = rpc.NewClient(rcfg)
	client.xrpScanner.rpcClient = rpc.NewClient(rcfg)

	priv, err := s.thorKeys.GetPrivateKey()
	c.Assert(err, IsNil)
	temp, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	c.Assert(err, IsNil)
	vaultPubKey, err := common.NewPubKeyFromCrypto(temp)
	c.Assert(err, IsNil)
	return client, vaultPubKey
}

// TestSigningMetadataInFlightGate covers the serial-drain gate: a NEW outbound defers
// (ErrTxPending, no checkpoint) while the open ledger is ahead of the validated ledger
// (a prior outbound applied but not yet validated), and proceeds once the open ledger
// falls back level with the validated one (the prior tx validated, or was dropped).
func (s *XrpTestSuite) TestSigningMetadataInFlightGate(c *C) {
	toAddress, err := common.NewAddress("rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ")
	c.Assert(err, IsNil)
	outAsset, err := common.NewAsset("XRP.XRP")
	c.Assert(err, IsNil)
	mkTx := func(vault common.PubKey) stypes.TxOutItem {
		return stypes.TxOutItem{
			Chain:       common.XRPChain,
			ToAddress:   toAddress,
			VaultPubKey: vault,
			Coins:       common.Coins{common.NewCoin(outAsset, cosmos.NewUint(2452835200))},
			Memo:        "memo",
			GasRate:     750000,
			InHash:      "hash",
		}
	}

	// In-flight: current (7) ahead of validated (6) -> defer, no checkpoint.
	client, vault := s.xrpClientWithAccountByLedger(c, 6, 7)
	_, err = client.signingMetadata(mkTx(vault), 1)
	c.Check(errors.Is(err, chainclienttypes.ErrTxPending), Equals, true, Commentf("in-flight outbound must defer, got %v", err))
	// The SignTx half of the gate contract: it surfaces ErrTxPending with NO signature and
	// NO checkpoint (the gate returns before any keysign), so the signer retains the item.
	signedTx, checkpoint, txIn, sErr := client.SignTx(mkTx(vault), 100)
	c.Check(errors.Is(sErr, chainclienttypes.ErrTxPending), Equals, true, Commentf("SignTx must surface ErrTxPending, got %v", sErr))
	c.Check(signedTx, IsNil, Commentf("no signature on a deferred sign"))
	c.Check(checkpoint, IsNil, Commentf("no checkpoint on a deferred sign"))
	c.Check(txIn, IsNil)

	// Cleared: current level with validated (6) -> sign at the validated sequence.
	client2, vault2 := s.xrpClientWithAccountByLedger(c, 6, 6)
	meta, err := client2.signingMetadata(mkTx(vault2), 1)
	c.Assert(err, IsNil)
	c.Check(meta.SeqNumber, Equals, int64(6))
}

// TestGetVaultLock covers the per-vault serialization lock: the same vault returns the
// same mutex (so sign+broadcast serialize), and distinct vaults return distinct mutexes
// (so different vaults are not serialized against each other).
func (s *XrpTestSuite) TestGetVaultLock(c *C) {
	client := &Client{vaultLocks: make(map[string]*sync.Mutex)}
	a1 := client.GetVaultLock("vaultA")
	a2 := client.GetVaultLock("vaultA")
	b1 := client.GetVaultLock("vaultB")
	c.Check(a1, Equals, a2, Commentf("same vault must return the same lock"))
	c.Check(a1 == b1, Equals, false, Commentf("different vaults must return different locks"))
	// Holding vaultA's lock blocks vaultA but not vaultB.
	a1.Lock()
	defer a1.Unlock()
	c.Check(a2.TryLock(), Equals, false, Commentf("vaultA lock must be held while a1 is locked"))
	c.Check(b1.TryLock(), Equals, true, Commentf("vaultB lock must be independent of vaultA"))
	b1.Unlock()
}

// xrpClientWithSubmitThenConfirm stands up a client whose "submit" request returns
// submitResp, whose FIRST "tx" lookup returns preTx (the pre-submit on-chain check) and
// whose subsequent "tx" lookups return postTx (the post-submit confirm). This isolates
// BroadcastTx's post-submit tefPAST_SEQ/tefALREADY confirm branches, which the pre-submit
// check would otherwise short-circuit when preTx is already a validated result.
func (s *XrpTestSuite) xrpClientWithSubmitThenConfirm(c *C, submitResp, preTx, postTx string) *Client {
	client, err := NewClient(
		s.thorKeys,
		config.BifrostChainConfiguration{
			ChainID:      common.XRPChain,
			ChainNetwork: "1234",
			RPCHost:      "http://testnode/",
			BlockScanner: config.BifrostBlockScannerConfiguration{ChainID: common.XRPChain, StartBlockHeight: 1},
		},
		nil, s.bridge, s.m,
	)
	c.Assert(err, IsNil)
	txCalls := 0
	mc := &testutil.JSONRPCMockClient{}
	mc.DoFunc = func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		var resp string
		if strings.Contains(string(body), `"submit"`) {
			resp = submitResp
		} else {
			if txCalls == 0 {
				resp = preTx
			} else {
				resp = postTx
			}
			txCalls++
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(resp)))}, nil
	}
	rcfg, err := rpc.NewClientConfig("http://testnode/", rpc.WithHTTPClient(mc))
	c.Assert(err, IsNil)
	client.rpcClient = rpc.NewClient(rcfg)
	return client
}

// TestBroadcastTxPostSubmitConfirm pins the post-submit fund-safety branches: after an
// ambiguous accept (tefPAST_SEQ / tefALREADY), BroadcastTx caches as sent ONLY on a
// validated tesSUCCESS, re-signs (ErrResignRequired) ONLY on a validated tec* failure, and
// otherwise (txnNotFound / not-yet-validated) returns a plain retry — never a re-sign, never
// a cache. The pre-submit lookup is forced to txnNotFound so these post-submit paths run.
func (s *XrpTestSuite) TestBroadcastTxPostSubmitConfirm(c *C) {
	const (
		valSuccess = `{"result":{"validated":true,"meta":{"TransactionResult":"tesSUCCESS"},"hash":"ABC"}}`
		valTec     = `{"result":{"validated":true,"meta":{"TransactionResult":"tecUNFUNDED_PAYMENT"},"hash":"ABC"}}`
		notFound   = `{"result":{"error":"txnNotFound","status":"error"}}`
		notYet     = `{"result":{"validated":false,"hash":"ABC"}}`
	)
	cases := []struct {
		name       string
		engineRes  string
		confirm    string
		wantResign bool // expect ErrResignRequired
		wantErr    bool // expect a (non-resign) retry error rather than a cached send
	}{
		{"tefPAST_SEQ+success caches", "tefPAST_SEQ", valSuccess, false, false},
		{"tefPAST_SEQ+tec resigns", "tefPAST_SEQ", valTec, true, true},
		{"tefPAST_SEQ+notyet retries", "tefPAST_SEQ", notYet, false, true},
		{"tefALREADY+success caches", "tefALREADY", valSuccess, false, false},
		{"tefALREADY+tec resigns", "tefALREADY", valTec, true, true},
		{"tefALREADY+notfound retries", "tefALREADY", notFound, false, true},
		{"tefALREADY+notyet retries", "tefALREADY", notYet, false, true},
	}
	for _, tc := range cases {
		submit := `{"result":{"engine_result":"` + tc.engineRes + `","status":"success"}}`
		client := s.xrpClientWithSubmitThenConfirm(c, submit, notFound, tc.confirm)
		tx := stypes.TxOutItem{Chain: common.XRPChain}
		h, err := client.BroadcastTx(tx, s.signedBlobBytes(c))
		c.Check(errors.Is(err, stypes.ErrResignRequired), Equals, tc.wantResign,
			Commentf("%s: want resign=%v, got err=%v", tc.name, tc.wantResign, err))
		// Assert the actual signer-cache side effect, not just the return value: an item is
		// cached as sent IFF the broadcast resolved to a success — a retry or a re-sign must
		// leave NO cache entry (caching a non-delivered tx would silently drop the outbound).
		cached := client.signerCacheManager.HasSigned(tx.CacheHash())
		if tc.wantErr {
			c.Check(err, NotNil, Commentf("%s: expected a retry error (no cache)", tc.name))
			c.Check(cached, Equals, false, Commentf("%s: a non-success broadcast must NOT cache", tc.name))
		} else {
			c.Check(err, IsNil, Commentf("%s: expected cache+return, got %v", tc.name, err))
			c.Check(h != "", Equals, true, Commentf("%s: expected a tx hash", tc.name))
			c.Check(cached, Equals, true, Commentf("%s: a validated success must be cached as sent", tc.name))
		}
	}
}

// TestSigningMetadataHalfReadErrors pins the half-read guard: if the validated read
// succeeds but the current (open) ledger read fails, signingMetadata returns an error and
// does NOT sign and does NOT defer — it never proceeds on a half-read (which could pin a
// stale sequence) and never conflates a read failure with the in-flight ErrTxPending defer.
func (s *XrpTestSuite) TestSigningMetadataHalfReadErrors(c *C) {
	client, err := NewClient(
		s.thorKeys,
		config.BifrostChainConfiguration{
			ChainID:      common.XRPChain,
			ChainNetwork: "1234",
			RPCHost:      "http://testnode/",
			BlockScanner: config.BifrostBlockScannerConfiguration{ChainID: common.XRPChain, StartBlockHeight: 1},
		},
		nil, s.bridge, s.m,
	)
	c.Assert(err, IsNil)
	mc := &testutil.JSONRPCMockClient{}
	mc.DoFunc = func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		// Validated read OK; current (non-validated) read fails.
		resp := `{"result":{"error":"backendError","status":"error"}}`
		if strings.Contains(string(body), `"validated"`) {
			resp = acctInfoResp(6, true)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(resp)))}, nil
	}
	rcfg, err := rpc.NewClientConfig("http://testnode/", rpc.WithHTTPClient(mc))
	c.Assert(err, IsNil)
	client.rpcClient = rpc.NewClient(rcfg)

	priv, err := s.thorKeys.GetPrivateKey()
	c.Assert(err, IsNil)
	temp, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	c.Assert(err, IsNil)
	vault, err := common.NewPubKeyFromCrypto(temp)
	c.Assert(err, IsNil)

	toAddress, err := common.NewAddress("rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ")
	c.Assert(err, IsNil)
	outAsset, err := common.NewAsset("XRP.XRP")
	c.Assert(err, IsNil)
	tx := stypes.TxOutItem{
		Chain: common.XRPChain, ToAddress: toAddress, VaultPubKey: vault,
		Coins: common.Coins{common.NewCoin(outAsset, cosmos.NewUint(2452835200))},
		Memo:  "memo", GasRate: 750000, InHash: "hash",
	}
	_, err = client.signingMetadata(tx, 1)
	c.Check(err, NotNil, Commentf("half-read (current read fails) must error"))
	c.Check(errors.Is(err, chainclienttypes.ErrTxPending), Equals, false,
		Commentf("a half-read is a hard error, not the in-flight defer"))
}
