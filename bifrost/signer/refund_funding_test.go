package signer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type refundFundingTestBridge struct {
	*mockSignBridge
	voter *ttypes.QueryObservedTxVoter
	err   error
	calls int
}

func (b *refundFundingTestBridge) GetRefundFundingVoter(context.Context, string) (*ttypes.QueryObservedTxVoter, bool, error) {
	b.calls++
	return b.voter, b.voter != nil, b.err
}

type refundFundingTestChain struct {
	MockChainClient
	proofCalls int
	proofError func(int) error
	source     common.Chain
}

type refundFundingTestPools struct {
	*pubkeymanager.MockPoolAddressValidator
	key common.PubKey
}

func (p *refundFundingTestPools) HasPubKey(key common.PubKey) bool { return p.key.Equals(key) }

func (c *refundFundingTestChain) ValidateRefundFunding(_ context.Context, tx common.ObservedTx) error {
	c.proofCalls++
	if !tx.Tx.Chain.Equals(c.source) {
		return errors.New("wrong source client")
	}
	if c.proofError != nil {
		return c.proofError(c.proofCalls)
	}
	return nil
}

func (c *refundFundingTestChain) SignTx(types.TxOutItem, int64) ([]byte, []byte, *types.TxInItem, error) {
	c.signCount++
	return []byte("signed-refund"), []byte("refund-checkpoint"), nil, nil
}

func newRefundFundingSigner(t *testing.T, source, payout common.Chain) (*Signer, *refundFundingTestBridge, *refundFundingTestChain, *refundFundingTestChain, TxOutStoreItem) {
	t.Helper()
	key := ttypes.GetRandomPubKey()
	id := common.TxID(strings.Repeat("A", 64))
	voter := &ttypes.QueryObservedTxVoter{
		TxID: id, Height: 80, FinalisedHeight: 90,
		Tx: ttypes.QueryObservedTx{
			Tx: common.Tx{
				ID: id, Chain: source, FromAddress: "source", ToAddress: "vault",
				Coins: common.NewCoins(common.NewCoin(source.GetGasAsset(), cosmos.NewUint(100))),
			},
			BlockHeight: 50, FinaliseHeight: 50, ObservedPubKey: key,
		},
	}
	voter.Actions = []ttypes.TxOutItem{{Chain: payout, ToAddress: "refund-address", InHash: id, Memo: "REFUND:" + id.String(), Coin: common.NewCoin(payout.GetGasAsset(), cosmos.NewUint(90))}}
	bridge := &refundFundingTestBridge{mockSignBridge: &mockSignBridge{blockHeight: 100, constants: map[string]int64{constants.SigningTransactionPeriod.String(): 300}}, voter: voter}
	sourceClient := &refundFundingTestChain{source: source}
	payoutClient := sourceClient
	if !payout.Equals(source) {
		payoutClient = &refundFundingTestChain{source: payout}
	}
	store, err := NewSignerStore(t.TempDir(), config.LevelDBOptions{}, "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	if metricsSingleton == nil {
		metricsSingleton, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{Enabled: false})
		require.NoError(t, err)
	}
	s := &Signer{
		thorchainBridge: bridge, constantsProvider: NewConstantsProvider(bridge), storage: store,
		logger: zerolog.Nop(), pubkeyMgr: &refundFundingTestPools{MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(), key: key}, m: metricsSingleton,
		tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
		chains:              map[common.Chain]chainclients.ChainClient{source: sourceClient, payout: payoutClient},
	}
	item := NewTxOutStoreItem(100, types.TxOutItem{
		Chain: payout, VaultPubKey: key, ToAddress: "refund-address", InHash: id,
		Memo: "REFUND:" + id.String(), Height: 100, Coins: common.NewCoins(common.NewCoin(payout.GetGasAsset(), cosmos.NewUint(90))),
	}, 0)
	require.NoError(t, store.Set(item))
	return s, bridge, sourceClient, payoutClient, item
}

func TestRefundFundingGuardBeforeSignAndSavedBroadcast(t *testing.T) {
	for _, signed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsigned", true: "saved-signature"}[signed], func(t *testing.T) {
			s, _, source, payout, item := newRefundFundingSigner(t, common.ETHChain, common.BTCChain)
			source.proofError = func(int) error { return errors.New("funding orphaned") }
			if signed {
				item.SignedTx, item.Checkpoint = []byte("saved-refund"), []byte("saved-checkpoint")
				require.NoError(t, s.storage.Set(item))
			}
			s.processTransaction(item)
			require.Equal(t, 1, source.proofCalls, "the source client must be consulted")
			require.Zero(t, payout.signCount)
			require.Zero(t, payout.broadcastCount)
			stored := s.storage.List()
			require.Len(t, stored, 1, "a funding hold must not retire the queued item")
			require.Equal(t, item.SignedTx, stored[0].SignedTx)
			require.Equal(t, item.Checkpoint, stored[0].Checkpoint)
		})
	}
}

