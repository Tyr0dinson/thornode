package utxo

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	btcjson "github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	btcwire "github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	dogewire "github.com/eager7/dogd/wire"
	bchwire "github.com/gcash/bchd/wire"
	ltcwire "github.com/ltcsuite/ltcd/wire"
	"github.com/rs/zerolog/log"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
)

// covRemoteSigner signs digests with a fixed key as TSS would, optionally
// failing or running a hook after producing the signature.
type covRemoteSigner struct {
	tss.ThorchainKeyManager
	priv      *btcec.PrivateKey
	err       error
	afterSign func()
	calls     int
}

func (s *covRemoteSigner) RemoteSign(msg []byte, _ common.SigningAlgo, _ string, _ int64) ([]byte, []byte, error) {
	s.calls++
	if s.err != nil {
		return nil, nil, s.err
	}
	sig, err := s.priv.Sign(msg)
	if err != nil {
		return nil, nil, err
	}
	raw := make([]byte, 64)
	sig.R.FillBytes(raw[:32])
	sig.S.FillBytes(raw[32:])
	if s.afterSign != nil {
		s.afterSign()
	}
	return raw, nil, nil
}

type signUTXOFixtureCov struct {
	client  *Client
	signer  *covRemoteSigner
	txOut   stypes.TxOutItem
	vaultPK []byte // compressed vault pubkey
	otherPK []byte
}

func newSignUTXOFixtureCov(t *testing.T) *signUTXOFixtureCov {
	t.Helper()
	nodeKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatal(err)
	}
	nodePub, err := bech32AccountPubKey(nodeKey)
	if err != nil {
		t.Fatal(err)
	}
	vaultKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatal(err)
	}
	vaultPub, err := bech32AccountPubKey(vaultKey)
	if err != nil {
		t.Fatal(err)
	}
	signer := &covRemoteSigner{priv: vaultKey}
	return &signUTXOFixtureCov{
		client: &Client{
			log:          log.Logger,
			nodePrivKey:  nodeKey,
			nodePubKey:   nodePub,
			tssKeySigner: signer,
		},
		signer:  signer,
		txOut:   stypes.TxOutItem{VaultPubKey: vaultPub},
		vaultPK: vaultKey.PubKey().SerializeCompressed(),
		otherPK: nodeKey.PubKey().SerializeCompressed(),
	}
}

func p2wpkhCov(pk []byte) []byte {
	return append([]byte{0x00, 0x14}, btcutil.Hash160(pk)...)
}

func p2pkhCov(pk []byte) []byte {
	script := append([]byte{0x76, 0xa9, 0x14}, btcutil.Hash160(pk)...)
	return append(script, 0x88, 0xac)
}

// runSignUTXOCasesCov exercises one chain's signUTXO through the TSS signer:
// success, signer failure, script failure and the NULLFAIL failsafe.
func runSignUTXOCasesCov[T any](t *testing.T, f *signUTXOFixtureCov, script func([]byte) []byte,
	newTx func() T, sign func(tx T, sourceScript []byte) error, mutate func(tx T),
	signed func(tx T) bool,
) {
	t.Helper()

	tx := newTx()
	if err := sign(tx, script(f.vaultPK)); err != nil {
		t.Fatalf("expected TSS signing to succeed: %v", err)
	}
	if !signed(tx) {
		t.Fatal("expected the input to carry a signature")
	}
	if f.signer.calls != 1 {
		t.Fatalf("expected one remote sign, got %d", f.signer.calls)
	}

	f.signer.err = errors.New("tss offline")
	err := sign(newTx(), script(f.vaultPK))
	if err == nil || !strings.Contains(err.Error(), "fail to get witness") ||
		!strings.Contains(err.Error(), "tss offline") {
		t.Fatalf("expected signer failure, got %v", err)
	}
	f.signer.err = nil

	// Spending an output locked to a different key fails script validation.
	err = sign(newTx(), script(f.otherPK))
	if err == nil || !strings.Contains(err.Error(), "fail to execute the script") {
		t.Fatalf("expected script failure, got %v", err)
	}

	// A signature that no longer matches the transaction trips NULLFAIL, which
	// is deliberately treated as success to avoid retry loops.
	tx = newTx()
	f.signer.afterSign = func() { mutate(tx) }
	if err = sign(tx, script(f.vaultPK)); err != nil {
		t.Fatalf("expected NULLFAIL failsafe to swallow the error, got %v", err)
	}
	f.signer.afterSign = nil
}

func TestSignUTXOBTCCov(t *testing.T) {
	f := newSignUTXOFixtureCov(t)
	newTx := func() *btcwire.MsgTx {
		tx := btcwire.NewMsgTx(btcwire.TxVersion)
		tx.AddTxIn(btcwire.NewTxIn(btcwire.NewOutPoint(&chainhash.Hash{1}, 0), nil, nil))
		tx.AddTxOut(btcwire.NewTxOut(1000, []byte{0x51}))
		return tx
	}
	runSignUTXOCasesCov(t, f, p2wpkhCov, newTx,
		func(tx *btcwire.MsgTx, script []byte) error {
			return f.client.signUTXOBTC(tx, f.txOut, 5000, script, 0, 1)
		},
		func(tx *btcwire.MsgTx) { tx.TxOut[0].Value++ },
		func(tx *btcwire.MsgTx) bool { return len(tx.TxIn[0].Witness) == 2 },
	)
}

