package solana

import (
	"fmt"

	"github.com/mr-tron/base58"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
)

// Deposits as wallets send them in practice: with a Jito tip or a fee transfer beside
// the deposit, paid for by a sponsor that takes its fee in tokens, from a smart wallet
// (the transfer and memo run as CPIs), or from a token account an aggregator created in
// the same transaction. Bifrost credits exactly what lands in a vault's canonical token
// account and ignores the rest of the transaction.

const (
	testJitoTip       = "96gYZGLnJYVFmbjzopPSU6QiEV5fGqZNyN9nmNhvrZU5" // a Jito tip account
	testFeeWallet     = "Ce2gMcvBiJcJXrAq8Hvy8dqEKVAcRMq3LsWGSfBNudyQ"
	testSmartWalletPg = "SQDS4ep65T869zMMBKyuUq6aD6EgTu8psMjkvj52pCf" // a smart-wallet program
)

// newRelaxedTestScanner is newSPLTestScanner knowing testVault as a vault.
func newRelaxedTestScanner() *SOLScanner {
	s := newSPLTestScanner()
	s.pubKeyMgr = splVaultAddrs{addrs: map[string]bool{testVault: true}}
	return s
}

// withSOLTransfers appends top-level SystemProgram transfers from the fee payer to each
// destination, as a Jito tip or a frontend fee is added to a deposit.
func withSOLTransfers(txn *rpc.TransactionResult, lamports uint64, dests ...string) {
	msg := &txn.Transaction.Message
	msg.AccountKeys = append(msg.AccountKeys, testSystemProgram)
	system := len(msg.AccountKeys) - 1
	for _, dest := range dests {
		msg.AccountKeys = append(msg.AccountKeys, dest)
		msg.Instructions = append(msg.Instructions,
			rpc.RPCInstruction{ProgramIdIndex: system, Accounts: []int{0, len(msg.AccountKeys) - 1}, Data: makeTransferData(lamports)})
	}
}

func (s *SPLScannerSuite) TestDepositWithTipObserved(c *C) {
	for name, dests := range map[string][]string{
		"Jito tip":           {testJitoTip},
		"Jito tip and a fee": {testJitoTip, testFeeWallet},
	} {
		txn := splInbound(c, 12_345_678)
		withSOLTransfers(txn, 10_000, dests...)
		item := newRelaxedTestScanner().getTxInItem(txn, 100)
		c.Assert(item, NotNil, Commentf(name))
		c.Check(item.Sender, Equals, testSender, Commentf(name))
		c.Check(item.To, Equals, testVault, Commentf(name))
		c.Check(item.Memo, Equals, "=:BTC.BTC:bc1qxyz", Commentf(name))
		c.Assert(item.Coins, HasLen, 1, Commentf(name))
		c.Check(item.Coins[0].Asset.String(), Equals, testUSDCAssetStr, Commentf(name))
		c.Check(item.Coins[0].Amount.String(), Equals, "1234567800", Commentf(name))
	}
}

// A SOL transfer to or from a vault keeps the transaction on the SOL path, exactly as
// before SPL support, whatever else it carries.
func (s *SPLScannerSuite) TestSOLTransferTouchingVaultKeepsSOLPath(c *C) {
	for name, tips := range map[string][]string{"no tip": nil, "with a tip": {testJitoTip}} {
		txn := splInbound(c, 12_345_678)
		withSOLTransfers(txn, 10_000, tips...)
		withSOLTransfers(txn, 1_000_000_000, testVault)
		item := newRelaxedTestScanner().getTxInItem(txn, 100)
		c.Assert(item, NotNil, Commentf(name))
		c.Check(item.Coins[0].Asset, Equals, common.SOLAsset, Commentf(name))
		c.Check(item.Coins[0].Amount.Equal(convertLamportsToTHORChain(1_000_000_000)), Equals, true, Commentf(name))
	}
}