func TestRefundFundingGuardRechecksAfterSignAndRetriesIdenticalBytes(t *testing.T) {
	s, _, source, payout, item := newRefundFundingSigner(t, common.ETHChain, common.BTCChain)
	source.proofError = func(call int) error {
		if call == 2 || call == 3 {
			return errors.New("funding disappeared during keysign")
		}
		return nil
	}
	s.processTransaction(item)
	require.Equal(t, 1, payout.signCount)
	require.Zero(t, payout.broadcastCount)
	stored := s.storage.List()
	require.Len(t, stored, 1)
	require.True(t, bytes.Equal([]byte("signed-refund"), stored[0].SignedTx), "post-sign hold must retain the new signed bytes")
	s.processTransaction(stored[0])
	require.Equal(t, 1, payout.signCount, "a held signed item must not be signed again")
	require.Zero(t, payout.broadcastCount)
	stored = s.storage.List()
	require.Len(t, stored, 1)
	s.processTransaction(stored[0])
	require.Equal(t, 1, payout.signCount)
	require.Equal(t, 1, payout.broadcastCount)
	require.Equal(t, 4, source.proofCalls)
	require.Empty(t, s.storage.List())
}

func TestRefundFundingGuardUsesOriginalMemoAndSourceChain(t *testing.T) {
	s, bridge, source, payout, item := newRefundFundingSigner(t, common.ETHChain, common.BTCChain)
	item.TxOutItem.OriginalMemo, item.TxOutItem.Memo = item.TxOutItem.Memo, ""
	_, _, err := s.signAndBroadcast(item)
	require.NoError(t, err)
	require.Equal(t, 2, source.proofCalls)
	require.Zero(t, payout.proofCalls, "payout-chain validation cannot prove source funding")
	require.Equal(t, 2, bridge.calls, "the voter must be refreshed after keysign")
	require.Equal(t, 1, payout.signCount)
	require.Equal(t, 1, payout.broadcastCount)
}

func TestRefundFundingGuardRefreshesVoterAfterSign(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "action-removed", true: "reverted"}[rollback], func(t *testing.T) {
			s, bridge, source, payout, item := newRefundFundingSigner(t, common.ETHChain, common.BTCChain)
			source.proofError = func(int) error {
				if rollback {
					bridge.voter.Reverted = true
				} else {
					bridge.voter.Actions = nil
				}
				return nil
			}
			s.processTransaction(item)
			require.Equal(t, 1, payout.signCount)
			require.Zero(t, payout.broadcastCount)
			require.Equal(t, 2, bridge.calls)
			require.Equal(t, 1, source.proofCalls)
			stored := s.storage.List()
			require.Len(t, stored, 1)
			require.Equal(t, []byte("signed-refund"), stored[0].SignedTx)
		})
	}
}

func TestRefundFundingGuardRejectsUnprovenSourceState(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Signer, *refundFundingTestBridge, *TxOutStoreItem)
	}{
		{"missing voter", func(_ *Signer, b *refundFundingTestBridge, _ *TxOutStoreItem) { b.voter = nil }},
		{"query failure", func(_ *Signer, b *refundFundingTestBridge, _ *TxOutStoreItem) { b.err = errors.New("unavailable") }},
		{"reverted", func(_ *Signer, b *refundFundingTestBridge, _ *TxOutStoreItem) { b.voter.Reverted = true }},
		{"unfinalised", func(_ *Signer, b *refundFundingTestBridge, _ *TxOutStoreItem) { b.voter.FinalisedHeight = 0 }},
		{"wrong voter", func(_ *Signer, b *refundFundingTestBridge, _ *TxOutStoreItem) {
			b.voter.TxID = common.TxID(strings.Repeat("B", 64))
		}},
		{"memo mismatch", func(_ *Signer, _ *refundFundingTestBridge, item *TxOutStoreItem) {
			item.TxOutItem.Memo = "REFUND:" + strings.Repeat("B", 64)
		}},
		{"malformed refund", func(_ *Signer, _ *refundFundingTestBridge, item *TxOutStoreItem) {
			item.TxOutItem.Memo = "refund:broken"
		}},
		{"missing capability", func(s *Signer, _ *refundFundingTestBridge, _ *TxOutStoreItem) {
			s.chains[common.ETHChain] = &MockChainClient{}
		}},
		{"missing source client", func(s *Signer, _ *refundFundingTestBridge, _ *TxOutStoreItem) { delete(s.chains, common.ETHChain) }},
		{"outbound voter fallback", func(_ *Signer, b *refundFundingTestBridge, _ *TxOutStoreItem) { b.voter.Actions = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, bridge, _, _, item := newRefundFundingSigner(t, common.ETHChain, common.BTCChain)
			tc.edit(s, bridge, &item)
			require.Error(t, s.validateRefundFunding(item.TxOutItem))
		})
	}
}

func TestRefundFundingGuardLeavesNativeAndSystemOutboundsAvailable(t *testing.T) {
	s, bridge, _, _, item := newRefundFundingSigner(t, common.THORChain, common.BTCChain)
	bridge.voter.Tx.BlockHeight, bridge.voter.Tx.FinaliseHeight = 0, 0
	require.NoError(t, s.validateRefundFunding(item.TxOutItem), "native deposits have no external mined occurrence")
	for _, semanticMemo := range []string{"OUT:" + item.TxOutItem.InHash.String(), "MIGRATE:100", "CONSOLIDATE", "RAGNAROK:100"} {
		item.TxOutItem.Memo = semanticMemo
		bridge.err = errors.New("system outbounds must not query a source voter")
		require.NoError(t, s.validateRefundFunding(item.TxOutItem))
	}
	require.Equal(t, 1, bridge.calls)
}

