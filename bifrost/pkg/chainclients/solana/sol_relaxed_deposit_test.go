package solana

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
)

// SOL deposits as wallets send them: with a Jito tip or a frontend fee beside the
// deposit. Bifrost observes the one SystemProgram transfer that touches a vault and
// ignores transfers that touch no vault.

// testSecondVault is another vault, for transfers that touch two vaults.
const testSecondVault = "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU"

type solTransferSpec struct {
	to       string
	lamports uint64
}

// solDeposit builds a transaction from testSender (the fee payer) with a memo and the
// given top-level SystemProgram transfers, in order.
func solDeposit(transfers ...solTransferSpec) *rpc.TransactionResult {
	keys := []string{testSender, testSystemProgram, testMemoProgram}
	instructions := []rpc.RPCInstruction{
		{ProgramIdIndex: 2, Accounts: []int{0}, Data: makeMemoData("=:BTC.BTC:bc1qxyz")},
	}
	pre := []uint64{100_000_000_000, 1, 1}
	for _, t := range transfers {
		keys = append(keys, t.to)
		pre = append(pre, 0)
		instructions = append(instructions, rpc.RPCInstruction{
			ProgramIdIndex: 1, Accounts: []int{0, len(keys) - 1}, Data: makeTransferData(t.lamports),
		})
	}
	return &rpc.TransactionResult{
		Slot: 100,
		Meta: rpc.RPCMeta{Fee: 5000, PreBalances: pre, PostBalances: pre},
		Transaction: rpc.RPCTxnData{
			Signatures: []string{testTxSig},
			Message:    rpc.RPCMessage{AccountKeys: keys, Instructions: instructions},
		},
	}
}

// solRelaxedScanners are scanners that know testVault and testSecondVault as vaults,
// with and without SPL tokens whitelisted: the SOL outcome must not depend on it.
func solRelaxedScanners() map[string]*SOLScanner {
	vaults := splVaultAddrs{addrs: map[string]bool{testVault: true, testSecondVault: true}}
	sol := newTestScanner()
	sol.pubKeyMgr = vaults
	spl := newSPLTestScanner()
	spl.pubKeyMgr = vaults
	return map[string]*SOLScanner{"SOL only": sol, "SPL whitelisted": spl}
}

func (s *GetTxInItemSuite) TestSOLDepositWithTipObserved(c *C) {
	deposit := solTransferSpec{to: testVault, lamports: 1_000_000_000}
	tip := solTransferSpec{to: testJitoTip, lamports: 10_000}
	fee := solTransferSpec{to: testFeeWallet, lamports: 50_000}
	shapes := map[string][]solTransferSpec{
		"tip after":         {deposit, tip},
		"tip before":        {tip, deposit},
		"tip and a fee":     {tip, deposit, fee},
		"fee, then the tip": {fee, tip, deposit},
	}
	for scannerName, scanner := range solRelaxedScanners() {
		for shape, transfers := range shapes {
			name := scannerName + ": " + shape
			item := scanner.getTxInItem(solDeposit(transfers...), 100)
			c.Assert(item, NotNil, Commentf(name))
			c.Check(item.Sender, Equals, testSender, Commentf(name))
			c.Check(item.To, Equals, testVault, Commentf(name))
			c.Check(item.Memo, Equals, "=:BTC.BTC:bc1qxyz", Commentf(name))
			c.Assert(item.Coins, HasLen, 1, Commentf(name))
			c.Check(item.Coins[0].Asset, Equals, common.SOLAsset, Commentf(name))
			c.Check(item.Coins[0].Amount.Equal(convertLamportsToTHORChain(1_000_000_000)), Equals, true, Commentf(name))
			// Gas is the transaction fee; the tip and fee went elsewhere.
			c.Check(item.Gas[0].Amount.Equal(convertLamportsToTHORChain(5000)), Equals, true, Commentf(name))
		}
	}
}

// More than one transfer touching vaults is never guessed between, and transfers that
// touch no vault are not a deposit.
func (s *GetTxInItemSuite) TestSOLTransfersNotExactlyOneVaultRejected(c *C) {
	deposit := solTransferSpec{to: testVault, lamports: 1_000_000_000}
	tip := solTransferSpec{to: testJitoTip, lamports: 10_000}
	shapes := map[string][]solTransferSpec{
		"two transfers to the vault": {deposit, tip, {to: testVault, lamports: 2_000_000_000}},
		"transfers to two vaults":    {deposit, {to: testSecondVault, lamports: 2_000_000_000}},
		"no vault":                   {tip, {to: testFeeWallet, lamports: 2_000_000_000}},
	}
	for scannerName, scanner := range solRelaxedScanners() {
		for shape, transfers := range shapes {
			c.Check(scanner.getTxInItem(solDeposit(transfers...), 100), IsNil, Commentf(scannerName+": "+shape))
		}
	}
}
