package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestLateMigrationReplayIncludesOnlyEligibleSourceProof(t *testing.T) {
	for _, tc := range []struct {
		name       string
		origin     string
		eligible   bool
		wantSource bool
	}{
		{"certified source", sidecar.OutgoingOriginVaultCertified, true, true},
		{"local source", sidecar.OutgoingOriginLocalCombined, true, true},
		{"ineligible source", sidecar.OutgoingOriginVaultCertified, false, false},
		{"committed pairing", sidecar.OutgoingOriginChainCommitted, false, false},
		{"committed cannot promote itself", sidecar.OutgoingOriginChainCommitted, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, destination, txID, voter, refs := testXMRCatchupMigration(t, tc.name)
			blockHash := testXMRHash(tc.name + "-block")
			parentHash := testXMRHash(tc.name + "-parent")
			proof := sidecar.Transfer{
				TxID: txID, TxKey: voter.Tx.XmrTxKey,
				VaultPubKey: source.String(), FromAddress: voter.Tx.Tx.FromAddress.String(),
				Address: voter.Tx.Tx.ToAddress.String(), Amount: 2 * moneroAtomicToThorchain,
				Fee: moneroAtomicToThorchain, Height: 100, Note: voter.Tx.Tx.Memo,
				Origin: tc.origin, ObservationEligible: tc.eligible,
				SpentOutputRefs: []sidecar.SpentRef{{
					TxHash:      refs[0].TxHash,
					OutputIndex: refs[0].OutputIndex, KeyImage: refs[0].KeyImage, SpendTxHash: txID,
				}},
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/monero/vaults/register":
					_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(201))
				case "/v1/monero/chain/tx/" + txID + "/location":
					_ = json.NewEncoder(w).Encode(sidecar.CanonicalTxLocationResp{
						TxHash: txID, Height: 100, BlockHash: blockHash, PreviousHash: parentHash,
					})
				case "/v1/monero/outputs/mark-observed":
					_ = json.NewEncoder(w).Encode(proof)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			require.NoError(t, err)
			scanner, store, _ := newXMRCatchupReplayScanner(t, signer, source, destination)
			// Authentication arrived after scanning the mined block. No scanner rewind
			// or synthetic vote may be required to recover its source observation.
			scanner.setCurrentHeight(200)
			observed := common.NewObservedTx(voter.Tx.Tx, 100, source, 100)
			observed.XmrTxKey = voter.Tx.XmrTxKey
			observed.SpentOutputRefs = []common.OutputRef{*refs[0]}
			queued, err := scanner.EnqueueAuthenticatedInternalInboundReplay(observed)
			require.NoError(t, err)
			require.True(t, queued)
			items, claims, err := scanner.replayAuthenticatedInternalInbounds(201, 201)
			require.NoError(t, err)
			require.Len(t, claims, 1)
			wantCount := 1
			if tc.wantSource {
				wantCount = 2
			}
			require.Len(t, items, wantCount)
			require.True(t, items[0].ObservedVaultPubKey.Equals(destination))
			require.Empty(t, items[0].TxKey)
			require.Empty(t, items[0].SpentOutputRefs)
			if !tc.wantSource {
				items[0].OnObservedTxStored()
				count, countErr := store.Count()
				require.NoError(t, countErr)
				require.Zero(t, count)
				return
			}
			require.True(t, items[1].ObservedVaultPubKey.Equals(source))
			require.Equal(t, int64(100), items[1].BlockHeight)
			require.Equal(t, voter.Tx.XmrTxKey, items[1].TxKey)
			require.Equal(t, refs[0].KeyImage, items[1].SpentOutputRefs[0].KeyImage)
			require.Equal(t, items[0].Coins, items[1].Coins)
			require.Equal(t, items[0].Gas, items[1].Gas)

			items[0].OnObservedTxStored()
			items[0].OnObservedTxStored() // duplicate callbacks cannot acknowledge the pair
			count, err := store.Count()
			require.NoError(t, err)
			require.Equal(t, 1, count)
			items[1].OnObservedTxRejected(errors.New("observer persistence failed"))
			items, _, err = scanner.replayAuthenticatedInternalInbounds(202, 202)
			require.NoError(t, err)
			require.Len(t, items, 2, "a partial rejection must replay both observations")
			items[1].OnObservedTxStored()
			count, err = store.Count()
			require.NoError(t, err)
			require.Equal(t, 1, count)
			// A process crash after storing one side must retain the durable record.
			require.NoError(t, store.ResetClaims())
			items, _, err = scanner.replayAuthenticatedInternalInbounds(203, 203)
			require.NoError(t, err)
			require.Len(t, items, 2)
			var wg sync.WaitGroup
			for _, item := range items {
				wg.Add(1)
				go func() { defer wg.Done(); item.OnObservedTxStored() }()
			}
			wg.Wait()
			count, err = store.Count()
			require.NoError(t, err)
			require.Zero(t, count, "only the complete durable pair may clear the replay")
		})
	}
}