func TestRefundFundingUnsignedHoldYieldsAndRetries(t *testing.T) {
	s, bridge, source, payout, held := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	source.proofError = func(int) error { return errors.New("funding orphaned") }
	other := held
	other.Index++
	other.TxOutItem.Memo = "OUT:" + held.TxOutItem.InHash.String()
	require.NoError(t, s.storage.Set(other))
	p, err := newPipeline(10)
	require.NoError(t, err)
	for range 3 {
		p.SpawnSignings(s, bridge)
		p.Wait()
	}
	require.Equal(t, 1, payout.signCount, "unrelated payout must progress while fresh refund is held")
	require.Equal(t, 1, payout.broadcastCount)
	require.Equal(t, 1, source.proofCalls, "held refund waits for its retry height")
	stored := s.storage.List()
	require.Len(t, stored, 1)
	require.Equal(t, int64(101), stored[0].RefundFundingRetryHeight)
	bridge.blockHeight = 101
	source.proofError = nil
	s.recheckRefundFunding()
	p.SpawnSignings(s, bridge)
	p.Wait()
	require.Equal(t, 2, payout.broadcastCount)
	require.Empty(t, s.storage.List())
}

func TestRefundFundingExposedHoldDoesNotYield(t *testing.T) {
	for _, state := range []string{"signed", "checkpoint", "tx-checkpoint", "terminal", "observation"} {
		t.Run(state, func(t *testing.T) {
			s, _, source, _, item := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
			switch state {
			case "signed":
				item.SignedTx = []byte("saved")
			case "checkpoint":
				item.Checkpoint = []byte("nonce")
			case "tx-checkpoint":
				item.TxOutItem.Checkpoint = []byte("nonce")
			case "terminal":
				item.Round7Retry = true
			case "observation":
				item.Observation = &types.TxInItem{}
			}
			require.NoError(t, s.storage.Set(item))
			source.proofError = func(int) error { return errors.New("funding orphaned") }
			s.processTransaction(item)
			stored, err := s.storage.Get(item.Key())
			require.NoError(t, err)
			require.Zero(t, stored.RefundFundingRetryHeight, "exposed material retains serialization")
		})
	}
}

func TestRefundFundingRedeliveryRetainsSavedSignature(t *testing.T) {
	s, bridge, source, payout, item := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	source.proofError = func(call int) error {
		if call == 2 {
			return errors.New("funding disappeared")
		}
		return nil
	}
	s.processTransaction(item)
	require.Equal(t, 1, payout.signCount)
	require.Zero(t, payout.broadcastCount)
	saved, err := s.storage.Get(item.Key())
	require.NoError(t, err)
	require.Equal(t, []byte("signed-refund"), saved.SignedTx)
	// Batch is the admission operation used by a repeat delivery of this item.
	require.NoError(t, s.storage.Batch([]TxOutStoreItem{item}))
	again, err := s.storage.Get(item.Key())
	require.NoError(t, err)
	require.Equal(t, saved.SignedTx, again.SignedTx)
	require.Zero(t, again.RefundFundingRetryHeight, "post-signing hold must not yield from stale input")
	s.processTransaction(again)
	require.Equal(t, 1, payout.signCount, "redelivery cannot force a new keysign")
	require.Equal(t, 1, payout.broadcastCount)
	require.Equal(t, 3, bridge.calls)
}

func TestRefundFundingDeferralCannotEraseNewSigningState(t *testing.T) {
	s, _, _, _, item := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	signed := item
	signed.SignedTx = []byte("exposed")
	require.NoError(t, s.storage.Set(signed))
	require.NoError(t, s.deferRefundFunding(item))
	stored, err := s.storage.Get(item.Key())
	require.NoError(t, err)
	require.Equal(t, signed.SignedTx, stored.SignedTx)
	require.Zero(t, stored.RefundFundingRetryHeight)
}

func TestRefundFundingInternalRecoveryActions(t *testing.T) {
	s, b, _, _, item := newRefundFundingSigner(t, common.THORChain, common.BTCChain)
	b.voter.Height = 0
	b.voter.Tx = ttypes.QueryObservedTx{}
	b.voter.Txs = nil
	require.NoError(t, s.validateRefundFunding(item.TxOutItem), "actions-only internal recovery has no external deposit")
	b.voter.Tx.Tx.ID = item.TxOutItem.InHash
	require.ErrorContains(t, s.validateRefundFunding(item.TxOutItem), "source observation", "a partially populated external identity is not an internal exemption")
}

func TestRefundFundingTRONOutsideThisPatch(t *testing.T) {
	s, _, source, _, item := newRefundFundingSigner(t, common.TRONChain, common.TRONChain)
	require.NoError(t, s.validateRefundFunding(item.TxOutItem))
	require.Zero(t, source.proofCalls)
}

