package monero

import (
	"strings"
	"testing"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
)

func migrationProofFixture(t *testing.T) (sidecar.Transfer, authenticatedInternalInboundReplay) {
	t.Helper()
	sourceVault, sourceAddress := testVaultPubKeyAndAddress(t)
	destinationVault, destinationAddress := testVaultPubKeyAndAddress(t)
	txID := testXMRHash("migration-proof")
	ref := testXMROutputRef("migration-proof-input", 1, txID)
	proof := sidecar.Transfer{
		TxID:        txID,
		TxKey:       strings.Repeat("1", 64),
		Address:     destinationAddress.String(),
		FromAddress: sourceAddress.String(),
		VaultPubKey: sourceVault.String(),
		Amount:      2*moneroAtomicToThorchain + 91,
		Fee:         moneroAtomicToThorchain + 77,
		Height:      100,
		Note:        "MIGRATE:100",
		Origin:      sidecar.OutgoingOriginChainCommitted,
		SpentOutputRefs: []sidecar.SpentRef{{
			TxHash: ref.TxHash, OutputIndex: ref.OutputIndex,
			KeyImage: ref.KeyImage, SpendTxHash: txID,
		}},
	}
	record := authenticatedInternalInboundReplay{
		TxID: txID, TxKey: proof.TxKey,
		BlockHeight: 100, BlockHash: testXMRHash("migration-proof-block"),
		PreviousHash:  testXMRHash("migration-proof-parent"),
		SourceAddress: sourceAddress.String(), SourceVaultPubKey: sourceVault.String(),
		ToAddress: destinationAddress.String(), DestinationVaultPubKey: destinationVault.String(),
		AmountThorchain: 2, FeeThorchain: 1, Memo: proof.Note,
		SpentOutputRefs: authenticatedInternalSpentRefsFromSidecar(proof.SpentOutputRefs),
	}
	return proof, record
}

// The durable replay identity is the single authority for both initial proof
// admission and later replay. Removing another validator must not weaken any
// part of this authenticated payment binding.
func TestAuthenticatedMigrationProofBindsDurableIdentity(t *testing.T) {
	t.Parallel()
	scanner := NewScanner(log.Logger, nil, nil, common.XMRChain, common.EmptyPubKey, 72)
	for _, origin := range []string{
		sidecar.OutgoingOriginLocalCombined,
		sidecar.OutgoingOriginVaultCertified,
		sidecar.OutgoingOriginChainCommitted,
	} {
		proof, record := migrationProofFixture(t)
		proof.Origin = origin
		if err := scanner.validateAuthenticatedMigrationProofForRecord(proof, record); err != nil {
			t.Fatalf("reject authenticated origin %q: %v", origin, err)
		}
	}

	tests := []struct {
		name   string
		mutate func(*sidecar.Transfer, *authenticatedInternalInboundReplay)
	}{
		{"locked", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.Locked = true }},
		{"txid", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.TxID = testXMRHash("other") }},
		{"tx key", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.TxKey = strings.Repeat("2", 64) }},
		{"height", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.Height++ }},
		{"destination", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.Address = p.FromAddress }},
		{"sender", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.FromAddress = p.Address }},
		{"vault", func(p *sidecar.Transfer, r *authenticatedInternalInboundReplay) {
			p.VaultPubKey = r.DestinationVaultPubKey
		}},
		{"amount", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.Amount += moneroAtomicToThorchain }},
		{"fee", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.Fee += moneroAtomicToThorchain }},
		{"zero fee", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.Fee = 0 }},
		{"memo", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.Note = "MIGRATE:101" }},
		{"unauthenticated origin", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.Origin = "" }},
		{"missing refs", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.SpentOutputRefs = nil }},
		{"different ref", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) { p.SpentOutputRefs[0].OutputIndex++ }},
		{"different key image", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) {
			p.SpentOutputRefs[0].KeyImage = testXMRHash("other-key-image")
		}},
		{"different spend", func(p *sidecar.Transfer, _ *authenticatedInternalInboundReplay) {
			p.SpentOutputRefs[0].SpendTxHash = testXMRHash("other-spend")
		}},
		{"invalid source binding", func(p *sidecar.Transfer, r *authenticatedInternalInboundReplay) {
			r.SourceAddress = r.ToAddress
			p.FromAddress = r.ToAddress
		}},
		{"invalid destination binding", func(p *sidecar.Transfer, r *authenticatedInternalInboundReplay) {
			r.ToAddress = r.SourceAddress
			p.Address = r.SourceAddress
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proof, record := migrationProofFixture(t)
			tc.mutate(&proof, &record)
			if err := scanner.validateAuthenticatedMigrationProofForRecord(proof, record); err == nil {
				t.Fatal("accepted altered migration proof")
			}
		})
	}
}

func TestAuthenticatedMigrationDestinationKeepsOnlyInboundMetadata(t *testing.T) {
	t.Parallel()
	scanner := NewScanner(log.Logger, nil, nil, common.XMRChain, common.EmptyPubKey, 72)
	proof, record := migrationProofFixture(t)
	if err := scanner.validateAuthenticatedMigrationProofForRecord(proof, record); err != nil {
		t.Fatal(err)
	}
	destination, err := scanner.authenticatedMigrationDestinationFromProof(proof, record)
	if err != nil {
		t.Fatal(err)
	}
	if destination.Tx != record.TxID || destination.BlockHeight != record.BlockHeight ||
		destination.Sender != record.SourceAddress || destination.To != record.ToAddress ||
		destination.ObservedVaultPubKey.String() != record.DestinationVaultPubKey || destination.Memo != record.Memo {
		t.Fatalf("migration destination changed authenticated identity: %+v", destination)
	}
	if destination.Coins.GetCoin(common.XMRAsset).Amount.Uint64() != record.AmountThorchain ||
		destination.Gas.ToCoins().GetCoin(common.XMRAsset).Amount.Uint64() != record.FeeThorchain {
		t.Fatal("migration destination changed authenticated amount or fee")
	}
	if destination.TxKey != "" || len(destination.SpentOutputRefs) != 0 {
		t.Fatal("migration destination carries outbound-only metadata")
	}
	if proof.TxKey == "" || len(proof.SpentOutputRefs) != 1 {
		t.Fatal("destination conversion mutated source authentication proof")
	}
}
