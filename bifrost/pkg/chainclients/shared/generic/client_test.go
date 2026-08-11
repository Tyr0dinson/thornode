package generic

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/hashicorp/go-multierror"
	"github.com/rs/zerolog"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/config"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

func TestClientSuite(t *testing.T) { TestingT(t) }

type ClientSuite struct {
	localKeys *thorclient.Keys
	vaultKeys *thorclient.Keys
}

type mockBridge struct {
	thorclient.ThorchainBridge
	lastBlame  stypes.Blame
	lastHeight int64
	lastMemo   string
	lastCoins  common.Coins
	lastPubKey common.PubKey
	callCount  int
	postErr    error
}

func (m *mockBridge) PostKeysignFailure(
	blame stypes.Blame,
	height int64,
	memo string,
	coins common.Coins,
	pubkey common.PubKey,
) (common.TxID, error) {
	m.callCount++
	m.lastBlame = blame
	m.lastHeight = height
	m.lastMemo = memo
	m.lastCoins = coins
	m.lastPubKey = pubkey
	if m.postErr != nil {
		return common.BlankTxID, m.postErr
	}
	return "MOCKTXID", nil
}

type mockTssKeyManager struct {
	keyManager LocalKeyManager
	forcedErr  error
}

func (m *mockTssKeyManager) GetPrivKey() crypto.PrivKey          { return nil }
func (m *mockTssKeyManager) GetAddr() sdktypes.AccAddress        { return nil }
func (m *mockTssKeyManager) ExportAsMnemonic() (string, error)   { return "", nil }
func (m *mockTssKeyManager) ExportAsPrivateKey() (string, error) { return "", nil }
func (m *mockTssKeyManager) ExportAsKeyStore(string) (*tss.EncryptedKeyJSON, error) {
	return nil, nil
}
func (m *mockTssKeyManager) Start() {}
func (m *mockTssKeyManager) Stop()  {}
func (m *mockTssKeyManager) RemoteSign(
	msg []byte,
	algo common.SigningAlgo,
	_ string,
	_ int64,
) ([]byte, []byte, error) {
	if m.forcedErr != nil {
		return nil, nil, m.forcedErr
	}
	sig, err := m.keyManager.Sign(msg)
	if err != nil {
		return nil, nil, err
	}
	if algo == common.SigningAlgoSecp256k1 {
		return sig[:64], sig[64:65], nil
	}
	return sig, nil, nil
}

var _ = Suite(&ClientSuite{})

func (s *ClientSuite) SetUpSuite(c *C) {
	cosmosSDKConfg := cosmos.GetConfig()
	cosmosSDKConfg.SetBech32PrefixForAccount("tthor", "tthorpub")

	s.localKeys = newKeys("odin", "password", c)
	s.vaultKeys = newKeys("thor", "password", c)
}

func newKeys(name, pass string, c *C) *thorclient.Keys {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)

	kb := keyring.NewInMemory(
		codec.NewProtoCodec(registry),
		func(opts *keyring.Options) {
			opts.SupportedAlgos = append(opts.SupportedAlgos, ed25519.Ed25519)
		},
	)

	// secp256k1 entry under `name`
	_, _, err := kb.NewMnemonic(
		name, keyring.English, cmd.THORChainHDPath, pass, hd.Secp256k1,
	)
	c.Assert(err, IsNil)

	// ed25519 entry under `SignerNameEDDSA(name)` (e.g. "ed-" + name)
	_, _, err = kb.NewMnemonic(
		ed25519.SignerNameEDDSA(name),
		keyring.English,
		"",
		pass,
		ed25519.Ed25519,
	)
	c.Assert(err, IsNil)

	return thorclient.NewKeysWithKeybase(kb, name, pass)
}

