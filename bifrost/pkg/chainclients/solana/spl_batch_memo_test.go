package solana

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/mr-tron/base58"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/common"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// p-token replaced the SPL Token program at the same address on mainnet (feature
// ptokFjwyJtrwCa9Kgo9xoDS59V4QccBGEaRFnRPnSdP). It adds Batch (discriminator 255): one
// instruction whose data packs several token instructions, each as
// [number of accounts u8][data length u8][data], taking its accounts in order from the
// instruction's account list. A transfer inside a Batch is not a separate instruction or
// CPI, so a deposit sent that way was never observed: the tokens sat in the vault's
// token account, neither credited nor refunded.

const testJupiterPg = "JUP6LkbZbjS1jKKwapdHNy74zcZ3tLUZoi5QNyVTaV4"

// batchEntry is one instruction packed into a Batch.
type batchEntry struct {
	accounts []int
	data     []byte
}

// batchInstructionData encodes entries as Batch instruction data, and the instruction's
// accounts as the entries' accounts in order.
func batchInstruction(entries ...batchEntry) (accounts []int, data string) {
	raw := []byte{255}
	for _, e := range entries {
		raw = append(raw, byte(len(e.accounts)), byte(len(e.data)))
		raw = append(raw, e.data...)
		accounts = append(accounts, e.accounts...)
	}
	return accounts, base58.Encode(raw)
}

func transferCheckedBytes(amount uint64, decimals uint8) []byte {
	d := make([]byte, 10)
	d[0] = types.TokenInstructionTransferChecked
	binary.LittleEndian.PutUint64(d[1:9], amount)
	d[9] = decimals
	return d
}

func transferBytes(amount uint64) []byte {
	d := make([]byte, 9)
	d[0] = types.TokenInstructionTransfer
	binary.LittleEndian.PutUint64(d[1:9], amount)
	return d
}

// A deposit whose TransferChecked is packed in a top-level Batch is credited exactly as
// the same TransferChecked on its own.
func (s *SPLScannerSuite) TestBatchTransferCheckedDepositObserved(c *C) {
	const amount = 12_345_678
	txn := splInbound(c, amount)
	accounts, data := batchInstruction(batchEntry{accounts: []int{1, 3, 2, 0}, data: transferCheckedBytes(amount, 6)})
	txn.Transaction.Message.Instructions[0] = rpc.RPCInstruction{ProgramIdIndex: 4, Accounts: accounts, Data: data}

	item := newSPLTestScanner().getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.Sender, Equals, testSender)
	c.Check(item.To, Equals, testVault)
	c.Check(item.Memo, Equals, "=:BTC.BTC:bc1qxyz")
	c.Check(item.Coins[0].Asset.String(), Equals, testUSDCAssetStr)
	c.Check(item.Coins[0].Amount.Uint64(), Equals, uint64(amount)*100)
}

// The same Batch invoked by another program (an aggregator's CPI) is observed too.
func (s *SPLScannerSuite) TestBatchTransferDepositViaCPIObserved(c *C) {
	const amount = 12_345_678
	txn := splInbound(c, amount)
	txn.Transaction.Message.AccountKeys = append(txn.Transaction.Message.AccountKeys, testJupiterPg) // index 6
	txn.Transaction.Message.Instructions[0] = rpc.RPCInstruction{ProgramIdIndex: 6, Accounts: []int{0, 1, 2, 3, 4}, Data: base58.Encode([]byte{1})}
	accounts, data := batchInstruction(batchEntry{accounts: []int{1, 2, 0}, data: transferBytes(amount)})
	txn.Meta.InnerInstructions = []interface{}{
		map[string]interface{}{
			"index": 0,
			"instructions": []interface{}{
				map[string]interface{}{"programIdIndex": 4, "accounts": accounts, "data": data, "stackHeight": 2},
			},
		},
	}

	item := newSPLTestScanner().getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.Sender, Equals, testSender)
	c.Check(item.To, Equals, testVault)
	c.Check(item.Coins[0].Amount.Uint64(), Equals, uint64(amount)*100)
}

