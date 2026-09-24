package signer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Exercise the production Monero SignTx and signer pipeline without starting
// scanners or a FROST network. A valid successor stops at a retryable sidecar
// registration error, proving it was attempted but never falsely completed.
type destinationChainClient struct {
	chainclients.ChainClient
	attempts   []common.Address
	broadcasts int
}

type protectedDestinationClient struct {
	MockChainClient
	chain common.Chain
}

func (c *protectedDestinationClient) GetChain() common.Chain { return c.chain }

func (c *protectedDestinationClient) SignTx(tx types.TxOutItem, _ int64) ([]byte, []byte, *types.TxInItem, error) {
	c.signCount++
	return nil, tx.Checkpoint, nil, chainclienttypes.ErrUnsupportedXMRDestination
}

func (c *protectedDestinationClient) BroadcastTx(types.TxOutItem, []byte) (string, error) {
	c.broadcastCount++
	return "", chainclienttypes.ErrUnsupportedXMRDestination
}

func (s *SignCoverageSuite) TestUnsupportedDestinationPreservesSignerEvidence(c *C) {
	for _, state := range []string{"signed", "checkpoint", "terminal-round", "observation", "non-XMR"} {
		comment := Commentf("state %s", state)
		vaultKey, err := common.NewPubKey(pubkeymanager.MockPubkey)
		c.Assert(err, IsNil)
		item := NewTxOutStoreItem(90, types.TxOutItem{Chain: common.XMRChain, VaultPubKey: vaultKey, ToAddress: common.Address(common.XMRChecksummedDummyAddress('C')), Memo: "OUT:" + strings.Repeat("A", 64)}, 0)
		switch state {
		case "signed":
			item.SignedTx = []byte("exact-signed-bytes")
		case "checkpoint":
			item.Checkpoint = []byte("fixed-inputs")
		case "terminal-round":
			item.Round7Retry = true
		case "observation":
			item.Observation = &types.TxInItem{Tx: strings.Repeat("B", 64)}
		case "non-XMR":
			item.TxOutItem.Chain = common.ETHChain
		}
		bridge := &mockSignBridge{blockHeight: 100, constants: map[string]int64{constants.SigningTransactionPeriod.String(): 300}, mimirs: map[string]int64{"MAXOUTBOUNDATTEMPTS": 10}, vault: ttypes.Vault{Status: ttypes.VaultStatus_ActiveVault}}
		chain := &protectedDestinationClient{chain: item.TxOutItem.Chain}
		store, err := NewSignerStore("", config.LevelDBOptions{}, "")
		c.Assert(err, IsNil)
		c.Assert(store.Set(item), IsNil)
		sign := &Signer{logger: log.Logger, storage: store, thorchainBridge: bridge, constantsProvider: NewConstantsProvider(bridge), chains: map[common.Chain]chainclients.ChainClient{item.TxOutItem.Chain: chain}, pubkeyMgr: pubkeymanager.NewMockPoolAddressValidator(), m: GetMetricForTest(c), localPubKeyECDSA: vaultKey}
		sign.processTransaction(item)
		items := store.List()
		c.Assert(items, HasLen, 1, comment)
		c.Check(items[0].SignedTx, DeepEquals, item.SignedTx, comment)
		c.Check(items[0].Checkpoint, DeepEquals, item.Checkpoint, comment)
		c.Check(items[0].Observation, DeepEquals, item.Observation, comment)
		c.Check(items[0].Round7Retry, Equals, item.Round7Retry, comment)
		c.Check(chain.signCount == 0, Equals, state == "signed", comment)
		c.Check(chain.broadcastCount == 1, Equals, state == "signed", comment)
		c.Check(bridge.broadcastCalls, Equals, 0, comment)
		c.Assert(store.Close(), IsNil)
	}
}

func (c *destinationChainClient) IsBlockScannerHealthy() bool { return true }

func (c *destinationChainClient) CanSkipUnsupportedXMRDestination(tx types.TxOutItem) (bool, error) {
	return c.ChainClient.(*monero.Client).CanSkipUnsupportedXMRDestination(tx)
}

func (c *destinationChainClient) SignTx(tx types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error) {
	c.attempts = append(c.attempts, tx.ToAddress)
	return c.ChainClient.SignTx(tx, height)
}

func (c *destinationChainClient) BroadcastTx(types.TxOutItem, []byte) (string, error) {
	c.broadcasts++
	return "", fmt.Errorf("unexpected broadcast")
}