func TestRefundFundingSavedBytesSurviveRestartAndRedelivery(t *testing.T) {
	s, _, source, payout, item := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	directory := t.TempDir()
	store, err := NewSignerStore(directory, config.LevelDBOptions{}, "")
	require.NoError(t, err)
	saved := item
	saved.SignedTx = []byte("persisted-refund")
	require.NoError(t, store.Set(saved))
	require.NoError(t, store.Close())
	reopened, err := NewSignerStore(directory, config.LevelDBOptions{}, "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	s.storage = reopened
	require.NoError(t, reopened.Batch([]TxOutStoreItem{item}))
	retry, err := reopened.Get(item.Key())
	require.NoError(t, err)
	source.proofError = func(int) error { return errors.New("temporarily unavailable") }
	s.processTransaction(retry)
	held, err := reopened.Get(item.Key())
	require.NoError(t, err)
	require.Equal(t, saved.SignedTx, held.SignedTx)
	require.Zero(t, held.RefundFundingRetryHeight)
	source.proofError = nil
	s.processTransaction(held)
	require.Zero(t, payout.signCount)
	require.Equal(t, 1, payout.broadcastCount)
}

func TestRefundFundingConcurrentAdmissionAndExposure(t *testing.T) {
	s, _, _, _, item := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	for i := int64(1); i <= 30; i++ {
		fresh := item
		fresh.Index = i
		exposed := fresh
		exposed.SignedTx = []byte("exposed")
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; results <- s.storage.Set(exposed) }()
		go func() { <-start; results <- s.storage.Batch([]TxOutStoreItem{fresh}) }()
		close(start)
		require.NoError(t, <-results)
		require.NoError(t, <-results)
		stored, err := s.storage.Get(fresh.Key())
		require.NoError(t, err)
		require.Equal(t, exposed.SignedTx, stored.SignedTx)
	}
}

type refundFundingHintTestChain struct {
	*refundFundingTestChain
	receivedHints []int64
}

func (c *refundFundingHintTestChain) ValidateRefundFundingWithHints(ctx context.Context, tx common.ObservedTx, hints []int64) error {
	c.receivedHints = append([]int64(nil), hints...)
	return c.refundFundingTestChain.ValidateRefundFunding(ctx, tx)
}

func TestRefundFundingPassesOnlyMatchingMinedHeightHints(t *testing.T) {
	s, bridge, source, _, item := newRefundFundingSigner(t, common.ETHChain, common.BTCChain)
	hinted := &refundFundingHintTestChain{refundFundingTestChain: source}
	s.chains[common.ETHChain] = hinted
	variant := bridge.voter.Tx
	variant.BlockHeight = 40
	variant.Tx.Memo = "wire memo differs from the resolved memo"
	variant.Tx.Gas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(3))}
	variant.Tx.FromAddress = common.Address(strings.ToUpper(variant.Tx.FromAddress.String()))
	bridge.voter.Txs = []ttypes.QueryObservedTx{variant, variant, bridge.voter.Tx}
	require.NoError(t, s.validateRefundFunding(item.TxOutItem))
	require.Equal(t, []int64{40}, hinted.receivedHints)
	require.Equal(t, 1, source.proofCalls, "hints never bypass source validation")
	for _, tc := range []struct {
		name string
		edit func(*ttypes.QueryObservedTx)
	}{
		{"different ID", func(v *ttypes.QueryObservedTx) { v.Tx.ID = common.TxID(strings.Repeat("B", 64)) }},
		{"different chain", func(v *ttypes.QueryObservedTx) { v.Tx.Chain = common.BSCChain }},
		{"different sender", func(v *ttypes.QueryObservedTx) { v.Tx.FromAddress = "someone-else" }},
		{"different vault", func(v *ttypes.QueryObservedTx) { v.Tx.ToAddress = "another-vault" }},
		{"different amount", func(v *ttypes.QueryObservedTx) {
			v.Tx.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(99)))
		}},
		{"different key", func(v *ttypes.QueryObservedTx) { v.ObservedPubKey = ttypes.GetRandomPubKey() }},
		{"different finalise height", func(v *ttypes.QueryObservedTx) { v.FinaliseHeight++ }},
		{"zero height", func(v *ttypes.QueryObservedTx) { v.BlockHeight = 0 }},
		{"final height", func(v *ttypes.QueryObservedTx) { v.BlockHeight = v.FinaliseHeight }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rejected := variant
			tc.edit(&rejected)
			bridge.voter.Txs = []ttypes.QueryObservedTx{rejected}
			require.NoError(t, s.validateRefundFunding(item.TxOutItem))
			require.Empty(t, hinted.receivedHints)
		})
	}
	bridge.voter.Txs = nil
	for h := int64(1); h < 40; h++ {
		candidate := variant
		candidate.BlockHeight = h
		bridge.voter.Txs = append(bridge.voter.Txs, candidate)
	}
	require.NoError(t, s.validateRefundFunding(item.TxOutItem))
	require.Len(t, hinted.receivedHints, refundFundingMaxHeightHints)
	require.Equal(t, int64(1), hinted.receivedHints[0])
	require.Equal(t, int64(refundFundingMaxHeightHints), hinted.receivedHints[len(hinted.receivedHints)-1])
}

type refundFundingQueueBridge struct {
	*refundFundingTestBridge
	voters map[string]*ttypes.QueryObservedTxVoter
	calls  []string
}

func (b *refundFundingQueueBridge) GetRefundFundingVoter(_ context.Context, id string) (*ttypes.QueryObservedTxVoter, bool, error) {
	b.calls = append(b.calls, id)
	voter, found := b.voters[id]
	return voter, found, nil
}

