package solana

import (
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestScanRetriesSignatureWhoseClaimingScanFailed: a vault's own token transfer lists
// both the vault wallet and its token account, so both scans return it, and the first
// to claim it observes it. When that claiming scan fails, the signature must stay
// pending for a scan that can retry it. Here the token account's scan claims it and its
// read fails; if the wallet's scan skipped it and advanced its cursor past it, a
// HaltSOLTokens in force on the next pass (which stops scanning token accounts) would
// leave no scan to retry it, and the vault's outbound, which may carry its durable
// nonce update, would never be observed.
func (s *BlockScannerTestSuite) TestScanRetriesSignatureWhoseClaimingScanFailed(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	tokenAccount := ata(c, vaultAddr.String(), testUSDCMint)

	const outSig = "VaultTokenOutbound11111111111111111111111111111111111111111111111111"
	out := splVaultOutbound(c, vaultAddr.String(), 25_000_000)
	out.Transaction.Signatures = []string{outSig}
	m := &splScanRPC{
		txs:      map[string]*rpc.TransactionResult{outSig: out},
		getTxHit: map[string]int{},
		// The token account's scan lists the signature first and claims it; its read
		// of the transaction fails once.
		sigsDelay:  map[string]time.Duration{vaultAddr.String(): 300 * time.Millisecond},
		failTxOnce: map[string]bool{outSig: true},
	}
	sigs := map[string][]rpc.SignaturesForAddressResult{
		vaultAddr.String(): {{Signature: outSig, Slot: 950}},
		tokenAccount:       {{Signature: outSig, Slot: 950}},
	}
	m.set(1000, sigs)
	server := m.server(c)
	defer server.Close()

	scanner, _, txIns := s.newTokenScanTestScanner(c, server, vault)
	scanner.pubKeyMgr = singleVaultPubKeys{
		vault:           vault,
		PubKeyValidator: splVaultAddrs{addrs: map[string]bool{vaultAddr.String(): true}},
	}
	bridge := &haltMimirBridge{height: 100, halt: 0}
	scanner.bridge = bridge
	scanner.lastMimirCheck = time.Time{}

	observed := func() []string {
		var ids []string
		for _, txIn := range drainTxIns(txIns) {
			for _, item := range txIn.TxArray {
				ids = append(ids, item.Tx)
			}
		}
		return ids
	}

	scanner.scan()
	c.Check(observed(), HasLen, 0) // the claiming read failed
	c.Check(m.getTxHit[outSig], Equals, 1)

	// HaltSOLTokens is now in force: the token account is no longer scanned, so only
	// the wallet's scan can retry the signature.
	bridge.setHalt(50)
	scanner.lastMimirCheck = time.Time{}
	m.set(1100, sigs)
	time.Sleep(50 * time.Millisecond) // scan status writes are async
	scanner.scan()
	c.Check(observed(), DeepEquals, []string{outSig})
}
