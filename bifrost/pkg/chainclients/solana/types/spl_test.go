package types

import (
	"encoding/binary"

	. "gopkg.in/check.v1"
)

// SPLTestSuite covers the SPL token primitives: associated token account derivation,
// the TransferChecked / CreateIdempotent encoders, and the outbound builders. Every
// keysign party must derive the same addresses and serialize identical bytes, and
// the derived accounts must be the ones the runtime actually uses.
type SPLTestSuite struct{}

var _ = Suite(&SPLTestSuite{})

const (
	testUSDCMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	testUSDTMint = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"
)

// Vectors are (associated token account, mint, owner) triples read from finalized
// mainnet transactions' postTokenBalances. They cover both on-curve wallet owners
// and off-curve (program derived) owners.
var ataVectors = [][3]string{
	{"2Y7HATmn9aJBcxCskE5V2U2epmjvkZmB51zTJBbhj4cU", testUSDCMint, "8FnX3xo2yYw3EUE6w3nQA4GfXGS9wpK6oj3veJpbFzLo"},
	{"CMiMBq7GH4khCojF8ysfYtwcmhuJvC6TQzxnaDcPsy3e", testUSDCMint, "b619EzHShebQRX7oouwUD5fUo72ai8GgcMLAHkcqfG2"},
	{"EaouumxDiXRFn2iUjkrvNj2ivra4CqcDqWV5gCQ915m9", testUSDCMint, "shakRDxyKwaBbduqZTZt8Q4etTALbDLnAANStv9TLH3"},
	{"EFfc3HXmgnKmvoLGKcNsGKs7zt3FdX4B6oZBLpCnbMcs", testUSDTMint, "shakRDxyKwaBbduqZTZt8Q4etTALbDLnAANStv9TLH3"},
	{"HSyC5Uyip3XTzWL5fis3Lj7XZC8z33PPxCE866nYkNLK", testUSDTMint, "b619EzHShebQRX7oouwUD5fUo72ai8GgcMLAHkcqfG2"},
	{"qZGfSMBqqCK7jFUdBDCVk7uHHgP67WDRD6kTt84gGK2", testUSDTMint, "EHcege7dok1iYs7SxL2XzDPvhg6XzMVcx2V5SkMUurJP"},
	{"8Uz5Bz4wYkvAz4mMpg9a8K4kqGtS5JLMwdrE2z24eoXc", "So11111111111111111111111111111111111111112", "shakRDxyKwaBbduqZTZt8Q4etTALbDLnAANStv9TLH3"},
	{"FhqsoCRe4xWUxfMig2yQ9rd61foqeEopsxNbi3EJHWep", "98sMhvDwXj1RQi5c5Mndm3vPe9cBqPrbLaufMXFNMh5g", "EHcege7dok1iYs7SxL2XzDPvhg6XzMVcx2V5SkMUurJP"},
}

func (s *SPLTestSuite) TestFindAssociatedTokenAddressMainnetVectors(c *C) {
	for _, v := range ataVectors {
		got, err := FindAssociatedTokenAddress(MustPublicKeyFromString(v[2]), MustPublicKeyFromString(v[1]))
		c.Assert(err, IsNil)
		c.Check(got.String(), Equals, v[0], Commentf("owner %s mint %s", v[2], v[1]))
		// An associated token account is program derived, so it is off the curve.
		c.Check(isOnCurve(got.Bytes()), Equals, false)
	}
	// The vectors include both kinds of owner.
	c.Check(isOnCurve(MustPublicKeyFromString("shakRDxyKwaBbduqZTZt8Q4etTALbDLnAANStv9TLH3").Bytes()), Equals, true)
	c.Check(isOnCurve(MustPublicKeyFromString("8FnX3xo2yYw3EUE6w3nQA4GfXGS9wpK6oj3veJpbFzLo").Bytes()), Equals, false)
}

func (s *SPLTestSuite) TestCreateProgramAddressRejectsBadSeeds(c *C) {
	long := make([]byte, MaxSeedLength+1)
	_, err := CreateProgramAddress([][]byte{long}, AssociatedTokenProgramID)
	c.Check(err, NotNil)

	many := make([][]byte, MaxSeed+1)
	_, err = CreateProgramAddress(many, AssociatedTokenProgramID)
	c.Check(err, NotNil)
}