func TestRefundFundingSlowHoldsYieldAcrossQueue(t *testing.T) {
	for _, tc := range []struct {
		refunds       int
		elapsedBlocks int64
	}{{2, 0}, {2, 5}, {7, 5}, {2, 12}} {
		t.Run(fmt.Sprintf("refunds-%d-duration-%d-blocks", tc.refunds, tc.elapsedBlocks), func(t *testing.T) {
			s, bridge, source, payout, first := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
			bridge.vault.Status = ttypes.VaultStatus_ActiveVault
			qb := &refundFundingQueueBridge{refundFundingTestBridge: bridge, voters: map[string]*ttypes.QueryObservedTxVoter{first.TxOutItem.InHash.String(): bridge.voter}}
			for i := 1; i < tc.refunds; i++ {
				item := first
				item.Index = int64(i)
				item.TxOutItem.InHash = common.TxID(fmt.Sprintf("%064X", i+1))
				item.TxOutItem.Memo = "REFUND:" + item.TxOutItem.InHash.String()
				voter := *bridge.voter
				voter.TxID, voter.Tx.Tx.ID = item.TxOutItem.InHash, item.TxOutItem.InHash
				voter.Actions = append([]ttypes.TxOutItem(nil), bridge.voter.Actions...)
				voter.Actions[0].InHash, voter.Actions[0].Memo = item.TxOutItem.InHash, item.TxOutItem.Memo
				qb.voters[item.TxOutItem.InHash.String()] = &voter
				require.NoError(t, s.storage.Set(item))
			}
			other := first
			other.Index = int64(tc.refunds)
			other.TxOutItem.InHash = common.TxID(strings.Repeat("C", 64))
			other.TxOutItem.Memo = "OUT:" + other.TxOutItem.InHash.String()
			require.NoError(t, s.storage.Set(other))
			s.thorchainBridge = qb
			s.constantsProvider = NewConstantsProvider(qb)
			source.proofError = func(int) error {
				// Model block progress during each slow RPC proof. No source
				// funding or signed state is changed by this clock advance.
				bridge.blockHeight += tc.elapsedBlocks
				return context.DeadlineExceeded
			}
			pipeline, err := newPipeline(10)
			require.NoError(t, err)
			for i := 0; i < tc.refunds+1; i++ {
				pipeline.SpawnSignings(s, qb)
				pipeline.Wait()
			}
			require.Equal(t, tc.refunds, source.proofCalls, "held refunds get one attempt before the queued OUT")
			require.Equal(t, 1, payout.signCount)
			require.Equal(t, 1, payout.broadcastCount)
			require.False(t, s.storage.Has(other.Key()))
			require.Len(t, s.storage.List(), tc.refunds)
			require.Equal(t, int64(100)+int64(tc.refunds)*tc.elapsedBlocks, bridge.blockHeight)
		})
	}
}

func TestRefundFundingEligibleRetryPrecedesLaterArrivals(t *testing.T) {
	s, bridge, source, payout, item := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	source.proofError = func(int) error { bridge.blockHeight += 5; return context.DeadlineExceeded }
	s.processTransaction(item)
	held, err := s.storage.Get(item.Key())
	require.NoError(t, err)
	require.Equal(t, int64(106), held.RefundFundingRetryHeight)
	source.proofError = nil
	bridge.blockHeight = 111
	s.recheckRefundFunding()
	later := item
	later.Height, later.TxOutItem.Height, later.Index = 111, 111, 1
	later.TxOutItem.Memo = "OUT:" + item.TxOutItem.InHash.String()
	require.NoError(t, s.storage.Set(later))
	pipeline, err := newPipeline(10)
	require.NoError(t, err)
	pipeline.SpawnSignings(s, bridge)
	pipeline.Wait()
	require.Equal(t, 4, source.proofCalls, "background recovery does not replace the pre-sign or pre-broadcast proof")
	require.Equal(t, 1, payout.broadcastCount)
	require.False(t, s.storage.Has(item.Key()))
	require.True(t, s.storage.Has(later.Key()), "continuous later arrivals cannot always jump ahead of an eligible retry")
}

func TestRefundFundingSchedulingPreservesExposedState(t *testing.T) {
	for _, state := range []string{"unsigned", "signed", "checkpoint", "tx-checkpoint", "terminal", "observation"} {
		t.Run(state, func(t *testing.T) {
			expose := func(item *TxOutStoreItem) {
				switch state {
				case "signed":
					item.SignedTx = []byte("saved")
				case "checkpoint":
					item.Checkpoint = []byte("nonce")
				case "tx-checkpoint":
					item.TxOutItem.Checkpoint = []byte("nonce")
				case "terminal":
					item.Round7Retry = true
				case "observation":
					item.Observation = &types.TxInItem{}
				}
			}
			if state == "unsigned" {
				s, bridge, source, _, stored := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
				stored.RefundFundingRetryHeight = 101
				require.NoError(t, s.storage.Set(stored))
				bridge.blockHeight = 150
				require.Empty(t, s.storageList(), "passing the retry height does not readmit a failed proof")
				s.recheckRefundFunding()
				require.Len(t, s.storageList(), 1)
				require.Equal(t, 1, source.proofCalls)
				return
			}
			s, bridge, source, payout, stored := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
			bridge.vault.Status = ttypes.VaultStatus_ActiveVault
			stored.RefundFundingRetryHeight = 150
			expose(&stored)
			require.NoError(t, s.storage.Set(stored))
			persisted, err := s.storage.Get(stored.Key())
			require.NoError(t, err)
			other := stored
			other.SignedTx, other.Checkpoint, other.TxOutItem.Checkpoint, other.Observation = nil, nil, nil, nil
			other.Round7Retry, other.RefundFundingRetryHeight = false, 0
			other.Height, other.TxOutItem.Height, other.Index = 101, 101, 1
			if state == "signed" || state == "terminal" {
				// These retry classes exclude even an older unsigned item.
				other.Height, other.TxOutItem.Height = 99, 99
			}
			other.TxOutItem.Memo = "OUT:" + stored.TxOutItem.InHash.String()
			require.NoError(t, s.storage.Set(other))
			source.proofError = func(int) error { return errors.New("funding unavailable") }
			pipeline, err := newPipeline(10)
			require.NoError(t, err)
			for i := 0; i < 3; i++ {
				pipeline.SpawnSignings(s, bridge)
				pipeline.Wait()
			}
			require.Equal(t, 3, source.proofCalls)
			require.Zero(t, payout.signCount)
			require.Zero(t, payout.broadcastCount)
			require.True(t, s.storage.Has(other.Key()), "saved signing state keeps the queue serialized")
			saved, err := s.storage.Get(stored.Key())
			require.NoError(t, err)
			require.Equal(t, stored.SignedTx, saved.SignedTx)
			require.Equal(t, persisted.Checkpoint, saved.Checkpoint)
			require.Equal(t, persisted.Observation, saved.Observation)
		})
	}
}