func TestSignUTXOLTCCov(t *testing.T) {
	f := newSignUTXOFixtureCov(t)
	newTx := func() *ltcwire.MsgTx {
		tx := ltcwire.NewMsgTx(ltcwire.TxVersion)
		tx.AddTxIn(ltcwire.NewTxIn(&ltcwire.OutPoint{Index: 1}, nil, nil))
		tx.AddTxOut(ltcwire.NewTxOut(1000, []byte{0x51}))
		return tx
	}
	runSignUTXOCasesCov(t, f, p2wpkhCov, newTx,
		func(tx *ltcwire.MsgTx, script []byte) error {
			return f.client.signUTXOLTC(tx, f.txOut, 5000, script, 0, 1)
		},
		func(tx *ltcwire.MsgTx) { tx.TxOut[0].Value++ },
		func(tx *ltcwire.MsgTx) bool { return len(tx.TxIn[0].Witness) == 2 },
	)
}

func TestSignUTXOBCHCov(t *testing.T) {
	f := newSignUTXOFixtureCov(t)
	newTx := func() *bchwire.MsgTx {
		tx := bchwire.NewMsgTx(bchwire.TxVersion)
		tx.AddTxIn(bchwire.NewTxIn(&bchwire.OutPoint{Index: 1}, nil))
		tx.AddTxOut(bchwire.NewTxOut(1000, []byte{0x51}))
		return tx
	}
	runSignUTXOCasesCov(t, f, p2pkhCov, newTx,
		func(tx *bchwire.MsgTx, script []byte) error {
			return f.client.signUTXOBCH(tx, f.txOut, 5000, script, 0, 1)
		},
		func(tx *bchwire.MsgTx) { tx.TxOut[0].Value++ },
		func(tx *bchwire.MsgTx) bool { return len(tx.TxIn[0].SignatureScript) > 0 },
	)
}

func TestSignUTXODOGECov(t *testing.T) {
	f := newSignUTXOFixtureCov(t)
	newTx := func() *dogewire.MsgTx {
		tx := dogewire.NewMsgTx(dogewire.TxVersion)
		tx.AddTxIn(dogewire.NewTxIn(&dogewire.OutPoint{Index: 1}, nil, nil))
		tx.AddTxOut(dogewire.NewTxOut(1000, []byte{0x51}))
		return tx
	}
	runSignUTXOCasesCov(t, f, p2pkhCov, newTx,
		func(tx *dogewire.MsgTx, script []byte) error {
			return f.client.signUTXODOGE(tx, f.txOut, 5000, script, 0, 1)
		},
		func(tx *dogewire.MsgTx) { tx.TxOut[0].Value++ },
		func(tx *dogewire.MsgTx) bool { return len(tx.TxIn[0].SignatureScript) > 0 },
	)
}

func TestGetAddressesFromScriptPubKeyBTCCov(t *testing.T) {
	c := &Client{log: log.Logger}
	pk := make([]byte, 33)
	pk[0] = 0x02
	pk[32] = 0x01

	// Explicit addresses are returned untouched.
	got := c.getAddressesFromScriptPubKeyBTC(btcjson.ScriptPubKeyResult{Addresses: []string{"a", "b"}})
	if len(got) != 2 || got[0] != "a" {
		t.Fatalf("unexpected addresses %v", got)
	}
	if got = c.getAddressesFromScriptPubKeyBTC(btcjson.ScriptPubKeyResult{}); got != nil {
		t.Fatalf("expected nil for empty script, got %v", got)
	}
	if got = c.getAddressesFromScriptPubKeyBTC(btcjson.ScriptPubKeyResult{Hex: "zz"}); got != nil {
		t.Fatalf("expected nil for bad hex, got %v", got)
	}
	// Truncated push: unparsable script.
	if got = c.getAddressesFromScriptPubKeyBTC(btcjson.ScriptPubKeyResult{Hex: "05"}); got != nil {
		t.Fatalf("expected nil for unparsable script, got %v", got)
	}
	// The address is encoded for the build's network (bcrt1 on mocknet, bc1 on
	// mainnet), so derive the expectation from the client's own chain params.
	want, err := btcutil.NewAddressWitnessPubKeyHash(btcutil.Hash160(pk), c.getChainCfgBTC())
	if err != nil {
		t.Fatal(err)
	}
	got = c.getAddressesFromScriptPubKeyBTC(btcjson.ScriptPubKeyResult{Hex: hex.EncodeToString(p2wpkhCov(pk))})
	if len(got) != 1 || got[0] != want.String() {
		t.Fatalf("expected [%s], got %v", want, got)
	}
}
