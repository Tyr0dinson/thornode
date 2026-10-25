package solana

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// A Solana address is base58 with no checksum, so one that differs from a vault's only
// in letter case is a different, valid account. Funds sent to it never reach the vault,
// so a transaction paying it must never be reported as an inbound to the vault, even
// when it also references the vault and so is returned by the vault's scan. These tests
// use the production pubkey manager.

// base58CaseVariant flips one lowercase letter of addr to uppercase, keeping a valid
// Solana address: another account than addr.
func base58CaseVariant(c *C, addr string) string {
	for i := 0; i < len(addr); i++ {
		ch := addr[i]
		if ch < 'a' || ch > 'z' || ch == 'i' || ch == 'o' { // I and O are not base58
			continue
		}
		variant := addr[:i] + string(ch-'a'+'A') + addr[i+1:]
		if common.IsValidSOLAddress(variant) {
			return variant
		}
	}
	c.Fatalf("no case variant of %s", addr)
	return ""
}

// caseTestVault is a production pubkey manager knowing one random vault, its address,
// and a case variant of that address.
func caseTestVault(c *C) (*pubkeymanager.PubKeyManager, string, string) {
	pkm, err := pubkeymanager.NewPubKeyManager(nil, nil)
	c.Assert(err, IsNil)
	pk := types2.GetRandomEd25519PubKey()
	pkm.AddPubKey(pk, false, common.SigningAlgoEd25519)
	addr, err := pk.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	variant := base58CaseVariant(c, addr.String())
	c.Assert(variant, Not(Equals), addr.String())
	return pkm, addr.String(), variant
}

// reportedAsVaultInbound mirrors the observer's inbound filter (filterObservations): an
// item reaches THORChain as an inbound to a vault when its To is a pool address.
func reportedAsVaultInbound(pkm *pubkeymanager.PubKeyManager, item *stypes.TxInItem) bool {
	ok, _ := pkm.IsValidPoolAddress(item.To, common.SOLChain)
	return ok
}

// solTransferWithMemo is a SOL transfer of lamports from testSender to to, with a memo.
func solTransferWithMemo(to string, lamports uint64) *rpc.TransactionResult {
	return &rpc.TransactionResult{
		Slot: 100,
		Meta: rpc.RPCMeta{
			Fee:          5000,
			PreBalances:  []uint64{100_000_000_000, 0, 1, 1},
			PostBalances: []uint64{100_000_000_000 - lamports - 5000, lamports, 1, 1},
		},
		Transaction: rpc.RPCTxnData{
			Signatures: []string{testTxSig},
			Message: rpc.RPCMessage{
				AccountKeys: []string{testSender, to, testSystemProgram, testMemoProgram},
				Instructions: []rpc.RPCInstruction{
					{ProgramIdIndex: 2, Accounts: []int{0, 1}, Data: makeTransferData(lamports)},
					{ProgramIdIndex: 3, Accounts: []int{0}, Data: makeMemoData("=:BTC.BTC:bc1qxyz")},
				},
			},
		},
	}
}

func (s *GetTxInItemSuite) TestSOLTransferToVaultCaseVariantNotAVaultInbound(c *C) {
	pkm, vault, variant := caseTestVault(c)
	scanner := newTestScanner()
	scanner.pubKeyMgr = pkm

	item := scanner.getTxInItem(solTransferWithMemo(variant, 5_000_000_000), 100)
	c.Assert(item, NotNil)
	c.Check(item.To, Equals, variant)
	c.Check(reportedAsVaultInbound(pkm, item), Equals, false)

	// The same deposit to the vault itself is a vault inbound.
	item = scanner.getTxInItem(solTransferWithMemo(vault, 5_000_000_000), 100)
	c.Assert(item, NotNil)
	c.Check(item.To, Equals, vault)
	c.Check(reportedAsVaultInbound(pkm, item), Equals, true)
}

// An outbound to a case variant of the vault is an ordinary recipient, not a self-send,
// so it is not excluded from the durable-nonce path (and recentBlockhashDestinations,
// which keys on the exact address, agrees).
func (s *SolTestSuite) TestDurableDestinationCaseVariantNotExcluded(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	variant := base58CaseVariant(c, vaultAddr.String())
	cl := &SOLClient{}
	mk := func(to string) stypes.TxOutItem {
		return stypes.TxOutItem{Chain: common.SOLChain, VaultPubKeyEddsa: vault, ToAddress: common.Address(to)}
	}
	c.Check(cl.durableDestinationExcluded(mk(vaultAddr.String())), Equals, true)
	c.Check(cl.durableDestinationExcluded(mk(variant)), Equals, false)
	c.Check(cl.recentBlockhashDestinations(mk(variant))[variant], Equals, false)
}