type destinationStore struct {
	SignerStorage
	removeFailures int
}

func (s *destinationStore) Remove(item TxOutStoreItem) error {
	if s.removeFailures > 0 {
		s.removeFailures--
		return fmt.Errorf("temporary delete failure")
	}
	return s.SignerStorage.Remove(item)
}

func (s *SignCoverageSuite) TestPipelineUnsupportedXMRDestination(c *C) {
	for _, failDelete := range []bool{false, true} {
		var registrationCalls, unexpectedCalls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/monero/info":
				_ = json.NewEncoder(w).Encode(sidecar.Info{
					Protocol: sidecar.ProtocolName, APIMajor: sidecar.APIMajorV2, StateSchema: sidecar.StateSchemaV4,
					Network: "mainnet", GitCommit: strings.Repeat("a", 40),
					Capabilities: []string{sidecar.CapabilityAtomicOutgoingV2, sidecar.CapabilityBalanceReconciliation, sidecar.CapabilityBoundRecovery, sidecar.CapabilityPerVaultKeyStatus, sidecar.CapabilityRestartReservation, sidecar.CapabilityDeadOutgoingQuarantine, sidecar.CapabilityScannerContinuity, sidecar.CapabilityDurableInboundReplay, sidecar.CapabilityScannerBoundedRewind, sidecar.CapabilitySharePlanQuarantine, sidecar.CapabilityVerifiedAbsenceBalance},
				})
			case "/v1/monero/sessions/clear":
				_, _ = w.Write([]byte(`{"cleared_sessions":0}`))
			case "/v1/monero/vaults/register":
				registrationCalls.Add(1)
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			default:
				unexpectedCalls.Add(1)
				http.Error(w, "unexpected sidecar request", http.StatusBadRequest)
			}
		}))
		defer server.Close()
		vaultKey, err := common.NewPubKey(pubkeymanager.MockPubkey)
		c.Assert(err, IsNil)
		frostKey := ttypes.GetRandomEd25519PubKey()
		bridge := &mockSignBridge{blockHeight: 100, constants: map[string]int64{constants.SigningTransactionPeriod.String(): 300}, vault: ttypes.Vault{PubKey: vaultKey, PubKeyFrost: frostKey, Status: ttypes.VaultStatus_ActiveVault}}
		client, err := monero.NewClient(nil, config.BifrostChainConfiguration{
			ChainID: common.XMRChain, SignerEndpoint: server.URL, SignerExpectedNetwork: "mainnet",
			BlockScanner: config.BifrostBlockScannerConfiguration{ChainID: common.XMRChain, StartBlockHeight: 1},
		}, &tssp.TssServer{}, bridge, GetMetricForTest(c))
		c.Assert(err, IsNil)
		defer client.Stop()
		chain := &destinationChainClient{ChainClient: client}
		storePath := c.MkDir()
		store, err := NewSignerStore(storePath, config.LevelDBOptions{}, "")
		c.Assert(err, IsNil)
		defer store.Close()
		storage := &destinationStore{SignerStorage: store}
		if failDelete {
			storage.removeFailures = 1
		}
		sign := &Signer{logger: log.Logger, storage: storage, thorchainBridge: bridge, constantsProvider: NewConstantsProvider(bridge), chains: map[common.Chain]chainclients.ChainClient{common.XMRChain: chain}, pubkeyMgr: pubkeymanager.NewMockPoolAddressValidator(), m: GetMetricForTest(c), localPubKeyECDSA: vaultKey}
		bad := NewTxOutStoreItem(90, types.TxOutItem{Chain: common.XMRChain, VaultPubKey: vaultKey, VaultPubKeyFrost: frostKey, ToAddress: common.Address(common.XMRChecksummedDummyAddress('C')), Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))), Memo: "OUT:" + strings.Repeat("A", 64)}, 0)
		good := bad
		good.Index = 1
		good.TxOutItem.ToAddress = common.Address(common.XMRChecksummedDummyAddress('4'))
		c.Assert(storage.Batch([]TxOutStoreItem{bad, good}), IsNil)
		pipeline, err := newPipeline(1)
		c.Assert(err, IsNil)
		step := func() { pipeline.SpawnSignings(sign, bridge); pipeline.Wait() }
		step()
		c.Assert(chain.attempts, DeepEquals, []common.Address{bad.TxOutItem.ToAddress})
		c.Assert(registrationCalls.Load(), Equals, int32(0))
		if failDelete {
			c.Assert(storage.Has(bad.Key()), Equals, true)
			c.Assert(storage.Has(good.Key()), Equals, true)
			step()
		}
		c.Assert(storage.Has(bad.Key()), Equals, false)
		step()
		c.Assert(chain.attempts[len(chain.attempts)-1], Equals, good.TxOutItem.ToAddress)
		c.Assert(registrationCalls.Load(), Equals, int32(1))
		c.Assert(storage.Has(good.Key()), Equals, true)
		// Restart reconstructs only the valid retry from storage. A THORChain
		// redelivery can insert the impossible obligation again; it skips again.
		c.Assert(store.Close(), IsNil)
		reopened, err := NewSignerStore(storePath, config.LevelDBOptions{}, "")
		c.Assert(err, IsNil)
		defer reopened.Close()
		storage.SignerStorage = reopened
		c.Assert(storage.List(), HasLen, 1)
		c.Assert(storage.Has(good.Key()), Equals, true)
		pipeline, err = newPipeline(1)
		c.Assert(err, IsNil)
		c.Assert(storage.Set(bad), IsNil)
		step()
		c.Assert(storage.Has(bad.Key()), Equals, false)
		step()
		c.Assert(registrationCalls.Load(), Equals, int32(2))
		c.Assert(storage.Has(good.Key()), Equals, true)
		c.Assert(chain.broadcasts, Equals, 0)
		c.Assert(unexpectedCalls.Load(), Equals, int32(0))
		c.Assert(bridge.broadcastCalls, Equals, 0)
		c.Assert(bad.TxOutItem.OutHash.IsEmpty(), Equals, true)
		// Observer is nil: any fabricated auto-observation would fail this test.

		// Keep admitting fresh unsupported obligations while all earlier ones
		// are redelivered. Admission keeps the queue and signing-pass work flat.
		// The valid payout must be attempted on every pass, including after a
		// restart and while its source vault retires during replacement.
		var pressure []types.TxArrayItem
		for round := 0; round < 20; round++ {
			for i := 0; i < 50; i++ {
				pressure = append(pressure, types.TxArrayItem{
					Chain: common.XMRChain, VaultPubKey: vaultKey, VaultPubKeyFrost: frostKey,
					ToAddress: bad.TxOutItem.ToAddress, Coin: bad.TxOutItem.Coins[0],
					Memo: fmt.Sprintf("OUT:%064X", round*50+i+1),
				})
			}
			c.Assert(sign.storeTxOut(types.TxOut{Height: int64(100 + round), TxArray: pressure}), IsNil)
			c.Assert(storage.List(), HasLen, 1)
			before := registrationCalls.Load()
			step()
			c.Assert(registrationCalls.Load(), Equals, before+1)
			c.Assert(chain.attempts[len(chain.attempts)-1], Equals, good.TxOutItem.ToAddress)
			if round == 9 {
				c.Assert(storage.Close(), IsNil)
				restarted, openErr := NewSignerStore(storePath, config.LevelDBOptions{}, "")
				c.Assert(openErr, IsNil)
				defer restarted.Close()
				storage.SignerStorage = restarted
				pipeline, err = newPipeline(1)
				c.Assert(err, IsNil)
				bridge.vault.Status = ttypes.VaultStatus_RetiringVault
			}
		}
		c.Assert(unexpectedCalls.Load(), Equals, int32(0))
		c.Assert(bridge.broadcastCalls, Equals, 0)

		// A replay must not overwrite a stored checkpoint or signed payload.
		delivery := types.TxOut{Height: 200, TxArray: pressure[:1]}
		protected := NewTxOutStoreItem(delivery.Height, delivery.TxArray[0].TxOutItem(delivery.Height), 0)
		protected.SignedTx = []byte("persisted-signed-payload")
		protected.Checkpoint = []byte("persisted-checkpoint")
		protected.Observation = &types.TxInItem{Tx: strings.Repeat("C", 64)}
		protected.Round7Retry = true
		c.Assert(storage.Set(protected), IsNil)
		c.Assert(sign.storeTxOut(delivery), IsNil)
		preserved, err := storage.Get(protected.Key())
		c.Assert(err, IsNil)
		c.Assert(preserved.SignedTx, DeepEquals, protected.SignedTx)
		c.Assert(preserved.Checkpoint, DeepEquals, protected.Checkpoint)
		c.Assert(preserved.Observation, DeepEquals, protected.Observation)
		c.Assert(preserved.Round7Retry, Equals, true)
	}
}
