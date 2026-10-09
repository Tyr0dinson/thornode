package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/keys"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	bech32 "github.com/cosmos/cosmos-sdk/types/bech32/legacybech32" // nolint SA1019 deprecated
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

type CmdCovSuite struct{}

var _ = Suite(&CmdCovSuite{})

const (
	cmdCovSigner   = "cmdcov"
	cmdCovPassword = "password1234"
	cmdCovMnemonic = `grape safe sound obtain bachelor festival profit iron meat moon exit garbage chapter promote noble grocery blood letter junk click mesh arm shop decorate`
)

func (s *CmdCovSuite) SetUpTest(c *C) {
	thorchain.SetupConfigForTest()
}

func cmdCovCodec() codec.Codec {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	return codec.NewProtoCodec(registry)
}

// runPubKeyCmdCov executes the pubkey command in process and returns stdout.
func runPubKeyCmdCov(c *C, stdin string, args ...string) (string, error) {
	cmd := GetPubKeyCmd()
	ctx := client.Context{}.WithCodec(cmdCovCodec())
	cmd.SetContext(context.Background())
	c.Assert(client.SetCmdClientContext(cmd, ctx), IsNil)
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(cmd.Context())
	return out.String(), err
}

func (s *CmdCovSuite) TestPubKeyCmd(c *C) {
	pk := secp256k1.GenPrivKeyFromSecret([]byte("cmd-cov")).PubKey()
	jsonPk, err := cmdCovCodec().MarshalInterfaceJSON(pk)
	c.Assert(err, IsNil)

	accWant, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pk)
	c.Assert(err, IsNil)
	consWant, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeConsPub, pk)
	c.Assert(err, IsNil)

	// from args, default acc prefix
	out, err := runPubKeyCmdCov(c, "", string(jsonPk))
	c.Assert(err, IsNil)
	c.Assert(strings.TrimSpace(out), Equals, accWant)

	// from stdin
	out, err = runPubKeyCmdCov(c, string(jsonPk)+"\n")
	c.Assert(err, IsNil)
	c.Assert(strings.TrimSpace(out), Equals, accWant)

	// consensus prefix
	out, err = runPubKeyCmdCov(c, "", string(jsonPk), "--"+keys.FlagBechPrefix, sdk.PrefixConsensus)
	c.Assert(err, IsNil)
	c.Assert(strings.TrimSpace(out), Equals, consWant)

	// validator prefix prints nothing
	out, err = runPubKeyCmdCov(c, "", string(jsonPk), "--"+keys.FlagBechPrefix, sdk.PrefixValidator)
	c.Assert(err, IsNil)
	c.Assert(out, Equals, "")

	// invalid json
	_, err = runPubKeyCmdCov(c, "", "not json")
	c.Assert(err, NotNil)

	// no stdin
	_, err = runPubKeyCmdCov(c, "")
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "fail to get Proto3 JSON encoded pubkey"), Equals, true)

	// too many args
	_, err = runPubKeyCmdCov(c, "", "a", "b")
	c.Assert(err, NotNil)
}

func runValidateSeedCov(stdin io.Reader) (string, error) {
	cmd := GetValidateSeedPhraseCmd()
	errOut := &bytes.Buffer{}
	cmd.SetErr(errOut)
	cmd.SetOut(io.Discard)
	cmd.SetIn(stdin)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	return errOut.String(), err
}

type errReaderCov struct{}

func (errReaderCov) Read([]byte) (int, error) { return 0, errors.New("boom") }