func (s *ClientSuite) setUpForTest(
	chain common.Chain,
	forcedErr error,
	c *C,
) (*Client, common.PubKeySet) {
	localKeyManager, err := NewLocalKeyManager(s.localKeys, chain)
	c.Assert(err, IsNil)

	vaultKeyManager, err := NewLocalKeyManager(s.vaultKeys, chain)
	c.Assert(err, IsNil)

	client := Client{
		logger: zerolog.Nop(),
		config: config.BifrostChainConfiguration{
			ChainID: chain,
		},
		bridge:          &mockBridge{},
		localKeyManager: localKeyManager,
		tssKeyManager: &mockTssKeyManager{
			keyManager: vaultKeyManager,
			forcedErr:  forcedErr,
		},
	}

	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	c.Assert(err, IsNil)

	client.signerCacheManager, err = signercache.NewSignerCacheManager(db)
	c.Assert(err, IsNil)

	// tmp key manager to get all pubkeys
	tmpSecp256k1, err := NewLocalKeyManager(s.vaultKeys, common.BTCChain)
	c.Assert(err, IsNil)

	tmpEd25519, err := NewLocalKeyManager(s.vaultKeys, common.SOLChain)
	c.Assert(err, IsNil)

	pubKeys := common.PubKeySet{
		Secp256k1: tmpSecp256k1.PubKey(),
		Ed25519:   tmpEd25519.PubKey(),
	}

	return &client, pubKeys
}

func (s *ClientSuite) TestSign(c *C) {
	testCases := []struct {
		Chain common.Chain
		Local bool
	}{
		{
			Chain: common.BTCChain,
			Local: false,
		},
		{
			Chain: common.ETHChain,
			Local: true,
		},
		{
			Chain: common.SOLChain,
			Local: false,
		},
		{
			Chain: common.TAOChain,
			Local: true,
		},
	}

	for _, tc := range testCases {
		client, vaultPubKeys := s.setUpForTest(tc.Chain, nil, c)
		label := Commentf("chain=%s local=%v", tc.Chain, tc.Local)

		txOutItem := types.TxOutItem{
			Chain: tc.Chain,
		}

		var verifyPubKey common.PubKey
		if tc.Local {
			if tc.Chain.GetSigningAlgo() == common.SigningAlgoSecp256k1 {
				txOutItem.VaultPubKey = client.GetPubKey()
				txOutItem.VaultPubKeyEddsa = common.EmptyPubKey
			} else {
				txOutItem.VaultPubKeyEddsa = client.GetPubKey()
				txOutItem.VaultPubKey = common.EmptyPubKey
			}

			verifyPubKey = client.GetPubKey()
		} else {
			txOutItem.VaultPubKey = vaultPubKeys.Secp256k1
			txOutItem.VaultPubKeyEddsa = vaultPubKeys.Ed25519

			if tc.Chain.GetSigningAlgo() == common.SigningAlgoSecp256k1 {
				verifyPubKey = vaultPubKeys.Secp256k1
			} else {
				verifyPubKey = vaultPubKeys.Ed25519
			}

			c.Assert(client.GetPubKey().Equals(txOutItem.VaultPubKey), Equals, false, label)
			c.Assert(client.GetPubKey().Equals(txOutItem.VaultPubKeyEddsa), Equals, false, label)
		}

		msg := []byte("d22d2f71fad067ae897af825c36e09b3")
		sig, err := client.Sign(msg, txOutItem, 0)
		c.Assert(err, IsNil, label)
		c.Assert(verifyPubKey.IsEmpty(), Equals, false, label)

		switch tc.Chain.GetSigningAlgo() {
		case common.SigningAlgoSecp256k1:
			c.Assert(len(sig), Equals, 65, label)
			c.Assert(verifySecp256k1(msg, sig, verifyPubKey), IsNil, label)
			c.Assert(verifyEd25519(msg, sig, vaultPubKeys.Ed25519), NotNil, label)
		case common.SigningAlgoEd25519:
			c.Assert(len(sig), Equals, 64, label)
			c.Assert(verifyEd25519(msg, sig, verifyPubKey), IsNil, label)
			c.Assert(verifySecp256k1(msg, sig, vaultPubKeys.Secp256k1), NotNil, label)
		default:
			panic(fmt.Errorf("unknown signing algo: %v", tc.Chain.GetSigningAlgo()))
		}
	}
}

