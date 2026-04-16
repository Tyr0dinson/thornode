package generic

import (
	"fmt"
	"testing"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
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

	. "gopkg.in/check.v1"
)

func TestClientSuite(t *testing.T) { TestingT(t) }

type ClientSuite struct {
	localKeys *thorclient.Keys
	vaultKeys *thorclient.Keys
}

type mockBridge struct {
	thorclient.ThorchainBridge
}

type mockTssKeyManager struct {
	keyManager LocalKeyManager
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
) ([]byte, []byte, error) {
	// if m.forcedErr != nil {
	// 	return nil, nil, m.forcedErr
	// }
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

func (s *ClientSuite) newClient(chain common.Chain, c *C) (*Client, common.PubKeySet) {
	localKeyManager, err := NewLocalKeyManager(s.localKeys, chain)
	c.Assert(err, IsNil)

	vaultKeyManager, err := NewLocalKeyManager(s.vaultKeys, chain)
	c.Assert(err, IsNil)

	client := Client{
		logger: zerolog.Nop(),
		config: config.BifrostChainConfiguration{
			ChainID: chain,
		},
		bridge:          mockBridge{},
		tssKeyManager:   &mockTssKeyManager{keyManager: vaultKeyManager},
		localKeyManager: localKeyManager,
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
		client, vaultPubKeys := s.newClient(tc.Chain, c)

		txOutItem := types.TxOutItem{
			Chain: tc.Chain,
		}

		label := Commentf("chain=%s local=%v", tc.Chain, tc.Local)

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
		sig, err := client.Sign(msg, txOutItem)
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
	client, vaultPubKeys := s.newClient(common.BTCChain, c)

	txOutItem := types.TxOutItem{
		Chain:            common.BTCChain,
		VaultPubKey:      vaultPubKeys.Secp256k1,
		VaultPubKeyEddsa: vaultPubKeys.Ed25519,
	}

	msg := []byte("66d2d6b5eb564972c59e4797683a1225")
	sig, err := client.Sign(msg, txOutItem)
	c.Assert(err, IsNil)
	c.Assert(len(sig), Equals, 65)

	c.Assert(verifySecp256k1(nil, sig, vaultPubKeys.Secp256k1), NotNil)
	c.Assert(verifySecp256k1(msg, nil, vaultPubKeys.Secp256k1), NotNil)
	c.Assert(verifySecp256k1(msg, sig, common.EmptyPubKey), NotNil)

	c.Assert(verifyEd25519(nil, sig, vaultPubKeys.Ed25519), NotNil)
	c.Assert(verifyEd25519(msg, nil, vaultPubKeys.Ed25519), NotNil)
	c.Assert(verifyEd25519(msg, sig, common.EmptyPubKey), NotNil)
}
