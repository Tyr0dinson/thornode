package monero

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestUnsupportedAdmissionPreservesIndexedRecoveryEvidence(t *testing.T) {
	client, tx, server, _ := newFrostSignTxCleanupFixture(t, &mockFrostTssServer{}, 0)
	defer server.Close()
	tx.ToAddress = common.Address(common.XMRChecksummedDummyAddress('C'))
	identity, err := xmrSigningCacheIdentity(tx)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{
		signedTxPayloadKey(txCacheVault(tx, common.XMRChain), identity.CacheHash),
		signedTxPayloadClaimKey(identity.LogicalHash),
		signedTxCommittedLogicalKey(identity.LogicalHash),
		signedTxObservedLogicalKey(identity.LogicalHash),
		signedTxQuarantinedLogicalKey(identity.LogicalHash),
		signedTxBroadcastAttemptKey(identity.LogicalHash),
		signedTxPayloadObserveKey(identity.ObserveKeys[0]),
		signedTxCommittedAliasKey(identity.ObserveKeys[0]),
		signedTxQuarantinedAliasKey(identity.ObserveKeys[0]),
		signedTxObserveEpochKey(identity.ObserveKeys[0]),
	}
	for _, key := range keys {
		// Even malformed/partial evidence must remain available for recovery;
		// admission is not allowed to infer that an unreadable record is absent.
		if err = client.signedTxPayloadStore.db.Put([]byte(key), []byte("partial"), nil); err != nil {
			t.Fatal(err)
		}
		if skip, checkErr := client.CanSkipUnsupportedXMRDestination(tx); skip || checkErr != nil {
			t.Fatalf("discarded indexed recovery evidence %s: skip=%v err=%v", key, skip, checkErr)
		}
		if err = client.signedTxPayloadStore.db.Delete([]byte(key), nil); err != nil {
			t.Fatal(err)
		}
	}
	// The logical claim survives rescheduling to a replacement vault.
	if err = client.signedTxPayloadStore.db.Put([]byte(signedTxPayloadClaimKey(identity.LogicalHash)), []byte("old-vault-claim"), nil); err != nil {
		t.Fatal(err)
	}
	tx.VaultPubKeyFrost = common.PubKey("replacement-vault")
	if skip, checkErr := client.CanSkipUnsupportedXMRDestination(tx); skip || checkErr != nil {
		t.Fatalf("discarded prior vault claim: skip=%v err=%v", skip, checkErr)
	}
}