func (s *ClientSuite) TestVerify(c *C) {
	client, vaultPubKeys := s.setUpForTest(common.BTCChain, nil, c)

	txOutItem := types.TxOutItem{
		Chain:            common.BTCChain,
		VaultPubKey:      vaultPubKeys.Secp256k1,
		VaultPubKeyEddsa: vaultPubKeys.Ed25519,
	}

	msg := []byte("66d2d6b5eb564972c59e4797683a1225")
	sig, err := client.Sign(msg, txOutItem, 0)
	c.Assert(err, IsNil)
	c.Assert(len(sig), Equals, 65)

	c.Assert(verifySecp256k1(nil, sig, vaultPubKeys.Secp256k1), NotNil)
	c.Assert(verifySecp256k1(msg, nil, vaultPubKeys.Secp256k1), NotNil)
	c.Assert(verifySecp256k1(msg, sig, common.EmptyPubKey), NotNil)

	c.Assert(verifyEd25519(nil, sig, vaultPubKeys.Ed25519), NotNil)
	c.Assert(verifyEd25519(msg, nil, vaultPubKeys.Ed25519), NotNil)
	c.Assert(verifyEd25519(msg, sig, common.EmptyPubKey), NotNil)
}

func (s *ClientSuite) TestSignErrors(c *C) {
	blameNodes := []stypes.Node{{
		Pubkey:         "",
		BlameData:      nil,
		BlameSignature: nil,
	}}

	testCases := []struct {
		Case              string
		ForcedError       error
		BridgeError       error
		ExpectForcedError bool
		ExpectMultiError  bool
		ExpectPostCalled  bool
	}{
		{
			Case:              "blame forwarded",
			ForcedError:       tss.KeysignError{Blame: stypes.Blame{BlameNodes: blameNodes}},
			ExpectForcedError: true,
			ExpectPostCalled:  true,
		},
		{
			Case:              "empty blame",
			ForcedError:       tss.KeysignError{Blame: stypes.Blame{}}, // no BlameNodes
			ExpectForcedError: true,
			ExpectPostCalled:  false,
		},
		{
			Case:             "post fails",
			ForcedError:      tss.KeysignError{Blame: stypes.Blame{BlameNodes: blameNodes}},
			BridgeError:      fmt.Errorf("post failed"),
			ExpectMultiError: true,
			ExpectPostCalled: true,
		},
		{
			Case:              "non-keysign error",
			ForcedError:       fmt.Errorf("rpc down"),
			ExpectForcedError: true,
			ExpectPostCalled:  false,
		},
	}

	for i, tc := range testCases {
		client, vaultPubKeys := s.setUpForTest(common.BTCChain, tc.ForcedError, c)
		bridge, ok := client.bridge.(*mockBridge)
		c.Assert(ok, Equals, true)
		bridge.postErr = tc.BridgeError
		label := Commentf("case=%s", tc.Case)

		height := int64(i+1) * 1000
		hash := sha256.Sum256([]byte(tc.Case))
		coin := common.NewCoin(client.GetGasAsset(), cosmos.NewUint(12345))

		txOutItem := types.TxOutItem{
			Chain:            client.GetChain(),
			VaultPubKey:      vaultPubKeys.Secp256k1,
			VaultPubKeyEddsa: vaultPubKeys.Ed25519,
			Height:           height,
			Memo:             fmt.Sprintf("OUT:%X", hash),
			Coins:            common.Coins{coin},
		}

		sig, err := client.Sign(hash[:], txOutItem, 0)
		c.Assert(sig, IsNil)
		c.Assert(err, NotNil)

		if tc.ExpectForcedError {
			c.Assert(err, DeepEquals, tc.ForcedError, label)
		}

		if tc.ExpectMultiError {
			expected := multierror.Append(tc.ForcedError, tc.BridgeError)
			c.Assert(err, DeepEquals, expected, label)
		}

		if tc.ExpectPostCalled {
			c.Assert(bridge.callCount, Equals, 1, label)
			c.Assert(bridge.lastHeight, Equals, height, label)
			c.Assert(bridge.lastMemo, Equals, txOutItem.Memo, label)
			c.Assert(bridge.lastCoins, DeepEquals, txOutItem.Coins, label)
			c.Assert(bridge.lastPubKey.Equals(txOutItem.VaultPubKey), Equals, true, label)
			c.Assert(bridge.lastBlame.BlameNodes, DeepEquals, blameNodes, label)
		} else {
			c.Assert(bridge.callCount, Equals, 0, label)
		}
	}
}