func (s *CmdCovSuite) TestValidateSeedPhraseCmd(c *C) {
	// valid 24 word mnemonic: no warnings
	stderr, err := runValidateSeedCov(strings.NewReader("  " + cmdCovMnemonic + "\n"))
	c.Assert(err, IsNil)
	c.Assert(stderr, Equals, "")

	// valid 12 word mnemonic warns about DR backup
	twelve := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	stderr, err = runValidateSeedCov(strings.NewReader(twelve))
	c.Assert(err, IsNil)
	c.Assert(strings.Contains(stderr, "signer seed is 12 words"), Equals, true)

	// 24 wordlist words with an invalid checksum are grandfathered with a warning
	legacy := strings.TrimSpace(strings.Repeat("abandon ", 24))
	stderr, err = runValidateSeedCov(strings.NewReader(legacy))
	c.Assert(err, IsNil)
	c.Assert(strings.Contains(stderr, "INVALID BIP39 checksum"), Equals, true)

	// garbage
	_, err = runValidateSeedCov(strings.NewReader("not a mnemonic"))
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "invalid seed phrase"), Equals, true)

	// empty
	_, err = runValidateSeedCov(strings.NewReader(""))
	c.Assert(err, NotNil)

	// read error
	_, err = runValidateSeedCov(errReaderCov{})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "fail to read mnemonic"), Equals, true)
}

// captureStdoutCov captures os.Stdout while f runs.
func captureStdoutCov(c *C, f func() error) (string, error) {
	r, w, err := os.Pipe()
	c.Assert(err, IsNil)
	old := os.Stdout
	os.Stdout = w
	runErr := f()
	os.Stdout = old
	c.Assert(w.Close(), IsNil)
	buf, err := io.ReadAll(r)
	c.Assert(err, IsNil)
	return string(buf), runErr
}

func cmdCovSetEnv(key, value string) func() {
	old, ok := os.LookupEnv(key)
	_ = os.Setenv(key, value)
	return func() {
		if ok {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}

func runEd25519Cov(stdin string) error {
	cmd := GetEd25519Keys()
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	return cmd.Execute()
}

func (s *CmdCovSuite) TestEd25519KeysCmd(c *C) {
	home := filepath.Join(c.MkDir(), ".thornode")
	// pre-create the file keyring so later opens only read the passphrase once
	buf := bytes.NewBufferString(cmdCovPassword + "\n" + cmdCovPassword + "\n")
	kb, err := ckeys.New(cosmos.KeyringServiceName(), ckeys.BackendFile, home, buf, cmdCovCodec())
	c.Assert(err, IsNil)
	_, _, err = kb.NewMnemonic(cmdCovSigner, ckeys.English, "m/44'/931'/0'/0/0", "", hd.Secp256k1)
	c.Assert(err, IsNil)

	oldHome := app.DefaultNodeHome
	app.DefaultNodeHome = home
	defer func() { app.DefaultNodeHome = oldHome }()
	defer cmdCovSetEnv(cosmos.EnvSignerName, cmdCovSigner)()
	defer cmdCovSetEnv(cosmos.EnvSignerPassword, cmdCovPassword)()

	// expected bech32 of the ed25519 key derived from the mnemonic; note the
	// command passes the signer password as the BIP39 passphrase
	priv, err := ed25519.DeriveKeypairFromMnemonic(cmdCovMnemonic, cmdCovPassword, ed25519.HDPath)
	c.Assert(err, IsNil)
	tmp, err := cryptocodec.FromCmtPubKeyInterface(cmted25519.PubKey(priv[32:]))
	c.Assert(err, IsNil)
	// nolint
	want, err := bech32.MarshalPubKey(bech32.AccPK, tmp)
	c.Assert(err, IsNil)

	// an invalid mnemonic fails to create the key
	_, err = captureStdoutCov(c, func() error { return runEd25519Cov("not a valid mnemonic\n") })
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "fail to create new key"), Equals, true)

	// missing mnemonic input
	_, err = captureStdoutCov(c, func() error { return runEd25519Cov("") })
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "fail to get mnemonic"), Equals, true)

	// key does not exist yet: it is created from the mnemonic read from stdin
	out, err := captureStdoutCov(c, func() error { return runEd25519Cov(cmdCovMnemonic + "\n") })
	c.Assert(err, IsNil)
	c.Assert(strings.TrimSpace(out), Equals, want)

	// key now exists: no mnemonic needed, same pubkey printed
	out, err = captureStdoutCov(c, func() error { return runEd25519Cov("") })
	c.Assert(err, IsNil)
	c.Assert(strings.TrimSpace(out), Equals, want)

	// args are rejected
	cmd := GetEd25519Keys()
	cmd.SetArgs([]string{"extra"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	c.Assert(cmd.Execute(), NotNil)
}