func (s *SPLTestSuite) TestPublicKeyFromStringStrict(c *C) {
	pk, err := PublicKeyFromStringStrict(testUSDCMint)
	c.Assert(err, IsNil)
	c.Check(pk.String(), Equals, testUSDCMint)

	// Short input decodes, but is not a 32-byte key.
	_, err = PublicKeyFromStringStrict("EPjFWdd5AufqSSqeM2qN1xzybapC8G4w")
	c.Check(err, NotNil)
	_, err = PublicKeyFromStringStrict("not-base58!")
	c.Check(err, NotNil)
}

func (s *SPLTestSuite) TestTransferCheckedEncoding(c *C) {
	ins := NewCompiledTransferCheckedInstruction(6, 1, 4, 2, 0, 1_234_567, 6)
	c.Check(ins.ProgramIDIndex, Equals, 6)
	c.Check(ins.Accounts, DeepEquals, []int{1, 4, 2, 0})
	c.Assert(ins.Data, HasLen, 10)
	c.Check(ins.Data[0], Equals, byte(TokenInstructionTransferChecked))
	c.Check(binary.LittleEndian.Uint64(ins.Data[1:9]), Equals, uint64(1_234_567))
	c.Check(ins.Data[9], Equals, byte(6))
}

func (s *SPLTestSuite) TestCreateIdempotentEncoding(c *C) {
	ins := NewCompiledCreateAssociatedTokenAccountIdempotentInstruction(7, 0, 2, 3, 4, 5, 6)
	c.Check(ins.ProgramIDIndex, Equals, 7)
	c.Check(ins.Accounts, DeepEquals, []int{0, 2, 3, 4, 5, 6})
	c.Check(ins.Data, DeepEquals, []byte{AssociatedTokenInstructionCreateIdempotent})
}

func splTestParams(c *C, memo string) SPLTransferParams {
	vault := MustPublicKeyFromString("BEcrPLugbAY1zEJGUNYgzcsxMi72rPeVwG6qKm96LK5g")
	recipient := MustPublicKeyFromString("shakRDxyKwaBbduqZTZt8Q4etTALbDLnAANStv9TLH3")
	mint := MustPublicKeyFromString(testUSDCMint)
	source, err := FindAssociatedTokenAddress(vault, mint)
	c.Assert(err, IsNil)
	dest, err := FindAssociatedTokenAddress(recipient, mint)
	c.Assert(err, IsNil)
	return SPLTransferParams{
		Vault: vault, Recipient: recipient, Mint: mint,
		SourceATA: source, DestATA: dest,
		Amount: 2_500_000, Decimals: 6, Memo: memo,
	}
}

func (s *SPLTestSuite) TestNewSPLTransferTransactionLayout(c *C) {
	p := splTestParams(c, "OUT:ABCDEF")
	tx := NewSPLTransferTransaction(p, "EkSnNWid2cvwEVnVx9aBqawnmiCNiDgp3gUdkDPTKN1N")
	m := tx.Message

	c.Check(m.Header, DeepEquals, MessageHeader{NumRequireSignatures: 1, NumReadonlySignedAccounts: 0, NumReadonlyUnsignedAccounts: 6})
	c.Check(m.Accounts, DeepEquals, []PublicKey{
		p.Vault, p.SourceATA, p.DestATA,
		p.Recipient, p.Mint, SystemProgramID, TokenProgramID, AssociatedTokenProgramID, MemoProgramID,
	})
	c.Assert(m.Instructions, HasLen, 3)
	c.Check(m.Instructions[0], DeepEquals, NewCompiledInstruction(8, []int{0}, []byte("OUT:ABCDEF")))
	c.Check(m.Instructions[1], DeepEquals, NewCompiledCreateAssociatedTokenAccountIdempotentInstruction(7, 0, 2, 3, 4, 5, 6))
	c.Check(m.Instructions[2], DeepEquals, NewCompiledTransferCheckedInstruction(6, 1, 4, 2, 0, 2_500_000, 6))
	c.Check(tx.Signatures, HasLen, 1)

	_, err := tx.Serialize()
	c.Assert(err, IsNil)
}