func (s *ClientSuite) TestOnObservedTxIn(c *C) {
	inboundHash := strings.Repeat("A", 64)

	testCases := []struct {
		Case         string
		Memo         string
		ExpectCached bool
	}{
		{
			Case:         "unparsable memo",
			Memo:         "garbage",
			ExpectCached: false,
		},
		{
			Case:         "non-outbound memo (swap)",
			Memo:         "SWAP:BTC.BTC:tb1qxyz",
			ExpectCached: false,
		},
		{
			Case:         "outbound memo with empty txid",
			Memo:         "OUT:",
			ExpectCached: false,
		},
		{
			Case:         "valid outbound memo",
			Memo:         "OUT:" + inboundHash,
			ExpectCached: true,
		},
	}

	for _, tc := range testCases {
		client, _ := s.setUpForTest(common.BTCChain, nil, c)
		label := Commentf("case=%s", tc.Case)

		txInItem := types.TxInItem{
			Tx:   "MOCKTXID",
			Memo: tc.Memo,
		}

		client.OnObservedTxIn(txInItem, 0)

		// For negative cases this hash should not exist.
		cacheHash := txInItem.CacheHash(client.GetChain(), inboundHash)
		c.Assert(
			client.signerCacheManager.HasSigned(cacheHash),
			Equals, tc.ExpectCached, label,
		)
	}
}

func (s *ClientSuite) TestSignerCache(c *C) {
	client, vaultPubKeys := s.setUpForTest(common.BTCChain, nil, c)

	txOutItem := types.TxOutItem{
		Chain:       common.BTCChain,
		VaultPubKey: vaultPubKeys.Secp256k1,
		ToAddress:   "bc1qabc",
		Memo:        "OUT:1234",
		Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100))},
	}

	c.Assert(client.HasSigned(txOutItem), Equals, false)

	c.Assert(client.SetSigned(txOutItem, "TXID123"), IsNil)
	c.Assert(client.HasSigned(txOutItem), Equals, true)
	txHash, err := client.GetSignedTransactionHash(txOutItem)
	c.Assert(err, IsNil)
	c.Assert(txHash, Equals, "TXID123")

	// Populate inbound cache, needed for GetLatestTxForVault
	txInItem := types.TxInItem{
		Tx:                  "INBOUND_TXID",
		Memo:                "OUT:" + strings.Repeat("A", 64),
		ObservedVaultPubKey: vaultPubKeys.Secp256k1,
	}
	client.OnObservedTxIn(txInItem, 0)

	// GetLatestTxForVault returns inbound (empty here) + broadcast
	observed, broadcast, err := client.GetLatestTxForVault(vaultPubKeys.Secp256k1.String())
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, txInItem.Tx)
	c.Assert(broadcast, Equals, "TXID123")
	c.Assert(client.RemoveSigned("TXID123"), IsNil)
	c.Assert(client.HasSigned(txOutItem), Equals, false)
	_, err = client.GetSignedTransactionHash(txOutItem)
	c.Assert(err, NotNil)
}

func (s *ClientSuite) TestShouldReportSolvency(c *C) {
	client, _ := s.setUpForTest(common.BTCChain, nil, c)

	client.config.SolvencyBlocks = -100
	c.Assert(client.ShouldReportSolvency(100), Equals, false)

	client.config.SolvencyBlocks = 0
	c.Assert(client.ShouldReportSolvency(100), Equals, false)

	client.config.SolvencyBlocks = 10
	c.Assert(client.ShouldReportSolvency(100), Equals, true)
	c.Assert(client.ShouldReportSolvency(101), Equals, false)
	c.Assert(client.ShouldReportSolvency(0), Equals, true)
}