// Return a nonce checkpoint on join failure, like the ETH client. This leaves the
// same obligations pending so selection can be compared across modeled nodes.
type refundFundingJoinFailure struct {
	*refundFundingTestChain
	selected []string
}

func (c *refundFundingJoinFailure) SignTx(tx types.TxOutItem, _ int64) ([]byte, []byte, *types.TxInItem, error) {
	c.selected = append(c.selected, strings.SplitN(tx.GetMemo(), ":", 2)[0])
	return nil, []byte("0"), nil, errors.New("no keysign party")
}

func TestRefundFundingNodesConvergeAfterRecovery(t *testing.T) {
	a, ba, ca, _, r := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	b, bb, cb, _, rb := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	require.NoError(t, b.storage.Remove(rb))
	require.NoError(t, b.storage.Set(r))
	b.pubkeyMgr, bb.voter = a.pubkeyMgr, ba.voter
	fa, fb := &refundFundingJoinFailure{refundFundingTestChain: ca}, &refundFundingJoinFailure{refundFundingTestChain: cb}
	a.chains[common.ETHChain], b.chains[common.ETHChain] = fa, fb
	other := r
	other.Height, other.TxOutItem.Height, other.Index = 101, 101, 1
	other.TxOutItem.Memo = "OUT:" + r.TxOutItem.InHash.String()
	require.NoError(t, a.storage.Set(other))
	require.NoError(t, b.storage.Set(other))
	ba.blockHeight, bb.blockHeight = 101, 101
	cb.proofError = func(int) error { return errors.New("temporary RPC failure") }
	pa, err := newPipeline(10)
	require.NoError(t, err)
	pb, err := newPipeline(10)
	require.NoError(t, err)
	pa.SpawnSignings(a, ba)
	pa.Wait()
	pb.SpawnSignings(b, bb)
	pb.Wait()
	// While failed, B can attempt ordinary work. Preserve that checkpoint too.
	pb.SpawnSignings(b, bb)
	pb.Wait()
	require.Equal(t, []string{"OUT"}, fb.selected)
	cb.proofError = nil
	ba.blockHeight, bb.blockHeight = 110, 110
	b.recheckRefundFunding()
	fa.selected, fb.selected = nil, nil
	for range 3 {
		pa.SpawnSignings(a, ba)
		pa.Wait()
		pb.SpawnSignings(b, bb)
		pb.Wait()
	}
	require.Equal(t, []string{"REFUND", "REFUND", "REFUND"}, fa.selected)
	require.Equal(t, fa.selected, fb.selected, "local failure history must not change recovered priority")
	saved, err := b.storage.Get(other.Key())
	require.NoError(t, err)
	require.Equal(t, []byte("0"), saved.Checkpoint)
}

func TestRefundFundingFirstCheckRetainsHead(t *testing.T) {
	a, ba, ca, _, r := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	b, bb, cb, _, rb := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	require.NoError(t, b.storage.Remove(rb))
	require.NoError(t, b.storage.Set(r))
	b.pubkeyMgr, bb.voter = a.pubkeyMgr, ba.voter
	fa, fb := &refundFundingJoinFailure{refundFundingTestChain: ca}, &refundFundingJoinFailure{refundFundingTestChain: cb}
	a.chains[common.ETHChain], b.chains[common.ETHChain] = fa, fb
	other := r
	other.Height, other.Index, other.TxOutItem.Memo = 101, 1, "OUT:"+r.TxOutItem.InHash.String()
	require.NoError(t, a.storage.Set(other))
	require.NoError(t, b.storage.Set(other))
	entered, release := make(chan struct{}), make(chan struct{})
	cb.proofError = func(int) error { close(entered); <-release; return nil }
	pa, err := newPipeline(10)
	require.NoError(t, err)
	pb, err := newPipeline(10)
	require.NoError(t, err)
	pa.SpawnSignings(a, ba)
	pa.Wait()
	pb.SpawnSignings(b, bb)
	<-entered
	// A slow first check has no failure marker and remains the head, even if
	// the pipeline and background worker are polled again while it is running.
	pb.SpawnSignings(b, bb)
	b.recheckRefundFunding()
	require.Empty(t, fb.selected)
	close(release)
	pb.Wait()
	require.Equal(t, []string{"REFUND"}, fa.selected)
	require.Equal(t, fa.selected, fb.selected)
}