func (s *SPLTestSuite) TestNewSPLTransferTransactionNoMemo(c *C) {
	p := splTestParams(c, "")
	tx := NewSPLTransferTransaction(p, "EkSnNWid2cvwEVnVx9aBqawnmiCNiDgp3gUdkDPTKN1N")
	c.Check(tx.Message.Accounts, HasLen, 8)
	c.Check(tx.Message.Header.NumReadonlyUnsignedAccounts, Equals, uint8(5))
	c.Assert(tx.Message.Instructions, HasLen, 2)
	c.Check(tx.Message.Instructions[0].Data, DeepEquals, []byte{AssociatedTokenInstructionCreateIdempotent})
}

func (s *SPLTestSuite) TestNewSPLTransferTransactionDedupsRecipient(c *C) {
	// A recipient equal to the vault must not list the vault twice
	// (AccountLoadedTwice); it stays the single writable signer, and the source and
	// destination token account coincide.
	p := splTestParams(c, "OUT:ABCDEF")
	p.Recipient = p.Vault
	p.DestATA = p.SourceATA
	tx := NewSPLTransferTransaction(p, "EkSnNWid2cvwEVnVx9aBqawnmiCNiDgp3gUdkDPTKN1N")

	seen := map[PublicKey]bool{}
	for _, k := range tx.Message.Accounts {
		c.Check(seen[k], Equals, false, Commentf("duplicate account %s", k))
		seen[k] = true
	}
	c.Check(tx.Message.Accounts[0], Equals, p.Vault)
	c.Check(tx.Message.Header.NumRequireSignatures, Equals, uint8(1))
	transfer := tx.Message.Instructions[len(tx.Message.Instructions)-1]
	c.Check(transfer.Accounts[0], Equals, transfer.Accounts[2])
	c.Check(transfer.Accounts[3], Equals, 0)
}

func (s *SPLTestSuite) TestNewNonceSPLTransferTransactionLayout(c *C) {
	p := splTestParams(c, "OUT:ABCDEF")
	nonceAccount, err := CreateWithSeed(p.Vault, "thornode-durable-nonce-v1", SystemProgramID)
	c.Assert(err, IsNil)
	nonceValue := "EkSnNWid2cvwEVnVx9aBqawnmiCNiDgp3gUdkDPTKN1N"
	tx := NewNonceSPLTransferTransaction(p, NonceParams{Value: nonceValue, Account: nonceAccount, Authority: p.Vault})
	m := tx.Message

	c.Check(m.RecentBlockHash, Equals, nonceValue)
	c.Check(m.Accounts, DeepEquals, []PublicKey{
		p.Vault, nonceAccount, p.SourceATA, p.DestATA,
		SysVarRecentBlockhashesID, p.Recipient, p.Mint, SystemProgramID, TokenProgramID, AssociatedTokenProgramID, MemoProgramID,
	})
	c.Check(m.Header, DeepEquals, MessageHeader{NumRequireSignatures: 1, NumReadonlySignedAccounts: 0, NumReadonlyUnsignedAccounts: 7})
	c.Assert(m.Instructions, HasLen, 4)
	// AdvanceNonceAccount must be instruction[0].
	c.Check(m.Instructions[0], DeepEquals, NewCompiledAdvanceNonceInstruction(7, 1, 4, 0))
	c.Check(m.Instructions[1], DeepEquals, NewCompiledInstruction(10, []int{0}, []byte("OUT:ABCDEF")))
	c.Check(m.Instructions[2], DeepEquals, NewCompiledCreateAssociatedTokenAccountIdempotentInstruction(9, 0, 3, 5, 6, 7, 8))
	c.Check(m.Instructions[3], DeepEquals, NewCompiledTransferCheckedInstruction(8, 2, 6, 3, 0, 2_500_000, 6))
}

func (s *SPLTestSuite) TestSPLTransferDeterministic(c *C) {
	p := splTestParams(c, "OUT:ABCDEF")
	txA := NewSPLTransferTransaction(p, "EkSnNWid2cvwEVnVx9aBqawnmiCNiDgp3gUdkDPTKN1N")
	txB := NewSPLTransferTransaction(p, "EkSnNWid2cvwEVnVx9aBqawnmiCNiDgp3gUdkDPTKN1N")
	a, err := txA.Message.Serialize()
	c.Assert(err, IsNil)
	b, err := txB.Message.Serialize()
	c.Assert(err, IsNil)
	c.Check(a, DeepEquals, b)
}
