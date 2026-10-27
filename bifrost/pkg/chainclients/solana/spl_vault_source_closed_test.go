package solana

import (
	"github.com/mr-tron/base58"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
)

// zly (!5273) asked whether a source token account with a pre-transaction state but no
// post-transaction state (closed in the transaction) should be refused when its owner
// was a vault. It must not be: SPL Token moves tokens out of an initialized account only
// with its owner's signature (or a delegate the owner approved, which vaults never do),
// changing the owner takes the owner's signature and clears any delegate, and an
// account can be closed only once empty, after which a transfer out of it fails the
// whole transaction. So a successful transfer out of an account a vault owned before
// the transaction was signed by that vault. Refusing it would hide a real vault spend
// (Bifrost's builder never closes the vault's token account, so only an unexpected
// signer could), leaving the tokens on THORChain's books. It is observed as the vault's
// outbound.
func (s *SPLScannerSuite) TestVaultSourceClosedInSameTxObservedAsVaultOutbound(c *C) {
	scanner := newSPLTestScanner()
	scanner.pubKeyMgr = splVaultAddrs{addrs: map[string]bool{testVault: true}}

	const amount = 90_000_000 // the vault token account's whole balance
	txn := splVaultOutbound(c, testVault, amount)
	// CloseAccount(vault token account -> vault, authority vault) after the transfer.
	txn.Transaction.Message.Instructions = append(txn.Transaction.Message.Instructions,
		rpc.RPCInstruction{ProgramIdIndex: 6, Accounts: []int{1, 0, 0}, Data: base58.Encode([]byte{9})})
	txn.Meta.PostTokenBalances = txn.Meta.PostTokenBalances[1:] // the source is gone
	txn.Meta.PostBalances[1] = 0
	txn.Meta.PostBalances[0] += 2_039_280

	item := scanner.getTxInItem(txn, 200)
	c.Assert(item, NotNil)
	c.Check(item.Sender, Equals, testVault)
	c.Check(item.To, Equals, testSender)
	c.Check(item.Memo, Equals, "OUT:ABCDEF")
	c.Check(item.Coins[0].Amount.String(), Equals, "9000000000")
}
