package solana

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// A Solana address is base58 with no checksum, so one that differs from a vault's only
// in letter case is a different, valid account. Funds sent to it never reach the vault,
// so a transaction paying it must never be reported as an inbound to the vault, even
// when it also references the vault and so is returned by the vault's scans. These
// tests use the production pubkey manager: splVaultAddrs matches exactly, so it cannot
// show the difference.

// base58CaseVariant flips one lowercase letter of addr to uppercase, keeping a valid
// 32-byte public key: another account than addr.
func base58CaseVariant(c *C, addr string) string {
	for i := 0; i < len(addr); i++ {
		ch := addr[i]
		if ch < 'a' || ch > 'z' || ch == 'i' || ch == 'o' { // I and O are not base58
			continue
		}
		variant := addr[:i] + string(ch-'a'+'A') + addr[i+1:]
		if _, err := types.PublicKeyFromStringStrict(variant); err == nil {
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

func (s *GetTxInItemSuite) TestSOLTransferToVaultCaseVariantNotAVaultInbound(c *C) {
	pkm, vault, variant := caseTestVault(c)
	scanner := newSPLTestScanner()
	scanner.pubKeyMgr = pkm
	c.Check(scanner.isVaultAddress(vault), Equals, true)
	c.Check(scanner.isVaultAddress(variant), Equals, false)

	item := scanner.getTxInItem(solDeposit(solTransferSpec{to: variant, lamports: 5_000_000_000}), 100)
	c.Assert(item, NotNil)
	c.Check(item.To, Equals, variant)
	c.Check(reportedAsVaultInbound(pkm, item), Equals, false)

	// Beside a deposit to the vault itself, the variant is just another transfer that
	// touches no vault: the deposit is observed, for its own amount.
	item = scanner.getTxInItem(solDeposit(
		solTransferSpec{to: vault, lamports: 1_000_000_000},
		solTransferSpec{to: variant, lamports: 5_000_000_000},
	), 100)
	c.Assert(item, NotNil)
	c.Check(item.To, Equals, vault)
	c.Check(item.Coins[0].Amount.Equal(convertLamportsToTHORChain(1_000_000_000)), Equals, true)
	c.Check(reportedAsVaultInbound(pkm, item), Equals, true)
}

func (s *SPLScannerSuite) TestTokenTransferToVaultCaseVariantNotAVaultInbound(c *C) {
	pkm, vault, variant := caseTestVault(c)
	scanner := newSPLTestScanner()
	scanner.pubKeyMgr = pkm

	// USDC into the variant's associated token account, in a transaction that also
	// (idempotently) creates the vault's own token account, so the vault wallet's and
	// token account's scans both return it.
	txn := splInboundTo(c, variant, 12_345_678)
	msg := &txn.Transaction.Message
	msg.AccountKeys = append(msg.AccountKeys, vault, ata(c, vault, testUSDCMint), testATAProgram, testSystemProgram)
	n := len(msg.AccountKeys)
	msg.Instructions = append(msg.Instructions, rpc.RPCInstruction{
		// CreateIdempotent: payer, associated account, wallet, mint, system, token program
		ProgramIdIndex: n - 2, Accounts: []int{0, n - 3, n - 4, 3, n - 1, 4}, Data: "2",
	})
	txn.Meta.PreBalances = append(txn.Meta.PreBalances, 0, 2_039_280, 1, 1)
	txn.Meta.PostBalances = append(txn.Meta.PostBalances, 0, 2_039_280, 1, 1)

	item := scanner.getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.To, Equals, variant)
	c.Check(reportedAsVaultInbound(pkm, item), Equals, false)

	// The same deposit to the vault itself is a vault inbound.
	item = scanner.getTxInItem(splInboundTo(c, vault, 12_345_678), 100)
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
