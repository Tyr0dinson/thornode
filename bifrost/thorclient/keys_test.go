package thorclient

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/cmd"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain"
)

type KeysSuite struct{}

var _ = Suite(&KeysSuite{})

func (*KeysSuite) SetUpSuite(c *C) {
	thorchain.SetupConfigForTest()
}

const (
	signerNameForTest     = `jack`
	signerPasswordForTest = `password`
)

func (*KeysSuite) setupKeysForTest(c *C) string {
	ns := strconv.Itoa(time.Now().Nanosecond())
	thorcliDir := filepath.Join(os.TempDir(), ns, ".thorcli")
	c.Logf("thorcliDir:%s", thorcliDir)
	buf := bytes.NewBufferString(signerPasswordForTest)
	// the library used by keyring is using ReadLine , which expect a new line
	buf.WriteByte('\n')
	buf.WriteString(signerPasswordForTest)
	buf.WriteByte('\n')
	kb, err := cKeys.New(cosmos.KeyringServiceName(), cKeys.BackendFile, thorcliDir, buf, codec.NewAminoCodec(codec.NewLegacyAmino()))
	c.Assert(err, IsNil)
	_, _, err = kb.NewMnemonic(signerNameForTest, cKeys.English, cmd.THORChainHDPath, signerPasswordForTest, hd.Secp256k1)
	c.Assert(err, IsNil)
	return thorcliDir
}

func (ks *KeysSuite) TestNewKeys(c *C) {
	oldStdIn := os.Stdin
	defer func() {
		os.Stdin = oldStdIn
	}()
	os.Stdin = nil
	folder := ks.setupKeysForTest(c)
	defer func() {
		err := os.RemoveAll(folder)
		c.Assert(err, IsNil)
	}()

	k, record, err := GetKeyringKeybase(folder, signerNameForTest, signerPasswordForTest)
	c.Assert(err, IsNil)
	c.Assert(k, NotNil)
	c.Assert(record, NotNil)
	ki := NewKeysWithKeybase(k, signerNameForTest, signerPasswordForTest)
	record = ki.GetSignerInfo()
	c.Assert(record, NotNil)
	c.Assert(record.Name, Equals, signerNameForTest)
	priKey, err := ki.GetPrivateKey()
	c.Assert(err, IsNil)
	c.Assert(priKey, NotNil)
	c.Assert(priKey.Bytes(), HasLen, 32)
	kb := ki.GetKeybase()
	c.Assert(kb, NotNil)
}
