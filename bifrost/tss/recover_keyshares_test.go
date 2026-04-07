package tss

import (
	"os"
	"path/filepath"

	. "gopkg.in/check.v1"
)

type RecoverKeysharesSuite struct{}

var _ = Suite(&RecoverKeysharesSuite{})

func (s *RecoverKeysharesSuite) TestRecoverScanLowerBound(c *C) {
	c.Assert(recoverScanLowerBound(0, 100), Equals, int64(0))
	c.Assert(recoverScanLowerBound(50, 100), Equals, int64(0))
	c.Assert(recoverScanLowerBound(500, 100), Equals, int64(400))
}

func (s *RecoverKeysharesSuite) TestRecoverySeedPhraseRequiresEnv(c *C) {
	originalSeedPhrase, hadOriginalSeedPhrase := os.LookupEnv("SIGNER_SEED_PHRASE")
	defer func() {
		if hadOriginalSeedPhrase {
			_ = os.Setenv("SIGNER_SEED_PHRASE", originalSeedPhrase)
		} else {
			_ = os.Unsetenv("SIGNER_SEED_PHRASE")
		}
	}()

	_ = os.Unsetenv("SIGNER_SEED_PHRASE")
	seedPhrase, err := recoverySeedPhrase()
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "SIGNER_SEED_PHRASE environment variable is not set")
	c.Assert(seedPhrase, Equals, "")

	err = os.Setenv("SIGNER_SEED_PHRASE", "  "+Mnemonic+"  ")
	c.Assert(err, IsNil)
	seedPhrase, err = recoverySeedPhrase()
	c.Assert(err, IsNil)
	c.Assert(seedPhrase, Equals, Mnemonic)
}

func (s *RecoverKeysharesSuite) TestWriteRecoveredDecompressedKeysharesWritesOwnerOnlyFile(c *C) {
	encrypted, err := EncryptKeyshares(LocalStateTestFile, Mnemonic)
	c.Assert(err, IsNil)

	dir, err := os.MkdirTemp("", "recover-keyshares-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "localstate-recovered.json")
	err = writeRecoveredDecompressedKeyshares(path, encrypted, Mnemonic)
	c.Assert(err, IsNil)

	fi, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(fi.Mode().Perm(), Equals, os.FileMode(0o600))

	original, err := os.ReadFile(LocalStateTestFile)
	c.Assert(err, IsNil)
	recovered, err := os.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(recovered, DeepEquals, original)
}

func (s *RecoverKeysharesSuite) TestWriteRecoveredDecompressedKeysharesDoesNotLeaveOrphanOnDecryptFailure(c *C) {
	dir, err := os.MkdirTemp("", "recover-keyshares-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "localstate-recovered.json")
	err = writeRecoveredDecompressedKeyshares(path, []byte("broken"), Mnemonic)
	c.Assert(err, NotNil)

	_, err = os.Stat(path)
	c.Assert(os.IsNotExist(err), Equals, true)

	entries, err := os.ReadDir(dir)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 0)
}

func (s *RecoverKeysharesSuite) TestWriteRecoveredRawKeysharesWritesOwnerOnlyFile(c *C) {
	rawKeyshares := []byte("deadbeefcafebabe")
	encrypted, err := EncryptRawKeyshares(rawKeyshares, Mnemonic)
	c.Assert(err, IsNil)

	dir, err := os.MkdirTemp("", "recover-keyshares-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "frost-recovery.hex")
	err = writeRecoveredRawKeyshares(path, encrypted, Mnemonic)
	c.Assert(err, IsNil)

	fi, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(fi.Mode().Perm(), Equals, os.FileMode(0o600))

	recovered, err := os.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(recovered, DeepEquals, rawKeyshares)
}
