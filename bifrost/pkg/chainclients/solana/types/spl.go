package types

import (
	"encoding/binary"
)

// SPL Token instruction discriminators (first data byte).
const (
	TokenInstructionTransfer        = 3
	TokenInstructionTransferChecked = 12
)

// AssociatedTokenInstructionCreateIdempotent creates the associated token account if
// it does not exist and succeeds without change if it does.
const AssociatedTokenInstructionCreateIdempotent = 1

// NewCompiledTransferCheckedInstruction builds the SPL Token TransferChecked
// instruction. Account order is fixed by the Token program:
//
//	0: source token account      (writable)
//	1: mint                      (readonly)
//	2: destination token account (writable)
//	3: owner / authority         (signer)
//
// The decimals argument makes the program reject the transfer if the mint's
// decimals differ, so a misconfigured whitelist entry fails closed on chain rather
// than moving a different amount than THORChain scheduled.
func NewCompiledTransferCheckedInstruction(programIndex, sourceIndex, mintIndex, destIndex, authorityIndex int, amount uint64, decimals uint8) CompiledInstruction {
	data := make([]byte, 0, 10)
	data = append(data, TokenInstructionTransferChecked)
	data = binary.LittleEndian.AppendUint64(data, amount)
	data = append(data, decimals)
	return NewCompiledInstruction(programIndex, []int{sourceIndex, mintIndex, destIndex, authorityIndex}, data)
}

// NewCompiledCreateAssociatedTokenAccountIdempotentInstruction builds the
// associated token program CreateIdempotent instruction. Account order is fixed by
// the program:
//
//	0: funding account           (writable signer; pays rent when created)
//	1: associated token account  (writable)
//	2: wallet (owner)            (readonly)
//	3: mint                      (readonly)
//	4: System program            (readonly)
//	5: SPL Token program         (readonly)
func NewCompiledCreateAssociatedTokenAccountIdempotentInstruction(programIndex, payerIndex, ataIndex, walletIndex, mintIndex, systemIndex, tokenProgramIndex int) CompiledInstruction {
	return NewCompiledInstruction(
		programIndex,
		[]int{payerIndex, ataIndex, walletIndex, mintIndex, systemIndex, tokenProgramIndex},
		[]byte{AssociatedTokenInstructionCreateIdempotent},
	)
}

// accountListBuilder collects the accounts a legacy message references and orders
// them the way the runtime requires: writable signers, readonly signers, writable
// non-signers, readonly non-signers. An account added more than once is listed once
// with the union of its flags, so a recipient that coincides with another account
// (the vault itself, its nonce account, the mint) never produces AccountLoadedTwice.
// Within each class accounts keep insertion order, so the result is a pure function
// of the calls and every keysign party compiles identical bytes.
type accountListBuilder struct {
	keys  []PublicKey
	flags map[PublicKey]*accountFlags
}

type accountFlags struct {
	signer   bool
	writable bool
}

func newAccountListBuilder() *accountListBuilder {
	return &accountListBuilder{flags: map[PublicKey]*accountFlags{}}
}

func (b *accountListBuilder) add(key PublicKey, signer, writable bool) {
	if f, ok := b.flags[key]; ok {
		f.signer = f.signer || signer
		f.writable = f.writable || writable
		return
	}
	b.keys = append(b.keys, key)
	b.flags[key] = &accountFlags{signer: signer, writable: writable}
}

// compile returns the ordered account list, the index of each account and the
// message header.
func (b *accountListBuilder) compile() ([]PublicKey, map[PublicKey]int, MessageHeader) {
	var ws, rs, wn, rn []PublicKey
	for _, k := range b.keys {
		f := b.flags[k]
		switch {
		case f.signer && f.writable:
			ws = append(ws, k)
		case f.signer:
			rs = append(rs, k)
		case f.writable:
			wn = append(wn, k)
		default:
			rn = append(rn, k)
		}
	}
	ordered := make([]PublicKey, 0, len(b.keys))
	ordered = append(ordered, ws...)
	ordered = append(ordered, rs...)
	ordered = append(ordered, wn...)
	ordered = append(ordered, rn...)

	index := make(map[PublicKey]int, len(ordered))
	for i, k := range ordered {
		index[k] = i
	}
	header := MessageHeader{
		NumRequireSignatures:        uint8(len(ws) + len(rs)),
		NumReadonlySignedAccounts:   uint8(len(rs)),
		NumReadonlyUnsignedAccounts: uint8(len(rn)),
	}
	return ordered, index, header
}