func TestRefundFundingBackgroundHoldDoesNotBlockOtherChain(t *testing.T) {
	s, bridge, source, payout, held := newRefundFundingSigner(t, common.ETHChain, common.BTCChain)
	held.RefundFundingRetryHeight = 101
	require.NoError(t, s.storage.Set(held))
	bridge.blockHeight = 101
	other := held
	other.Index, other.RefundFundingRetryHeight = 1, 0
	other.TxOutItem.Memo = "OUT:" + held.TxOutItem.InHash.String()
	require.NoError(t, s.storage.Set(other))
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	source.proofError = func(int) error { close(entered); <-release; return context.DeadlineExceeded }
	go func() { s.recheckRefundFunding(); close(done) }()
	<-entered
	p, err := newPipeline(10)
	require.NoError(t, err)
	p.SpawnSignings(s, bridge)
	p.Wait()
	require.Equal(t, 1, payout.broadcastCount, "a blocked ETH recheck must not occupy the BTC signing queue")
	close(release)
	<-done
	require.Equal(t, 1, source.proofCalls)
	require.Len(t, s.storage.List(), 1)
}

func TestRefundFundingBackgroundUpdatePreservesCurrentState(t *testing.T) {
	for _, state := range []string{"removed", "redelivered", "newer-hold", "gas", "signed", "checkpoint", "tx-checkpoint", "terminal", "observation"} {
		t.Run(state, func(t *testing.T) {
			s, bridge, source, _, held := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
			held.RefundFundingRetryHeight = 101
			require.NoError(t, s.storage.Set(held))
			bridge.blockHeight = 101
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			source.proofError = func(int) error { close(entered); <-release; return nil }
			go func() { s.recheckRefundFunding(); close(done) }()
			<-entered
			current := held
			current.RetrievalKey = held.Key()
			switch state {
			case "removed", "redelivered":
				require.NoError(t, s.storage.Remove(held))
				if state == "redelivered" {
					current.RefundFundingRetryHeight = 0
					require.NoError(t, s.storage.Batch([]TxOutStoreItem{current}))
				}
			case "newer-hold":
				current.RefundFundingRetryHeight = 200
			case "gas":
				current.TxOutItem.GasRate++
			case "signed":
				current.SignedTx = []byte("saved")
			case "checkpoint":
				current.Checkpoint = []byte("nonce")
			case "tx-checkpoint":
				current.TxOutItem.Checkpoint = []byte("nonce")
				current.Checkpoint = []byte("nonce")
			case "terminal":
				current.Round7Retry = true
			case "observation":
				current.Observation = &types.TxInItem{}
			}
			if state != "removed" && state != "redelivered" {
				require.NoError(t, s.storage.Set(current))
			}
			close(release)
			<-done
			if state == "removed" {
				require.False(t, s.storage.Has(held.Key()))
				return
			}
			saved, err := s.storage.Get(held.Key())
			require.NoError(t, err)
			require.Equal(t, current.RefundFundingRetryHeight, saved.RefundFundingRetryHeight)
			require.Equal(t, current.SignedTx, saved.SignedTx)
			require.Equal(t, current.Checkpoint, saved.Checkpoint)
			require.Equal(t, current.Round7Retry, saved.Round7Retry)
			require.Equal(t, current.Observation, saved.Observation)
			require.Equal(t, current.TxOutItem.GasRate, saved.TxOutItem.GasRate)
		})
	}
}

func TestRefundFundingFailedMarkerSurvivesRestart(t *testing.T) {
	s, bridge, source, payout, held := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	directory := t.TempDir()
	store, err := NewSignerStore(directory, config.LevelDBOptions{}, "")
	require.NoError(t, err)
	held.RefundFundingRetryHeight = 101
	require.NoError(t, store.Set(held))
	require.NoError(t, store.Close())
	reopened, err := NewSignerStore(directory, config.LevelDBOptions{}, "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	s.storage, bridge.blockHeight = reopened, 120
	require.Empty(t, s.storageList(), "restart must not forget the failed first check")
	s.recheckRefundFunding()
	require.NoError(t, reopened.Batch([]TxOutStoreItem{held}))
	p, err := newPipeline(10)
	require.NoError(t, err)
	p.SpawnSignings(s, bridge)
	p.Wait()
	require.Equal(t, 3, source.proofCalls, "recovery, pre-sign and pre-broadcast checks all run")
	require.Equal(t, 1, payout.broadcastCount)
	require.Empty(t, reopened.List())
}

func TestRefundFundingExpiredHoldUsesNormalCleanup(t *testing.T) {
	s, bridge, source, payout, held := newRefundFundingSigner(t, common.ETHChain, common.BTCChain)
	held.RefundFundingRetryHeight = 101
	require.NoError(t, s.storage.Set(held))
	s.cfg.Signer.RescheduleBufferBlocks = 150
	bridge.blockHeight = 250
	require.Empty(t, s.storageList(), "the exact last signing height is still held")
	bridge.blockHeight++
	s.recheckRefundFunding()
	require.Zero(t, source.proofCalls, "expired holds need no expensive background proof")
	p, err := newPipeline(10)
	require.NoError(t, err)
	p.SpawnSignings(s, bridge)
	p.Wait()
	require.Empty(t, s.storage.List(), "ordinary signer expiry retires only the local attempt")
	require.Zero(t, source.proofCalls)
	require.Zero(t, payout.signCount)
}

func TestRefundFundingRetryWorkerStopsAfterActiveCheck(t *testing.T) {
	s, bridge, source, _, held := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	held.RefundFundingRetryHeight = 101
	require.NoError(t, s.storage.Set(held))
	s.stopChan, s.wg, bridge.blockHeight = make(chan struct{}), &sync.WaitGroup{}, 101
	entered, release := make(chan struct{}), make(chan struct{})
	source.proofError = func(int) error { close(entered); <-release; return nil }
	s.wg.Add(1)
	go s.retryRefundFunding()
	<-entered
	close(s.stopChan)
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		t.Fatal("active proof must finish before signer resources close")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retry worker did not stop")
	}
	require.Equal(t, 1, source.proofCalls)
}