// A Batch that pays a fee to another token account and then deposits into the vault:
// as for separate instructions, the one transfer into the vault is observed.
func (s *SPLScannerSuite) TestBatchWithFeeAndDepositObservesTheVaultTransfer(c *C) {
	const amount, fee = 12_345_678, 1_000
	txn := splInbound(c, amount)
	feeOwner, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	feeATA := ata(c, feeOwner.String(), testUSDCMint)
	txn.Transaction.Message.AccountKeys = append(txn.Transaction.Message.AccountKeys, feeATA) // index 6
	txn.Meta.PreTokenBalances = append(txn.Meta.PreTokenBalances, tokenBalance(6, testUSDCMint, feeOwner.String(), "0", 6))
	txn.Meta.PostTokenBalances = append(txn.Meta.PostTokenBalances, tokenBalance(6, testUSDCMint, feeOwner.String(), fmt.Sprint(fee), 6))
	txn.Meta.PostTokenBalances[0] = tokenBalance(1, testUSDCMint, testSender, fmt.Sprint(50_000_000-amount-fee), 6)
	accounts, data := batchInstruction(
		batchEntry{accounts: []int{1, 6, 0}, data: transferBytes(fee)},
		batchEntry{accounts: []int{1, 3, 2, 0}, data: transferCheckedBytes(amount, 6)},
	)
	txn.Transaction.Message.Instructions[0] = rpc.RPCInstruction{ProgramIdIndex: 4, Accounts: accounts, Data: data}

	scanner := newSPLTestScanner()
	scanner.pubKeyMgr = splVaultAddrs{addrs: map[string]bool{testVault: true}}
	item := scanner.getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.To, Equals, testVault)
	c.Check(item.Coins[0].Amount.Uint64(), Equals, uint64(amount)*100)
}

// A Batch of non-transfer instructions beside a plain deposit does not hide it.
func (s *SPLScannerSuite) TestBatchWithoutTransfersBesideDeposit(c *C) {
	const amount = 12_345_678
	txn := splInbound(c, amount)
	// SyncNative (17) on the sender's token account: no transfer.
	accounts, data := batchInstruction(batchEntry{accounts: []int{1}, data: []byte{17}})
	txn.Transaction.Message.Instructions = append(txn.Transaction.Message.Instructions,
		rpc.RPCInstruction{ProgramIdIndex: 4, Accounts: accounts, Data: data})

	item := newSPLTestScanner().getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.Coins[0].Amount.Uint64(), Equals, uint64(amount)*100)
}

// A malformed Batch (an entry longer than the data, or too few accounts) is a
// malformed transfer candidate: never credited.
func (s *SPLScannerSuite) TestMalformedBatchNotCredited(c *C) {
	const amount = 12_345_678
	for name, mutate := range map[string]func(*rpc.RPCInstruction){
		"truncated data": func(ins *rpc.RPCInstruction) {
			raw, _ := base58.Decode(ins.Data)
			ins.Data = base58.Encode(raw[:len(raw)-3])
		},
		"too few accounts": func(ins *rpc.RPCInstruction) { ins.Accounts = ins.Accounts[:2] },
	} {
		txn := splInbound(c, amount)
		accounts, data := batchInstruction(batchEntry{accounts: []int{1, 3, 2, 0}, data: transferCheckedBytes(amount, 6)})
		ins := rpc.RPCInstruction{ProgramIdIndex: 4, Accounts: accounts, Data: data}
		mutate(&ins)
		txn.Transaction.Message.Instructions[0] = ins
		c.Check(newSPLTestScanner().getTxInItem(txn, 100), IsNil, Commentf(name))
	}
}

// Memo v1 (Memo1UhkJRfHyvLMcVucJwxXeuD728EqVDDwQDxFMNo) is still a valid memo program;
// a deposit memo written with it must be read like a v2 memo.
const testMemoV1Program = "Memo1UhkJRfHyvLMcVucJwxXeuD728EqVDDwQDxFMNo"