// splSponsoredDeposit is a deposit whose fee is paid by a sponsor that takes its fee
// in tokens:
//
//	0 sponsor (fee payer), 1 sender token account, 2 vault token account, 3 USDC mint,
//	4 token program, 5 memo program, 6 sender (authority), 7 sponsor token account.
func splSponsoredDeposit(c *C, amount, sponsorFee uint64) *rpc.TransactionResult {
	sponsorATA := ata(c, testNewAccount, testUSDCMint)
	return &rpc.TransactionResult{
		Slot: 100,
		Meta: rpc.RPCMeta{
			Fee:          5000,
			PreBalances:  []uint64{1_000_000_000, 2_039_280, 2_039_280, 1, 1, 1, 0, 2_039_280},
			PostBalances: []uint64{1_000_000_000 - 5000, 2_039_280, 2_039_280, 1, 1, 1, 0, 2_039_280},
			PreTokenBalances: []interface{}{
				tokenBalance(1, testUSDCMint, testSender, "50000000", 6),
				tokenBalance(2, testUSDCMint, testVault, "7000000", 6),
				tokenBalance(7, testUSDCMint, testNewAccount, "0", 6),
			},
			PostTokenBalances: []interface{}{
				tokenBalance(1, testUSDCMint, testSender, fmt.Sprint(50_000_000-amount-sponsorFee), 6),
				tokenBalance(2, testUSDCMint, testVault, fmt.Sprint(7_000_000+amount), 6),
				tokenBalance(7, testUSDCMint, testNewAccount, fmt.Sprint(sponsorFee), 6),
			},
		},
		Transaction: rpc.RPCTxnData{
			Signatures: []string{testTxSig},
			Message: rpc.RPCMessage{
				AccountKeys: []string{testNewAccount, testSenderTokenAcc, ata(c, testVault, testUSDCMint), testUSDCMint, testTokenProgram, testMemoProgram, testSender, sponsorATA},
				Instructions: []rpc.RPCInstruction{
					{ProgramIdIndex: 4, Accounts: []int{1, 3, 7, 6}, Data: makeTransferCheckedData(sponsorFee, 6)},
					{ProgramIdIndex: 4, Accounts: []int{1, 3, 2, 6}, Data: makeTransferCheckedData(amount, 6)},
					{ProgramIdIndex: 5, Accounts: []int{6}, Data: makeMemoData("=:BTC.BTC:bc1qxyz")},
				},
			},
		},
	}
}

func (s *SPLScannerSuite) TestSponsoredDepositWithTokenFeeObserved(c *C) {
	item := newRelaxedTestScanner().getTxInItem(splSponsoredDeposit(c, 12_345_678, 100_000), 100)
	c.Assert(item, NotNil)
	c.Check(item.Sender, Equals, testSender) // the token owner, not the sponsor
	c.Check(item.To, Equals, testVault)
	c.Check(item.Coins[0].Amount.String(), Equals, "1234567800")
	c.Check(item.Memo, Equals, "=:BTC.BTC:bc1qxyz")
	c.Check(item.Gas[0].Amount.String(), Equals, convertLamportsToTHORChain(5000).String())
}

// Two transfers into vault token accounts are never observed: which one the memo
// belongs to, and what to credit, would be guesswork.
func (s *SPLScannerSuite) TestTwoTransfersIntoVaultRejected(c *C) {
	txn := splSponsoredDeposit(c, 12_345_678, 100_000)
	// The "fee" goes to the vault's token account too.
	txn.Transaction.Message.Instructions[0].Accounts = []int{1, 3, 2, 6}
	txn.Meta.PostTokenBalances[1] = tokenBalance(2, testUSDCMint, testVault, fmt.Sprint(7_000_000+12_345_678+100_000), 6)
	txn.Meta.PostTokenBalances[2] = tokenBalance(7, testUSDCMint, testNewAccount, "0", 6)
	c.Check(newRelaxedTestScanner().getTxInItem(txn, 100), IsNil)
}

// splSmartWalletDeposit is a deposit from a smart wallet: its program moves the tokens
// and writes the memo through CPIs, so neither is a top-level instruction.
//
//	0 fee payer, 1 smart wallet (token owner), 2 smart wallet token account,
//	3 vault token account, 4 USDC mint, 5 token program, 6 memo program,
//	7 smart-wallet program.
func splSmartWalletDeposit(c *C, amount uint64) *rpc.TransactionResult {
	return &rpc.TransactionResult{
		Slot: 100,
		Meta: rpc.RPCMeta{
			Fee:          5000,
			PreBalances:  []uint64{1_000_000_000, 0, 2_039_280, 2_039_280, 1, 1, 1, 1},
			PostBalances: []uint64{1_000_000_000 - 5000, 0, 2_039_280, 2_039_280, 1, 1, 1, 1},
			PreTokenBalances: []interface{}{
				tokenBalance(2, testUSDCMint, testSender, "50000000", 6),
				tokenBalance(3, testUSDCMint, testVault, "7000000", 6),
			},
			PostTokenBalances: []interface{}{
				tokenBalance(2, testUSDCMint, testSender, fmt.Sprint(50_000_000-amount), 6),
				tokenBalance(3, testUSDCMint, testVault, fmt.Sprint(7_000_000+amount), 6),
			},
			InnerInstructions: []interface{}{
				map[string]interface{}{
					"index": 0,
					"instructions": []interface{}{
						map[string]interface{}{"programIdIndex": 5, "accounts": []int{2, 4, 3, 1}, "data": makeTransferCheckedData(amount, 6), "stackHeight": 2},
						map[string]interface{}{"programIdIndex": 6, "accounts": []int{1}, "data": makeMemoData("=:ETH.ETH:0xabc"), "stackHeight": 2},
					},
				},
			},
		},
		Transaction: rpc.RPCTxnData{
			Signatures: []string{testTxSig},
			Message: rpc.RPCMessage{
				AccountKeys: []string{testNewAccount, testSender, testSenderTokenAcc, ata(c, testVault, testUSDCMint), testUSDCMint, testTokenProgram, testMemoProgram, testSmartWalletPg},
				Instructions: []rpc.RPCInstruction{
					{ProgramIdIndex: 7, Accounts: []int{0, 1, 2, 3, 4, 5, 6}, Data: base58.Encode([]byte("execute"))},
				},
			},
		},
	}
}