// SPLTransferParams describes a vault SPL token outbound.
type SPLTransferParams struct {
	Vault     PublicKey // fee payer, ATA rent payer and transfer authority
	Recipient PublicKey // destination wallet (owner of DestATA)
	Mint      PublicKey
	SourceATA PublicKey // the vault's associated token account for Mint
	DestATA   PublicKey // the recipient's associated token account for Mint
	Amount    uint64    // in the mint's native units
	Decimals  uint8
	Memo      string
}

// NonceParams anchors a transaction on a durable nonce instead of a recent blockhash.
type NonceParams struct {
	Value     string // the stored nonce, baked into the message's recentBlockhash
	Account   PublicKey
	Authority PublicKey // must be the vault: it is the only signer
}

// NewSPLTransferTransaction builds a vault SPL token outbound anchored on a recent
// blockhash: an optional memo, CreateIdempotent for the recipient's associated token
// account (the vault pays rent only if it is missing), then TransferChecked.
func NewSPLTransferTransaction(p SPLTransferParams, recentBlockHash string) Transaction {
	return newSPLTransferTransaction(p, nil, recentBlockHash)
}

// NewNonceSPLTransferTransaction builds the same outbound as NewSPLTransferTransaction
// anchored on a durable nonce: AdvanceNonceAccount is instruction[0], as Solana
// requires, and the message's recentBlockhash field carries the stored nonce.
func NewNonceSPLTransferTransaction(p SPLTransferParams, nonce NonceParams) Transaction {
	return newSPLTransferTransaction(p, &nonce, nonce.Value)
}

func newSPLTransferTransaction(p SPLTransferParams, nonce *NonceParams, blockhash string) Transaction {
	b := newAccountListBuilder()
	b.add(p.Vault, true, true)
	if nonce != nil {
		// The vault is the nonce authority; a distinct authority would need a second
		// signer, which the vault signing model does not support.
		b.add(nonce.Account, false, true)
		b.add(SysVarRecentBlockhashesID, false, false)
	}
	b.add(p.SourceATA, false, true)
	b.add(p.DestATA, false, true)
	b.add(p.Recipient, false, false)
	b.add(p.Mint, false, false)
	b.add(SystemProgramID, false, false)
	b.add(TokenProgramID, false, false)
	b.add(AssociatedTokenProgramID, false, false)
	if p.Memo != "" {
		b.add(MemoProgramID, false, false)
	}
	accounts, idx, header := b.compile()

	instructions := make([]CompiledInstruction, 0, 4)
	if nonce != nil {
		instructions = append(instructions, NewCompiledAdvanceNonceInstruction(
			idx[SystemProgramID], idx[nonce.Account], idx[SysVarRecentBlockhashesID], idx[p.Vault],
		))
	}
	if p.Memo != "" {
		// Memo first, matching the SOL transfer builders.
		instructions = append(instructions, NewCompiledInstruction(idx[MemoProgramID], []int{idx[p.Vault]}, []byte(p.Memo)))
	}
	instructions = append(instructions,
		NewCompiledCreateAssociatedTokenAccountIdempotentInstruction(
			idx[AssociatedTokenProgramID], idx[p.Vault], idx[p.DestATA], idx[p.Recipient],
			idx[p.Mint], idx[SystemProgramID], idx[TokenProgramID],
		),
		NewCompiledTransferCheckedInstruction(
			idx[TokenProgramID], idx[p.SourceATA], idx[p.Mint], idx[p.DestATA], idx[p.Vault],
			p.Amount, p.Decimals,
		),
	)

	return NewTransaction(NewMessage(header, accounts, blockhash, instructions))
}