func TestRefundFundingBackgroundSweepsEachDueHoldOnce(t *testing.T) {
	s, bridge, source, _, held := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	held.RefundFundingRetryHeight = 101
	require.NoError(t, s.storage.Set(held))
	other := held
	other.Index++
	require.NoError(t, s.storage.Set(other))
	bridge.blockHeight = 101
	source.proofError = func(int) error { return context.DeadlineExceeded }
	s.recheckRefundFunding()
	require.Equal(t, 2, source.proofCalls, "one failed item must not prevent the next due check")
	s.recheckRefundFunding()
	require.Equal(t, 2, source.proofCalls, "checks are paced by THORChain height")
	bridge.blockHeight++
	s.recheckRefundFunding()
	require.Equal(t, 4, source.proofCalls)
}

func TestRefundFundingSweepSkipsNewlyExpiredAttempts(t *testing.T) {
	s, bridge, source, _, held := newRefundFundingSigner(t, common.ETHChain, common.ETHChain)
	s.cfg.Signer.RescheduleBufferBlocks = 150
	held.RefundFundingRetryHeight = 101
	require.NoError(t, s.storage.Set(held))
	other := held
	other.Index++
	require.NoError(t, s.storage.Set(other))
	bridge.blockHeight = 250
	source.proofError = func(int) error { bridge.blockHeight++; return context.DeadlineExceeded }
	s.recheckRefundFunding()
	require.Equal(t, 1, source.proofCalls, "a slow failed proof must not force more expired historical lookups")
	require.Len(t, s.storageList(), 2, "expired records return to ordinary queue cleanup")
}

func TestRefundFundingBackgroundSuccessRequiresFreshSigningProofs(t *testing.T) {
	for _, boundary := range []string{"pre-sign", "pre-broadcast"} {
		t.Run(boundary, func(t *testing.T) {
			s, bridge, source, payout, held := newRefundFundingSigner(t, common.ETHChain, common.BTCChain)
			held.RefundFundingRetryHeight = 101
			require.NoError(t, s.storage.Set(held))
			bridge.blockHeight = 101
			s.recheckRefundFunding()
			require.Len(t, s.storageList(), 1)
			require.Equal(t, 1, source.proofCalls)
			source.proofError = func(call int) error {
				if boundary == "pre-sign" || call == 3 {
					return errors.New("funding disappeared after recovery")
				}
				return nil
			}
			p, err := newPipeline(10)
			require.NoError(t, err)
			p.SpawnSignings(s, bridge)
			p.Wait()
			require.Zero(t, payout.broadcastCount)
			saved, err := s.storage.Get(held.Key())
			require.NoError(t, err)
			if boundary == "pre-sign" {
				require.Zero(t, payout.signCount)
				require.Positive(t, saved.RefundFundingRetryHeight)
			} else {
				require.Equal(t, 1, payout.signCount)
				require.Equal(t, []byte("signed-refund"), saved.SignedTx)
				require.Zero(t, saved.RefundFundingRetryHeight)
			}
		})
	}
}

func TestRefundFundingEmbeddedStoresSupportRecovery(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(SignerStorage) SignerStorage
	}{
		{"recovery", func(store SignerStorage) SignerStorage { return &recoveryStore{SignerStorage: store} }},
		{"destination", func(store SignerStorage) SignerStorage { return &destinationStore{SignerStorage: store} }},
		{"admission", func(store SignerStorage) SignerStorage { return &admissionReadErrorStore{SignerStorage: store} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, bridge, source, payout, item := newRefundFundingSigner(t, common.ETHChain, common.BTCChain)
			s.storage = tc.wrap(s.storage)
			source.proofError = func(int) error { return errors.New("source temporarily unavailable") }
			s.processTransaction(item)
			held, err := s.storage.Get(item.Key())
			require.NoError(t, err)
			require.Positive(t, held.RefundFundingRetryHeight, "embedding storage must preserve the failed-check handoff")
			require.Empty(t, s.storageList())
			bridge.blockHeight = held.RefundFundingRetryHeight
			source.proofError = nil
			s.recheckRefundFunding()
			recovered, err := s.storage.Get(item.Key())
			require.NoError(t, err)
			require.Zero(t, recovered.RefundFundingRetryHeight, "embedding storage must preserve the background completion")
			p, err := newPipeline(10)
			require.NoError(t, err)
			p.SpawnSignings(s, bridge)
			p.Wait()
			require.Equal(t, 4, source.proofCalls)
			require.Equal(t, 1, payout.signCount)
			require.Equal(t, 1, payout.broadcastCount)
			require.Empty(t, s.storage.List())
		})
	}
}