func (s *SPLScannerSuite) TestMemoV1ReadOnTokenDeposits(c *C) {
	txn := splInbound(c, 12_345_678)
	txn.Transaction.Message.AccountKeys[5] = testMemoV1Program
	item := newSPLTestScanner().getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.Memo, Equals, "=:BTC.BTC:bc1qxyz")

	// Written through a CPI.
	cpi := splInbound(c, 12_345_678)
	cpi.Transaction.Message.AccountKeys[5] = testMemoV1Program
	cpi.Transaction.Message.Instructions = cpi.Transaction.Message.Instructions[:1]
	cpi.Transaction.Message.AccountKeys = append(cpi.Transaction.Message.AccountKeys, testJupiterPg) // index 6
	cpi.Transaction.Message.Instructions = append(cpi.Transaction.Message.Instructions,
		rpc.RPCInstruction{ProgramIdIndex: 6, Accounts: []int{0}, Data: base58.Encode([]byte{1})})
	cpi.Meta.InnerInstructions = []interface{}{
		map[string]interface{}{
			"index": 1,
			"instructions": []interface{}{
				map[string]interface{}{"programIdIndex": 5, "accounts": []int{0}, "data": makeMemoData("=:ETH.ETH:0xabc"), "stackHeight": 2},
			},
		},
	}
	item = newSPLTestScanner().getTxInItem(cpi, 100)
	c.Assert(item, NotNil)
	c.Check(item.Memo, Equals, "=:ETH.ETH:0xabc")
}

func (s *GetTxInItemSuite) TestMemoV1ReadOnSOLDeposits(c *C) {
	scanner := newTestScanner()
	oneSol, fee := uint64(1_000_000_000), uint64(5000)
	txn := &rpc.TransactionResult{
		Slot: 100,
		Meta: rpc.RPCMeta{
			Fee:          fee,
			PreBalances:  []uint64{10_000_000_000, 50_000_000_000, 1, 1},
			PostBalances: []uint64{10_000_000_000 - oneSol - fee, 50_000_000_000 + oneSol, 1, 1},
		},
		Transaction: rpc.RPCTxnData{
			Signatures: []string{testTxSig},
			Message: rpc.RPCMessage{
				AccountKeys: []string{testSender, testVault, testSystemProgram, testMemoV1Program},
				Instructions: []rpc.RPCInstruction{
					{ProgramIdIndex: 2, Accounts: []int{0, 1}, Data: makeTransferData(oneSol)},
					{ProgramIdIndex: 3, Accounts: []int{0}, Data: makeMemoData("=:ETH.ETH:0x1234567890abcdef")},
				},
			},
		},
	}
	item := scanner.getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.Memo, Equals, "=:ETH.ETH:0x1234567890abcdef")
}

// Anyone can pad a vault token account's history with failed transactions for the fee
// alone, and every node would fetch each one. A failed transaction moves no tokens, and
// a vault's own failed durable transaction lists the vault wallet (its fee payer), whose
// scan reads it. So a token account's scan skips failed signatures without fetching
// them, and its cursor still moves past them.
func (s *BlockScannerTestSuite) TestTokenAccountScanSkipsFailedSignatures(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	tokenAccount := ata(c, vaultAddr.String(), testUSDCMint)

	const failedSig, depositSig = "FailedSpam1111111111111111111111111111111111111111111111111111111111", "Deposit11111111111111111111111111111111111111111111111111111111111111"
	instructionErr := map[string]interface{}{"InstructionError": []interface{}{0, "InvalidAccountData"}}
	failed := splInboundTo(c, vaultAddr.String(), 1_000_000)
	failed.Transaction.Signatures = []string{failedSig}
	failed.Meta.Err = instructionErr
	deposit := splInboundTo(c, vaultAddr.String(), 2_000_000)
	deposit.Transaction.Signatures = []string{depositSig}

	m := &splScanRPC{
		txs:      map[string]*rpc.TransactionResult{failedSig: failed, depositSig: deposit},
		getTxHit: map[string]int{},
	}
	m.set(1000, map[string][]rpc.SignaturesForAddressResult{
		tokenAccount: {{Signature: failedSig, Slot: 950, Err: instructionErr}, {Signature: depositSig, Slot: 900}},
	})
	server := m.server(c)
	defer server.Close()

	scanner, storage, txIns := s.newTokenScanTestScanner(c, server, vault)
	scanner.scan()
	observed := 0
	for _, txIn := range drainTxIns(txIns) {
		for _, item := range txIn.TxArray {
			c.Check(item.Tx, Equals, depositSig)
			observed++
		}
	}
	c.Check(observed, Equals, 1)
	c.Check(m.getTxHit[depositSig], Equals, 1)
	c.Check(m.getTxHit[failedSig], Equals, 0, Commentf("a failed signature in a token account's history must not be fetched"))
	c.Check(scanner.IsHealthy(), Equals, true)
	time.Sleep(50 * time.Millisecond) // scan status writes are async
	sig, _, err := storage.GetScanStatus(tokenAccount)
	c.Assert(err, IsNil)
	c.Check(sig, Equals, failedSig)
}
