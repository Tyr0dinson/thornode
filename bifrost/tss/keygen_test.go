package tss

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestTSSKeyGen(t *testing.T) { TestingT(t) }

type KeyGenTestSuite struct{}

var _ = Suite(&KeyGenTestSuite{})

type dklsKeygenBridge struct {
	thorclient.ThorchainBridge
	asgards ttypes.Vaults
}

func (b *dklsKeygenBridge) GetAsgards() (ttypes.Vaults, error) {
	return b.asgards, nil
}

func (*KeyGenTestSuite) SetUpSuite(c *C) {
	thorchain.SetupConfigForTest()
}

const (
	signerNameForTest     = `jack`
	signerPasswordForTest = `password`
)

func (*KeyGenTestSuite) setupKeysForTest(c *C) string {
	ns := strconv.Itoa(time.Now().Nanosecond())
	thorcliDir := filepath.Join(os.TempDir(), ns, ".thorcli")
	c.Logf("thorcliDir:%s", thorcliDir)
	buf := bytes.NewBufferString(signerPasswordForTest)
	// the library used by keyring is using ReadLine , which expect a new line
	buf.WriteByte('\n')
	buf.WriteString(signerPasswordForTest)
	buf.WriteByte('\n')
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb, err := cKeys.New(cosmos.KeyringServiceName(), cKeys.BackendFile, thorcliDir, buf, cdc)
	c.Assert(err, IsNil)
	info, _, err := kb.NewMnemonic(signerNameForTest, cKeys.English, cmd.THORChainHDPath, signerPasswordForTest, hd.Secp256k1)
	c.Assert(err, IsNil)
	c.Logf("name:%s", info.Name)
	return thorcliDir
}

func (kts *KeyGenTestSuite) TestNewTssKenGen(c *C) {
	oldStdIn := os.Stdin
	defer func() {
		os.Stdin = oldStdIn
	}()
	os.Stdin = nil
	folder := kts.setupKeysForTest(c)
	defer func() {
		err := os.RemoveAll(folder)
		c.Assert(err, IsNil)
	}()
	kb, _, err := thorclient.GetKeyringKeybase(folder, signerNameForTest, signerPasswordForTest)
	c.Assert(err, IsNil)
	k := thorclient.NewKeysWithKeybase(kb, signerNameForTest, signerPasswordForTest)
	c.Assert(k, NotNil)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		c.Logf("requestUri:%s", req.RequestURI)
	}))
	b, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID:      "thorchain",
		ChainHost:    server.Listener.Addr().String(),
		SignerName:   "bob",
		SignerPasswd: "password",
	}, nil, k)
	c.Assert(err, IsNil)
	kg, err := NewTssKeyGen(k, nil, b)
	c.Assert(err, IsNil)
	c.Assert(kg, NotNil)
}

func (kts *KeyGenTestSuite) TestDKLSIncomingKeygenBlameNodesBlamesOnlyStandby(c *C) {
	active := map[string]struct{}{
		"active-1": {},
		"active-2": {},
		"active-3": {},
	}
	got := dklsIncomingKeygenBlameNodes([]string{"active-1", "active-2", "standby-1", "active-3"}, active)
	c.Assert(got, HasLen, 1)
	c.Assert(got[0].Pubkey, Equals, "standby-1")
}

func (kts *KeyGenTestSuite) TestDKLSIncomingKeygenBlameNodesNeedsActiveMembership(c *C) {
	got := dklsIncomingKeygenBlameNodes([]string{"active-1", "standby-1"}, nil)
	c.Assert(got, HasLen, 0)
}

func (kts *KeyGenTestSuite) TestActiveAsgardMembersIgnoresRetiringVaults(c *C) {
	vaults := ttypes.Vaults{
		{
			Type:       ttypes.VaultType_AsgardVault,
			Status:     ttypes.VaultStatus_RetiringVault,
			Membership: []string{"retiring-1"},
		},
		{
			Type:       ttypes.VaultType_AsgardVault,
			Status:     ttypes.VaultStatus_ActiveVault,
			Membership: []string{"active-1", "active-2"},
		},
	}
	active := activeAsgardMembers(vaults)
	_, hasRetiring := active["retiring-1"]
	_, hasActive := active["active-1"]
	c.Assert(hasRetiring, Equals, false)
	c.Assert(hasActive, Equals, true)
}

func (kts *KeyGenTestSuite) TestApplyDKLSKeygenTimeoutFallbackBlame(c *C) {
	kg := &KeyGen{
		bridge: &dklsKeygenBridge{
			asgards: ttypes.Vaults{{
				Type:       ttypes.VaultType_AsgardVault,
				Status:     ttypes.VaultStatus_ActiveVault,
				Membership: []string{"active-1", "active-2"},
			}},
		},
		logger: log.With().Str("module", "tss_keygen_test").Logger(),
	}
	b := ttypes.Blame{FailReason: "DKLS session timeout: produced=0 missing="}
	kg.applyDKLSKeygenTimeoutFallbackBlame(&b, storage.SigningEngineDKLS, common.PubKeys{"active-1", "standby-1", "active-2"}, errors.New(b.FailReason))
	c.Assert(b.BlameNodes, HasLen, 1)
	c.Assert(b.BlameNodes[0].Pubkey, Equals, "standby-1")
	c.Assert(b.Round, Equals, "DKLSKeygenAbort")
}