func (s *SPLScannerSuite) TestSmartWalletDepositObserved(c *C) {
	item := newRelaxedTestScanner().getTxInItem(splSmartWalletDeposit(c, 3_000_000), 100)
	c.Assert(item, NotNil)
	c.Check(item.Sender, Equals, testSender) // the smart wallet that owns the tokens
	c.Check(item.To, Equals, testVault)
	c.Check(item.Coins[0].Amount.String(), Equals, "300000000")
	c.Check(item.Memo, Equals, "=:ETH.ETH:0xabc")

	// A top-level memo takes precedence over one written through a CPI.
	txn := splSmartWalletDeposit(c, 3_000_000)
	txn.Transaction.Message.Instructions = append(txn.Transaction.Message.Instructions,
		rpc.RPCInstruction{ProgramIdIndex: 6, Accounts: []int{0}, Data: makeMemoData("=:BTC.BTC:bc1qxyz")})
	item = newRelaxedTestScanner().getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.Memo, Equals, "=:BTC.BTC:bc1qxyz")
}

// A CPI credit beside a top-level transfer into the same vault token account: two
// transfers into the vault, rejected.
func (s *SPLScannerSuite) TestCPICreditBesideTopLevelTransferRejected(c *C) {
	txn := splInbound(c, 12_345_678)
	txn.Meta.InnerInstructions = []interface{}{
		map[string]interface{}{
			"index":        1,
			"instructions": []interface{}{map[string]interface{}{"programIdIndex": 4, "accounts": []int{1, 3, 2, 0}, "data": makeTransferCheckedData(1, 6)}},
		},
	}
	txn.Meta.PostTokenBalances[1] = tokenBalance(2, testUSDCMint, testVault, fmt.Sprint(7_000_000+12_345_678+1), 6)
	c.Check(newRelaxedTestScanner().getTxInItem(txn, 100), IsNil)
}

// A source token account created in the same transaction (an aggregator's output, or
// a wallet's first token account) has no pre-transaction owner. Its owner is trusted
// only when that owner signed the transfer: an owner set after the transfer (the
// forged-vault-sender attack) or a transfer by a delegate is not observed.
func (s *SPLScannerSuite) TestSourceCreatedInTransaction(c *C) {
	created := func() *rpc.TransactionResult {
		txn := splInbound(c, 12_345_678)
		txn.Meta.PreTokenBalances = txn.Meta.PreTokenBalances[1:]
		return txn
	}
	item := newRelaxedTestScanner().getTxInItem(created(), 100)
	c.Assert(item, NotNil)
	c.Check(item.Sender, Equals, testSender)
	c.Check(item.Coins[0].Amount.String(), Equals, "1234567800")

	handedToVault := created()
	tokenBalanceEntry(handedToVault.Meta.PostTokenBalances[0])["owner"] = testVault
	c.Check(newRelaxedTestScanner().getTxInItem(handedToVault, 100), IsNil)

	byDelegate := created()
	byDelegate.Transaction.Message.AccountKeys = append(byDelegate.Transaction.Message.AccountKeys, testNewAccount)
	byDelegate.Transaction.Message.Instructions[0].Accounts = []int{1, 3, 2, 6}
	c.Check(newRelaxedTestScanner().getTxInItem(byDelegate, 100), IsNil)

	// Created and closed again in the transaction: no owner to trust at all.
	closed := created()
	closed.Meta.PostTokenBalances = closed.Meta.PostTokenBalances[1:]
	c.Check(newRelaxedTestScanner().getTxInItem(closed, 100), IsNil)
}

// A Jito bundle is consecutive ordinary transactions in one slot; nothing on chain
// marks it. Each is observed on its own: the deposit, whose source an earlier bundle
// transaction funded, is credited in full, and a separate tip transaction is not a
// deposit.
func (s *SPLScannerSuite) TestBundledDepositObservedOnItsOwn(c *C) {
	scanner := newRelaxedTestScanner()
	tip := splInbound(c, 1)
	tip.Meta.PreTokenBalances, tip.Meta.PostTokenBalances = nil, nil
	tip.Transaction.Message.Instructions = nil
	withSOLTransfers(tip, 10_000, testJitoTip)

	var credited []string
	for _, txn := range []*rpc.TransactionResult{splInbound(c, 12_345_678), tip} {
		if item := scanner.getTxInItem(txn, 100); item != nil && item.To == testVault {
			credited = append(credited, item.Coins[0].Asset.String()+" "+item.Coins[0].Amount.String())
		}
	}
	c.Check(credited, DeepEquals, []string{testUSDCAssetStr + " 1234567800"})
}
