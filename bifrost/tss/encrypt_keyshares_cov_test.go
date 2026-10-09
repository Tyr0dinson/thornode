package tss

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"

	"filippo.io/age"
	"golang.org/x/crypto/chacha20poly1305"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
)

type EncryptKeysharesCovSuite struct{}

var _ = Suite(&EncryptKeysharesCovSuite{})

func (s *EncryptKeysharesCovSuite) TestEncryptKeysharesAtRestInput(c *C) {
	dir := c.MkDir()
	raw, err := os.ReadFile(LocalStateTestFile)
	c.Assert(err, IsNil)

	// an at-rest encrypted keyshare file is transparently decrypted first
	atRest, err := storage.EncryptAtRest(storage.DeriveAtRestKey(Mnemonic), raw)
	c.Assert(err, IsNil)
	path := filepath.Join(dir, "atrest.json")
	c.Assert(os.WriteFile(path, atRest, 0o600), IsNil)
	encrypted, err := EncryptKeyshares(path, Mnemonic)
	c.Assert(err, IsNil)
	decrypted, err := DecryptRawKeyshares(encrypted, Mnemonic)
	c.Assert(err, IsNil)
	c.Check(decrypted, DeepEquals, raw)

	// at-rest data sealed with another key cannot be decrypted
	otherKey, err := storage.EncryptAtRest(storage.DeriveAtRestKey("some other seed"), raw)
	c.Assert(err, IsNil)
	c.Assert(os.WriteFile(path, otherKey, 0o600), IsNil)
	_, err = EncryptKeyshares(path, Mnemonic)
	c.Assert(err, ErrorMatches, "failed keyshare encrypt - cannot decrypt at-rest keyshares.*")

	// non-JSON input is rejected
	c.Assert(os.WriteFile(path, []byte("not json"), 0o600), IsNil)
	_, err = EncryptKeyshares(path, Mnemonic)
	c.Assert(err, ErrorMatches, "failed keyshare encrypt - cannot decode keyshares.*")
}

func (s *EncryptKeysharesCovSuite) TestEncryptRawKeysharesValidation(c *C) {
	_, err := EncryptRawKeyshares([]byte("x"), "")
	c.Assert(err, ErrorMatches, "failed keyshare encrypt: signer seed phrase is not set")
	_, err = EncryptRawKeyshares(nil, Mnemonic)
	c.Assert(err, ErrorMatches, "failed keyshare encrypt: raw bytes are empty")
}

func (s *EncryptKeysharesCovSuite) TestDecryptKeysharesLayers(c *C) {
	_, err := DecryptKeyshares([]byte("x"), "")
	c.Assert(err, ErrorMatches, "failed keyshare decrypt: passphrase is empty")

	_, err = DecryptKeyshares([]byte("short"), Mnemonic)
	c.Assert(err, ErrorMatches, "failed keyshare decrypt - cannot chacha decrypt keyshares: ciphertext too short")

	chaCha, err := chacha20poly1305.NewX(saltAndHash(Mnemonic, SaltChaCha))
	c.Assert(err, IsNil)

	// valid chacha layer around a broken aes layer
	badAES, err := encryptAEAD(chaCha, []byte("not an aes payload, long enough to have a nonce"))
	c.Assert(err, IsNil)
	_, err = DecryptKeyshares(badAES, Mnemonic)
	c.Assert(err, ErrorMatches, "failed keyshare decrypt - cannot aes decrypt keyshares.*")

	// valid chacha + aes layers around a broken age payload
	aesLayer, err := encryptAES(saltAndHash(Mnemonic, SaltAES), []byte("not an age file"))
	c.Assert(err, IsNil)
	badAge, err := encryptAEAD(chaCha, aesLayer)
	c.Assert(err, IsNil)
	_, err = DecryptKeyshares(badAge, Mnemonic)
	c.Assert(err, ErrorMatches, "failed keyshare decrypt - cannot decrypt.*")

	_, err = DecryptRawKeyshares([]byte("short"), Mnemonic)
	c.Assert(err, NotNil)

	// a fully valid triple-encrypted payload that is not lzma compressed
	// decrypts but fails raw decompression. The payload is shorter than the
	// 13-byte lzma header, so the decoder fails before reading a dictionary
	// size (a longer one can parse as a header asking for a multi-GiB window).
	recipient, err := age.NewScryptRecipient(hex.EncodeToString(saltAndHash(Mnemonic, SaltAge)))
	c.Assert(err, IsNil)
	recipient.SetWorkFactor(ageWorkFactor)
	ageBuf := new(bytes.Buffer)
	w, err := age.Encrypt(ageBuf, recipient)
	c.Assert(err, IsNil)
	_, err = w.Write([]byte("not lzma"))
	c.Assert(err, IsNil)
	c.Assert(w.Close(), IsNil)
	aesLayer, err = encryptAES(saltAndHash(Mnemonic, SaltAES), ageBuf.Bytes())
	c.Assert(err, IsNil)
	plainAge, err := encryptAEAD(chaCha, aesLayer)
	c.Assert(err, IsNil)
	compressed, err := DecryptKeyshares(plainAge, Mnemonic)
	c.Assert(err, IsNil)
	c.Check(string(compressed), Equals, "not lzma")
	_, err = DecryptRawKeyshares(plainAge, Mnemonic)
	c.Assert(err, ErrorMatches, "failed to decompress raw keyshares.*")
	err = writeRecoveredDecompressedKeyshares(filepath.Join(c.MkDir(), "ks.json"), plainAge, Mnemonic)
	c.Assert(err, ErrorMatches, "failed to decompress key shares.*")
}

func (s *EncryptKeysharesCovSuite) TestAESKeyLength(c *C) {
	_, err := encryptAES([]byte("short"), []byte("data"))
	c.Assert(err, ErrorMatches, "failed to encrypt: key must be 32 bytes")
	_, err = decryptAES([]byte("short"), []byte("data"))
	c.Assert(err, ErrorMatches, "failed to decrypt: key must be 32 bytes")

	key := saltAndHash("k", 1)
	enc, err := encryptAES(key, []byte("hello"))
	c.Assert(err, IsNil)
	dec, err := decryptAES(key, enc)
	c.Assert(err, IsNil)
	c.Check(string(dec), Equals, "hello")
	// wrong key fails authentication
	_, err = decryptAES(saltAndHash("k", 2), enc)
	c.Assert(err, NotNil)
}
