package reporter

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	. "gopkg.in/check.v1"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	ctypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestPackage(t *testing.T) { TestingT(t) }

type SolvencySuite struct{}

var _ = Suite(&SolvencySuite{})

func expectedReportHeightForTest(chain common.Chain, cfg config.BifrostChainConfiguration, height int64) int64 {
	blocks := cfg.SolvencyBlocks
	if blocks <= 0 {
		switch chain {
		case common.ETHChain:
			blocks = 20
		case common.SOLChain:
			blocks = 50
		case common.GAIAChain, common.NOBLEChain, common.TRONChain, common.DOGEChain, common.ZECChain, common.XMRChain:
			blocks = 10
		case common.BTCChain, common.BCHChain:
			blocks = 2
		case common.LTCChain:
			blocks = 5
		default:
			blocks = 1
		}
	}

	reportHeight := (height / blocks) * blocks
	if chain == common.SOLChain && height%blocks == 0 {
		reportHeight -= blocks
	}
	if chain == common.TRONChain {
		reportHeight -= tronSolvencyFinalityBlocks
	}
	return reportHeight
}

// mockChainClient is a mock that satisfies the chainclients.ChainClient interface
type mockChainClient struct {
	chain    common.Chain
	cfg      config.BifrostChainConfiguration
	gas      cosmos.Uint
	account  common.Account
	accounts map[string]common.Account
	// accountErrors lets tests fail specific vault lookups without affecting others.
	accountErrors         map[string]error
	err                   error
	blockScannerHeight    int64
	blockScannerIsHealthy bool
	chainHeight           int64
	getHeightCalls        atomic.Int64
	vaultAccountHeights   []int64
}

func (m *mockChainClient) GetChain() common.Chain                      { return m.chain }
func (m *mockChainClient) GetConfig() config.BifrostChainConfiguration { return m.cfg }
func (m *mockChainClient) GetCurrentGas() cosmos.Uint                  { return m.gas }
func (m *mockChainClient) GetAccount(pk common.PubKey, height *big.Int) (common.Account, error) {
	if m.accounts != nil {
		if acct, ok := m.accounts[pk.String()]; ok {
			if m.accountErrors != nil {
				if err, ok := m.accountErrors[pk.String()]; ok {
					return acct, err
				}
			}
			return acct, m.err
		}
	}
	if m.accountErrors != nil {
		if err, ok := m.accountErrors[pk.String()]; ok {
			return m.account, err
		}
	}
	return m.account, m.err
}

func (m *mockChainClient) GetVaultAccount(vault ctypes.Vault, height *big.Int) (common.Account, error) {
	if height != nil {
		m.vaultAccountHeights = append(m.vaultAccountHeights, height.Int64())
	}
	accountPubKey, err := vault.AlgoPubKey(m.chain)
	if err != nil {
		return common.Account{}, err
	}
	return m.GetAccount(accountPubKey, height)
}
func (m *mockChainClient) GetBlockScannerHeight() (int64, error) { return m.blockScannerHeight, nil }
func (m *mockChainClient) IsBlockScannerHealthy() bool           { return m.blockScannerIsHealthy }
func (m *mockChainClient) Start(globalTxsQueue chan types.TxIn, globalErrataQueue chan types.ErrataBlock, reportDispatcher Dispatcher) {
}
func (m *mockChainClient) Stop() {}
func (m *mockChainClient) GetHeight() (int64, error) {
	m.getHeightCalls.Add(1)
	return m.chainHeight, nil
}
func (m *mockChainClient) OnObservedTxIn(txIn types.TxInItem, blockHeight int64) {}
func (m *mockChainClient) GetAccountByAddress(address string, _ *big.Int) (common.Account, error) {
	return m.account, nil
}
func (b *mockChainClient) ConfirmationCountReady(txIn types.TxIn) bool { return true }
func (b *mockChainClient) GetConfirmationCount(txIn types.TxIn) int64  { return 0 }
func (b *mockChainClient) GetLatestTxForVault(vault string) (string, string, error) {
	return "", "", nil
}

func (b *mockChainClient) SignTx(tx types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error) {
	return nil, nil, nil, nil
}

func (b *mockChainClient) BroadcastTx(_ types.TxOutItem, _ []byte) (string, error) { return "", nil }

func (m *mockChainClient) GetAddress(poolPubKey common.PubKey) string {
	addr, _ := thorchain.GetRandomPubKey().GetAddress(m.chain)
	return addr.String()
}

func (m *mockChainClient) GetGasEstimateForSolvency() cosmos.Uint {
	return m.gas
}

type mockVaultAccountChainClient struct {
	*mockChainClient
	vaultAccounts        map[string]common.Account
	getAccountCalls      int
	getVaultAccountCalls int
	vaultAccountHeights  []int64
}

func (m *mockVaultAccountChainClient) GetAccount(pk common.PubKey, height *big.Int) (common.Account, error) {
	m.getAccountCalls++
	return m.mockChainClient.GetAccount(pk, height)
}

func (m *mockVaultAccountChainClient) GetVaultAccount(vault ctypes.Vault, height *big.Int) (common.Account, error) {
	m.getVaultAccountCalls++
	if height != nil {
		m.vaultAccountHeights = append(m.vaultAccountHeights, height.Int64())
	}
	if acct, ok := m.vaultAccounts[vault.PubKey.String()]; ok {
		return acct, nil
	}
	return common.Account{}, fmt.Errorf("missing vault account for %s", vault.PubKey)
}

type solvencyDeferredTestError struct{}

func (solvencyDeferredTestError) Error() string {
	return "balance reconciliation pending"
}

func (solvencyDeferredTestError) SolvencyDeferred() bool {
	return true
}

type mimirMap struct {
	mimir map[string]int64
	mu    sync.Mutex
}

func (mm *mimirMap) set(key string, value int64) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if mm.mimir == nil {
		mm.mimir = map[string]int64{}
	}
	upperKey := strings.ToUpper(key)
	mm.mimir[upperKey] = value
}

func (mm *mimirMap) lookup(key string) (int64, bool) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	upperKey := strings.ToUpper(key)
	value, ok := mm.mimir[upperKey]
	return value, ok
}

func (mm *mimirMap) get(key string) int64 {
	value, ok := mm.lookup(key)
	if !ok {
		return -1
	}
	return value
}

// mockThorchainBridge is a mock for the bridge
type thorMockBridge struct {
	thorclient.ThorchainBridge
	getAsgardsFunc      func() (ctypes.Vaults, error)
	fetchNodeStatusFunc func() (ctypes.NodeStatus, error)
	msgs                []ctypes.MsgSolvency
	mimir               mimirMap
	thorchainHeight     int64
	mu                  sync.Mutex
}

func (m *thorMockBridge) GetAsgards() (ctypes.Vaults, error) {
	return m.getAsgardsFunc()
}

func (m *thorMockBridge) FetchNodeStatus() (ctypes.NodeStatus, error) {
	if m.fetchNodeStatusFunc != nil {
		return m.fetchNodeStatusFunc()
	}
	return ctypes.NodeStatus_Active, nil
}

func (b *thorMockBridge) GetSolvencyMsg(height int64, chain common.Chain, pubKey common.PubKey, coins common.Coins) *ctypes.MsgSolvency {
	addr := ctypes.GetRandomTHORAddress()
	signer, _ := addr.AccAddress()
	msg, _ := ctypes.NewMsgSolvency(chain, pubKey, coins, height, signer)
	return msg
}

func (b *thorMockBridge) Broadcast(msgs ...sdk.Msg) (common.TxID, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	msg, ok := msgs[0].(*ctypes.MsgSolvency)
	if !ok {
		return "", fmt.Errorf("unexpected message type: %T", msgs[0])
	}
	report := *msg
	if b.msgs == nil {
		b.msgs = []ctypes.MsgSolvency{report}
	} else {
		for _, m := range b.msgs {
			if report.Id.Equals(m.Id) && report.Height == m.Height && report.Chain == m.Chain && report.PubKey.Equals(m.PubKey) {
				// tx already signed - ignore
				return "", nil
			}
		}
		b.msgs = append(b.msgs, report)
	}
	return "0x123", nil
}

func (b *thorMockBridge) GetMimir(key string) (int64, error) {
	return b.mimir.get(key), nil
}

func (b *thorMockBridge) GetMimirWithRef(template, ref string) (int64, error) {
	key := fmt.Sprintf(template, ref)
	return b.GetMimir(key)
}

func (b *thorMockBridge) GetBlockHeight() (int64, error) {
	return b.thorchainHeight, nil
}

func (b *thorMockBridge) clearMsgs() {
	b.getMsgs()
}

func (b *thorMockBridge) getMsgs() []ctypes.MsgSolvency {
	b.mu.Lock()
	defer b.mu.Unlock()
	copyMsgs := b.msgs
	b.msgs = nil
	return copyMsgs
}

type mockSolvencyAttester struct {
	msgs []common.Solvency
	err  error
	mu   sync.Mutex
}

func (m *mockSolvencyAttester) AttestSolvency(_ context.Context, solvency common.Solvency) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, solvency)
	return m.err
}

func (m *mockSolvencyAttester) getMsgs() []common.Solvency {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := append([]common.Solvency(nil), m.msgs...)
	m.msgs = nil
	return cp
}

func (m *mockSolvencyAttester) msgCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.msgs)
}

type blockingSolvencyAttester struct {
	started     chan struct{}
	done        chan error
	startedOnce sync.Once
}

func newBlockingSolvencyAttester() *blockingSolvencyAttester {
	return &blockingSolvencyAttester{
		started: make(chan struct{}),
		done:    make(chan error, 1),
	}
}

func (m *blockingSolvencyAttester) AttestSolvency(ctx context.Context, _ common.Solvency) error {
	m.startedOnce.Do(func() { close(m.started) })
	<-ctx.Done()
	err := ctx.Err()
	m.done <- err
	return err
}

// ------- Tests
func (s *SolvencySuite) SetUpSuite(c *C) {
	ctypes.SetupConfigForTest()
}

func (s *SolvencySuite) TestReportSolvencyReporter(c *C) {
	// speed up Thorchain for quick tests
	origBlockTime := constants.ThorchainBlockTime
	defer func() { constants.ThorchainBlockTime = origBlockTime }()
	constants.ThorchainBlockTime = 100 * time.Millisecond

	defaultCoinsInVault := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1024_000_000)))
	insolventCoinsInVault := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(24_000_000)))
	avaxCoinsInVault := common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(1024_000_000)))
	avaxInsolventCoinsInVault := common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(24_000_000)))

	asgards := ctypes.Vaults{
		ctypes.Vault{
			PubKey: ctypes.GetRandomPubKey(),
			Coins:  defaultCoinsInVault,
		},
	}
	avaxAsgards := ctypes.Vaults{
		ctypes.Vault{
			PubKey: asgards[0].PubKey,
			Coins:  avaxCoinsInVault,
		},
	}
	stdAccount := common.Account{
		Sequence:      0,
		AccountNumber: 0,
		Coins:         defaultCoinsInVault,
	}
	insolventAccount := common.Account{
		Sequence:      0,
		AccountNumber: 0,
		Coins:         insolventCoinsInVault,
	}
	avaxAccount := common.Account{
		Sequence:      0,
		AccountNumber: 0,
		Coins:         avaxCoinsInVault,
	}
	avaxInsolventAccount := common.Account{
		Sequence:      0,
		AccountNumber: 0,
		Coins:         avaxInsolventCoinsInVault,
	}
	stdMockClient := &mockChainClient{
		chain:                 common.ETHChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 0,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 40,
			},
		},
		account: stdAccount,
	}
	insolventStdMockClient := &mockChainClient{
		chain:                 common.ETHChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 0,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 40,
			},
		},
		account: insolventAccount,
	}

	stdMockBridgeSetup := func(bridge *thorMockBridge) {
		bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
			return asgards, nil
		}
	}
	avaxMockBridgeSetup := func(bridge *thorMockBridge) {
		bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
			return avaxAsgards, nil
		}
	}
	testCases := []struct {
		name                    string
		height                  int64
		chainHeight             int64
		shouldReport            bool
		client                  *mockChainClient
		mockSetup               func(*thorMockBridge)
		expectedError           string
		expectReportFromCollect bool
		expectReportFromRunner  bool
	}{
		{
			name:                    "1. should report on ETH when height is a multiple of 20",
			height:                  1000,
			shouldReport:            true,
			client:                  stdMockClient,
			mockSetup:               stdMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: false, // vault is solvent, block scanner won't report
			expectReportFromRunner:  false, // runner reports only insolvent vaults when scanner is healthy
		},
		{
			name:                    "1a. - Insolvent Vault - should report on ETH when height is a multiple of 20",
			height:                  1000,
			shouldReport:            true,
			client:                  insolventStdMockClient, // <<-- vault is insolvent
			mockSetup:               stdMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: true, // scanner should report insolvency
			expectReportFromRunner:  true, // runner reports insolvent vaults
		},
		{
			name:                    "2. skip report on ETH before the first default 20-block cadence bucket",
			height:                  19,
			chainHeight:             100,
			shouldReport:            false,
			client:                  stdMockClient,
			mockSetup:               stdMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: false,
			expectReportFromRunner:  false,
		},
		{
			name:                    "2a. - Insolvent Vault - skip report on ETH before the first default 20-block cadence bucket",
			height:                  19,
			chainHeight:             100,
			shouldReport:            false,
			client:                  insolventStdMockClient, // <<-- vault is insolvent
			mockSetup:               stdMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: false, // scanner won't report at this height
			expectReportFromRunner:  true,  // runner uses chain height, which is already in the next cadence bucket
		},
		{
			name:         "3. should report when height is in a configured solvency cadence bucket (123) for EVM chains",
			height:       246,
			chainHeight:  246,
			shouldReport: true,
			client: &mockChainClient{
				chain:                 common.AVAXChain,
				blockScannerIsHealthy: true,
				gas:                   cosmos.NewUint(10),
				cfg: config.BifrostChainConfiguration{
					SolvencyBlocks: 123,
				},
				account: avaxAccount,
			},
			mockSetup:               avaxMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: false,
			expectReportFromRunner:  false,
		},
		{
			name:         "3a. - Insolvent Vault - should report when height is in a configured solvency cadence bucket (123) for EVM chains",
			height:       246,
			chainHeight:  246,
			shouldReport: true,
			client: &mockChainClient{
				chain:                 common.AVAXChain,
				blockScannerIsHealthy: true,
				gas:                   cosmos.NewUint(10),
				cfg: config.BifrostChainConfiguration{
					SolvencyBlocks: 123,
				},
				account: avaxInsolventAccount, // <<-- vault is insolvent
			},
			mockSetup:               avaxMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: true, // scanner should report insolvency
			expectReportFromRunner:  true, // runner reports insolvent vaults
		},
		{
			name:         "4. skip report when height is before the first configured solvency cadence bucket (1500) for EVM chains",
			height:       1001,
			shouldReport: false,
			client: &mockChainClient{
				chain:                 common.AVAXChain,
				blockScannerIsHealthy: true,
				gas:                   cosmos.NewUint(10),
				cfg: config.BifrostChainConfiguration{
					SolvencyBlocks: 1500,
				},
				account: avaxAccount,
			},
			mockSetup:               avaxMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: false,
			expectReportFromRunner:  false,
		},
		{
			name:         "5. skip report - should handle error from GetAsgards",
			height:       2000,
			shouldReport: true,
			client:       stdMockClient,
			mockSetup: func(bridge *thorMockBridge) {
				bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
					return nil, fmt.Errorf("failed to get asgards")
				}
			},
			expectedError:           "fail to get asgards: failed to get asgards",
			expectReportFromCollect: false,
			expectReportFromRunner:  false,
		},
		{
			name:         "6. skip report when block scanner is unhealthy",
			height:       2000,
			chainHeight:  2000, // keep runner on solvency_blocks cadence (1000)
			shouldReport: false,
			client: &mockChainClient{
				chain:                 common.ETHChain,
				blockScannerIsHealthy: false,
				gas:                   cosmos.NewUint(10),
				cfg: config.BifrostChainConfiguration{
					SolvencyBlocks: 1000,
				},
				account: stdAccount,
			},
			mockSetup:               stdMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: false,
			expectReportFromRunner:  true,
		},
		{
			name:         "7. skip report when current gas is not known yet for GAIA",
			height:       2000,
			shouldReport: false,
			client: &mockChainClient{
				chain:                 common.GAIAChain,
				blockScannerIsHealthy: true,
				gas:                   cosmos.NewUint(0),
				cfg: config.BifrostChainConfiguration{
					SolvencyBlocks: 1000,
				},
				account: stdAccount,
			},
			mockSetup:               stdMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: false,
			expectReportFromRunner:  false,
		},
		{
			name:         "8. skip report when current gas is not known yet for NOBLE",
			height:       2000,
			shouldReport: false,
			client: &mockChainClient{
				chain:                 common.NOBLEChain,
				blockScannerIsHealthy: true,
				gas:                   cosmos.NewUint(0),
				cfg: config.BifrostChainConfiguration{
					SolvencyBlocks: 1000,
				},
				account: stdAccount,
			},
			mockSetup:               stdMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: false,
			expectReportFromRunner:  false,
		},
		{
			name:         "9. skip report as chainHeight (2020) - fetchHeight (2000) > ObservationFlexibilityBlocks (5)",
			height:       2000,
			shouldReport: false,
			client: &mockChainClient{
				chain:                 common.ETHChain,
				blockScannerIsHealthy: true,
				gas:                   cosmos.NewUint(1),
				cfg: config.BifrostChainConfiguration{
					SolvencyBlocks: 1000,
					BlockScanner: config.BifrostBlockScannerConfiguration{
						ObservationFlexibilityBlocks: 5,
					},
				},
				account: stdAccount,
			},
			mockSetup:               stdMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: false,
			expectReportFromRunner:  false, // runner reports only insolvent vaults when scanner is healthy
		},
	}

	for _, tc := range testCases {
		c.Log(fmt.Sprintf("Test: %s", tc.name))
		fmt.Printf("Test: %s\n", tc.name)
		// setup environment
		mockBridge := &thorMockBridge{}
		tc.mockSetup(mockBridge)

		// helper functions
		verifyReport := func(from triggeredFrom, msg ctypes.MsgSolvency) {
			var expectedHeight int64
			if from == fromBlockScanner {
				expectedHeight = expectedReportHeightForTest(tc.client.GetChain(), tc.client.GetConfig(), tc.client.blockScannerHeight)
			} else {
				expectedHeight = expectedReportHeightForTest(tc.client.GetChain(), tc.client.GetConfig(), tc.client.chainHeight)
			}
			c.Assert(msg.Height, Equals, expectedHeight)
			c.Assert(msg.Chain, Equals, tc.client.GetChain())
			c.Assert(msg.PubKey.Equals(asgards[0].PubKey), Equals, true)
			c.Assert(msg.Coins, HasLen, 1)
			c.Assert(msg.Coins[0].Asset.Equals(tc.client.account.Coins[0].Asset), Equals, true)
			c.Assert(msg.Coins[0].Amount.Equal(tc.client.account.Coins[0].Amount), Equals, true)
		}
		verifyAttestedReport := func(from triggeredFrom, solvency common.Solvency) {
			var expectedHeight int64
			if from == fromBlockScanner {
				expectedHeight = expectedReportHeightForTest(tc.client.GetChain(), tc.client.GetConfig(), tc.client.blockScannerHeight)
			} else {
				expectedHeight = expectedReportHeightForTest(tc.client.GetChain(), tc.client.GetConfig(), tc.client.chainHeight)
			}
			c.Assert(solvency.Height, Equals, expectedHeight)
			c.Assert(solvency.Chain, Equals, tc.client.GetChain())
			c.Assert(solvency.PubKey.Equals(asgards[0].PubKey), Equals, true)
			c.Assert(solvency.Coins, HasLen, 1)
			c.Assert(solvency.Coins[0].Asset.Equals(tc.client.account.Coins[0].Asset), Equals, true)
			c.Assert(solvency.Coins[0].Amount.Equal(tc.client.account.Coins[0].Amount), Equals, true)
			c.Assert(solvency.Id.IsEmpty(), Equals, true)
		}
		verifyCollectedFromScanner := func(allMsgs []sdk.Msg) {
			if tc.expectReportFromCollect {
				c.Assert(allMsgs, HasLen, 1)
				msg, ok := allMsgs[0].(*ctypes.MsgSolvency)
				c.Assert(ok, Equals, true)
				verifyReport(fromBlockScanner, *msg)
			} else {
				c.Assert(allMsgs, HasLen, 0)
			}
		}
		var attester *mockSolvencyAttester
		verifyResults := func(from triggeredFrom, expectReport bool) {
			waitFor := constants.ThorchainBlockTime * 2
			deadline := time.Now().Add(waitFor)
			for time.Now().Before(deadline) {
				time.Sleep(constants.ThorchainBlockTime / 15)
				if attester != nil && attester.msgCount() > 0 {
					break
				}
			}
			msgs := attester.getMsgs()
			if expectReport {
				c.Assert(msgs, HasLen, 1)
				verifyAttestedReport(from, msgs[0])
			} else {
				c.Assert(msgs, HasLen, 0)
			}
		}

		// prepare reporter
		if tc.chainHeight > 0 {
			tc.client.chainHeight = tc.chainHeight
		} else {
			tc.client.chainHeight = tc.height + 20 // within default ObservationFlexibilityBlocks (40)
		}
		mockBridge.thorchainHeight = tc.client.chainHeight
		// set default ObservationFlexibilityBlocks if not set
		if tc.client.cfg.BlockScanner.ObservationFlexibilityBlocks == 0 {
			tc.client.cfg.BlockScanner.ObservationFlexibilityBlocks = 40
		}
		tc.client.blockScannerHeight = tc.height
		reporter := newSolvencyReporter(tc.client, mockBridge)
		reporter.setLastSolvencyCheckHeight(0)

		// test ShouldReport
		shouldReport := reporter.ShouldReport(fromBlockScanner, tc.client.blockScannerHeight)
		c.Assert(shouldReport, Equals, tc.shouldReport, Commentf(tc.name))

		// test Collect
		if shouldReport {
			allMsgs, err := reporter.Collect(fromBlockScanner, tc.client.blockScannerHeight)
			if tc.expectedError != "" {
				c.Assert(err, NotNil)
				c.Assert(err.Error(), Equals, tc.expectedError)
			} else {
				c.Assert(err, IsNil)
			}
			// verify Collect results
			verifyCollectedFromScanner(allMsgs)
		}

		// test full reporter dispatcher with running solvency runner
		func() {
			// setup environment
			mockBridge.clearMsgs()
			stopper := make(chan struct{})
			wg := sync.WaitGroup{}
			chain := tc.client.GetChain()
			chains := map[common.Chain]ChainClient{chain: tc.client}
			attester = &mockSolvencyAttester{}
			defer func() {
				// stop goroutines
				close(stopper)
				wg.Wait()
			}()

			// prepare dispatcher
			disp := NewDispatcher(chains, mockBridge, attester, nil, stopper, &wg)

			// now trigger reporter checks
			disp.Process(chain, 0)

			// verify the solvency message was attested
			verifyResults(fromBlockScanner, tc.expectReportFromCollect)

			// test solvency runner (it's already running, started by NewDispatcher())
			// runner should not report anything as chain is not halted yet
			verifyResults(fromRunner, false)
			// now halt the chain
			mockBridge.mimir.set(fmt.Sprintf("Halt%sChain", tc.client.chain), 100)

			resetLastSolvencyCheck := func() {
				// reset last check height to force the check
				dImpl, ok := disp.(*dispatcher)
				c.Assert(ok, Equals, true)
				reporters := dImpl.reporters[tc.client.chain]
				var solvencyRep *solvencyReporter
				for _, rep := range reporters {
					if r, ok := rep.(*solvencyReporter); ok {
						solvencyRep = r
						break
					}
				}
				c.Assert(solvencyRep, NotNil)
				solvencyRep.setLastSolvencyCheckHeight(0) // reset to 0
			}

			resetLastSolvencyCheck()
			// block scanner reports only when vault is insolvent
			// runner reports insolvent vaults, and reports all solvent vaults only when the scanner is unhealthy
			// so runner should report here if expected
			verifyResults(fromRunner, tc.expectReportFromRunner)

			// test runner with unhalt/halt mimirs
			// unhalt the chain
			resetLastSolvencyCheck()
			mockBridge.mimir.set(fmt.Sprintf("Halt%sChain", tc.client.chain), 0)
			// runner should not report anything as chain is not halted
			verifyResults(fromRunner, false)
			// 1 - admin halt, skip report solvency
			resetLastSolvencyCheck()
			mockBridge.mimir.set(fmt.Sprintf("Halt%sChain", tc.client.chain), 1)
			verifyResults(fromRunner, false)
			// should report solvency if expected
			resetLastSolvencyCheck()
			mockBridge.mimir.set(fmt.Sprintf("SolvencyHalt%sChain", tc.client.chain), 1)
			verifyResults(fromRunner, tc.expectReportFromRunner)
		}()
	}
}

func (s *SolvencySuite) TestShouldReportFromRunnerRequiresActiveHalt(c *C) {
	client := &mockChainClient{chain: common.ETHChain}
	bridge := &thorMockBridge{thorchainHeight: 100}
	reporter := newSolvencyReporter(client, bridge)

	c.Assert(reporter.shouldReportFromRunner(), Equals, false)

	bridge.mimir.set("HaltETHChain", 1)
	c.Assert(reporter.shouldReportFromRunner(), Equals, false)

	bridge.mimir.set("HaltETHChain", 120)
	c.Assert(reporter.shouldReportFromRunner(), Equals, false)

	bridge.mimir.set("HaltETHChain", 100)
	c.Assert(reporter.shouldReportFromRunner(), Equals, true)

	bridge.mimir.set("HaltETHChain", 0)
	bridge.mimir.set("SolvencyHaltETHChain", 120)
	c.Assert(reporter.shouldReportFromRunner(), Equals, false)

	bridge.mimir.set("SolvencyHaltETHChain", 100)
	c.Assert(reporter.shouldReportFromRunner(), Equals, true)
}

func (s *SolvencySuite) TestDispatcherGatesRunnerBeforeResolvingChainHeight(c *C) {
	client := &mockChainClient{
		chain:       common.ETHChain,
		chainHeight: 100,
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 20,
		},
	}
	bridge := &thorMockBridge{thorchainHeight: 100}
	reporter := newSolvencyReporter(client, bridge)
	// Keep the active-halt pass from collecting; this test is concerned only
	// with whether the dispatcher resolves the external chain height.
	reporter.setLastSolvencyCheckHeight(client.chainHeight)

	d := &dispatcher{
		logger:      log.Logger,
		reportQueue: make(chan ReportBatch, 1),
		stopper:     make(chan struct{}),
		chains:      map[common.Chain]ChainClient{common.ETHChain: client},
		reporters: map[common.Chain]Reporters{
			common.ETHChain: {reporter},
		},
	}

	d.processInternal(common.ETHChain, fromRunner, 0)
	c.Assert(client.getHeightCalls.Load(), Equals, int64(0))

	bridge.mimir.set("HaltETHChain", bridge.thorchainHeight)
	d.processInternal(common.ETHChain, fromRunner, 0)
	c.Assert(client.getHeightCalls.Load(), Equals, int64(1))
}

// TestCollectSkipsSolventVaultsWhenInsolventPresent ensures a runner collect only reports
// insolvent vaults when both solvent and insolvent vaults exist.
func (s *SolvencySuite) TestCollectSkipsSolventVaultsWhenInsolventPresent(c *C) {
	solventVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1_000_000_000))),
	}
	insolventVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1_000_000_000))),
	}
	asgards := ctypes.Vaults{solventVault, insolventVault}

	solventAccount := common.Account{Coins: solventVault.Coins}
	insolventAccount := common.Account{Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))}

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return asgards, nil
	}

	client := &mockChainClient{
		chain:                 common.ETHChain,
		blockScannerIsHealthy: false,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 1,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 40,
			},
		},
		account: solventAccount,
		accounts: map[string]common.Account{
			solventVault.PubKey.String():   solventAccount,
			insolventVault.PubKey.String(): insolventAccount,
		},
		chainHeight: 100,
	}

	reporter := newSolvencyReporter(client, bridge)
	reporter.setLastSolvencyCheckHeight(0)

	batch, err := reporter.Collect(fromRunner, client.chainHeight)
	c.Assert(err, IsNil)
	c.Assert(batch, HasLen, 1)

	msg, ok := batch[0].(*ctypes.MsgSolvency)
	c.Assert(ok, Equals, true)
	c.Assert(msg.PubKey.Equals(insolventVault.PubKey), Equals, true)
}

// TestCollectReportsAllSolventWhenScannerDown ensures runner reporting emits all solvent
// vaults when the block scanner is unhealthy and no vaults are insolvent.
func (s *SolvencySuite) TestCollectReportsAllSolventWhenScannerDown(c *C) {
	solventVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1_000_000_000))),
	}
	otherSolventVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(2_000_000_000))),
	}
	asgards := ctypes.Vaults{solventVault, otherSolventVault}

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return asgards, nil
	}

	client := &mockChainClient{
		chain:                 common.ETHChain,
		blockScannerIsHealthy: false,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 1,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 40,
			},
		},
		account: common.Account{Coins: solventVault.Coins},
		accounts: map[string]common.Account{
			solventVault.PubKey.String():      {Coins: solventVault.Coins},
			otherSolventVault.PubKey.String(): {Coins: otherSolventVault.Coins},
		},
		chainHeight: 200,
	}

	reporter := newSolvencyReporter(client, bridge)
	reporter.setLastSolvencyCheckHeight(0)

	batch, err := reporter.Collect(fromRunner, client.chainHeight)
	c.Assert(err, IsNil)
	c.Assert(batch, HasLen, len(asgards))

	reported := map[string]bool{}
	for _, reportMsg := range batch {
		msg, ok := reportMsg.(*ctypes.MsgSolvency)
		c.Assert(ok, Equals, true)
		reported[msg.PubKey.String()] = true
	}
	c.Assert(reported[solventVault.PubKey.String()], Equals, true)
	c.Assert(reported[otherSolventVault.PubKey.String()], Equals, true)
}

// TestCollectAllSolventUsesChainVaultCount verifies unrelated Asgard vaults do
// not prevent a chain reporter from emitting the auto-unhalt all-solvent report.
func (s *SolvencySuite) TestCollectAllSolventUsesChainVaultCount(c *C) {
	secpPubKey := ctypes.GetRandomPubKey()
	eddsaPubKey := ctypes.GetRandomEd25519PubKey()
	solVault := ctypes.Vault{
		PubKey:      secpPubKey,
		PubKeyEddsa: eddsaPubKey,
		Coins:       common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(1_000_000_000))),
	}
	btcVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(2_000_000))),
	}

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{solVault, btcVault}, nil
	}

	client := &mockChainClient{
		chain:                 common.SOLChain,
		blockScannerIsHealthy: false,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 1,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 40,
			},
		},
		accounts: map[string]common.Account{
			eddsaPubKey.String(): {Coins: solVault.Coins},
		},
		chainHeight: 200,
	}

	reporter := newSolvencyReporter(client, bridge)
	reporter.setLastSolvencyCheckHeight(0)

	batch, err := reporter.Collect(fromRunner, client.chainHeight)
	c.Assert(err, IsNil)
	c.Assert(batch, HasLen, 1)
	c.Assert(reporter.getLastSolvencyCheckHeight(), Equals, int64(199))

	msg, ok := batch[0].(*ctypes.MsgSolvency)
	c.Assert(ok, Equals, true)
	c.Assert(msg.PubKey.Equals(solVault.PubKey), Equals, true)
	c.Assert(msg.Chain.Equals(common.SOLChain), Equals, true)
}

func (s *SolvencySuite) TestCollectUsesVaultScopedAccountLookupWhenAvailable(c *C) {
	vault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1_000_000_000))),
	}
	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{vault}, nil
	}

	baseClient := &mockChainClient{
		chain:                 common.ETHChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 1,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 40,
			},
		},
		accounts: map[string]common.Account{
			vault.PubKey.String(): {Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))},
		},
		blockScannerHeight: 200,
	}
	client := &mockVaultAccountChainClient{
		mockChainClient: baseClient,
		vaultAccounts: map[string]common.Account{
			vault.PubKey.String(): {Coins: vault.Coins},
		},
	}

	reporter := newSolvencyReporter(client, bridge)
	batch, err := reporter.Collect(fromBlockScanner, client.blockScannerHeight)
	c.Assert(err, IsNil)
	c.Assert(batch, HasLen, 0)
	c.Assert(client.getVaultAccountCalls, Equals, 1)
	c.Assert(client.getAccountCalls, Equals, 0)
	c.Assert(reporter.getLastSolvencyCheckHeight(), Equals, client.blockScannerHeight)
}

// TestCollectAllSolventPathRequiresRunner verifies the auto-unhalt "all solvent"
// branch cannot be reached by block-scanner-triggered collects.
func (s *SolvencySuite) TestCollectAllSolventPathRequiresRunner(c *C) {
	solventVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1_000_000_000))),
	}
	otherSolventVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(2_000_000_000))),
	}
	asgards := ctypes.Vaults{solventVault, otherSolventVault}

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return asgards, nil
	}

	client := &mockChainClient{
		chain:                 common.ETHChain,
		blockScannerIsHealthy: false,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 1,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 40,
			},
		},
		accounts: map[string]common.Account{
			solventVault.PubKey.String():      {Coins: solventVault.Coins},
			otherSolventVault.PubKey.String(): {Coins: otherSolventVault.Coins},
		},
		blockScannerHeight: 200,
	}

	reporter := newSolvencyReporter(client, bridge)
	batch, err := reporter.Collect(fromBlockScanner, client.blockScannerHeight)
	c.Assert(err, IsNil)
	c.Assert(batch, HasLen, 0)
}

func (s *SolvencySuite) TestShouldReportAtHeightSolChain(c *C) {
	client := &mockChainClient{
		chain:                 common.SOLChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 100,
		},
		account: common.Account{Coins: common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(1)))},
	}
	reporter := newSolvencyReporter(client, &thorMockBridge{})
	reporter.setLastSolvencyCheckHeight(0)

	c.Assert(reporter.shouldReportAtHeight(100), Equals, false)
	c.Assert(reporter.shouldReportAtHeight(101), Equals, true)
}

func (s *SolvencySuite) TestShouldReportAtHeightSolChainUsesDefaultCadence(c *C) {
	client := &mockChainClient{
		chain: common.SOLChain,
		account: common.Account{
			Coins: common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(1))),
		},
	}
	reporter := newSolvencyReporter(client, &thorMockBridge{})
	reporter.setLastSolvencyCheckHeight(0)

	c.Assert(reporter.shouldReportAtHeight(50), Equals, false)
	c.Assert(reporter.shouldReportAtHeight(51), Equals, true)
}

func (s *SolvencySuite) TestShouldReportAtHeightEVMUsesCrossedConfiguredBoundary(c *C) {
	client := &mockChainClient{
		chain: common.AVAXChain,
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 100,
		},
		account: common.Account{
			Coins: common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(1))),
		},
	}
	reporter := newSolvencyReporter(client, &thorMockBridge{})
	reporter.setLastSolvencyCheckHeight(101)

	c.Assert(reporter.shouldReportAtHeight(199), Equals, false)
	c.Assert(reporter.shouldReportAtHeight(204), Equals, true)
}

func (s *SolvencySuite) TestShouldReportAtHeightETHUsesCrossedDefaultBoundary(c *C) {
	client := &mockChainClient{
		chain: common.ETHChain,
		account: common.Account{
			Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1))),
		},
	}
	reporter := newSolvencyReporter(client, &thorMockBridge{})
	reporter.setLastSolvencyCheckHeight(21)

	c.Assert(reporter.shouldReportAtHeight(39), Equals, false)
	c.Assert(reporter.shouldReportAtHeight(43), Equals, true)
}

func (s *SolvencySuite) TestShouldReportAtHeightXRPUsesCrossedConfiguredBoundary(c *C) {
	xrpAsset, err := common.NewAsset("XRP.XRP")
	c.Assert(err, IsNil)
	client := &mockChainClient{
		chain: common.XRPChain,
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 10,
		},
		account: common.Account{Coins: common.NewCoins(common.NewCoin(xrpAsset, cosmos.NewUint(1)))},
	}
	reporter := newSolvencyReporter(client, &thorMockBridge{})
	reporter.setLastSolvencyCheckHeight(101)

	c.Assert(reporter.shouldReportAtHeight(109), Equals, false)
	c.Assert(reporter.shouldReportAtHeight(119), Equals, true)
}

func (s *SolvencySuite) TestShouldReportAtHeightBTCUsesTwoBlockFallback(c *C) {
	client := &mockChainClient{
		chain: common.BTCChain,
		account: common.Account{
			Coins: common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1))),
		},
	}
	reporter := newSolvencyReporter(client, &thorMockBridge{})
	reporter.setLastSolvencyCheckHeight(0)

	c.Assert(reporter.shouldReportAtHeight(1), Equals, false)
	c.Assert(reporter.shouldReportAtHeight(2), Equals, true)
}

func (s *SolvencySuite) TestCollectCanonicalizesDefaultRunnerHeightWithinBucket(c *C) {
	vault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1_000_000_000))),
	}
	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{vault}, nil
	}

	collect := func(height int64) (*ctypes.MsgSolvency, []int64) {
		client := &mockChainClient{
			chain:                 common.ETHChain,
			blockScannerIsHealthy: false,
			gas:                   cosmos.NewUint(10),
			cfg: config.BifrostChainConfiguration{
				SolvencyBlocks: 20,
			},
			account: common.Account{Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))},
		}
		reporter := newSolvencyReporter(client, bridge)
		batch, err := reporter.Collect(fromRunner, height)
		c.Assert(err, IsNil)
		c.Assert(batch, HasLen, 1)
		msg, ok := batch[0].(*ctypes.MsgSolvency)
		c.Assert(ok, Equals, true)
		c.Assert(reporter.getLastSolvencyCheckHeight(), Equals, int64(120))
		return msg, client.vaultAccountHeights
	}

	msgA, queryHeightsA := collect(121)
	msgB, queryHeightsB := collect(139)

	c.Assert(msgA.Height, Equals, int64(120))
	c.Assert(msgB.Height, Equals, int64(120))
	c.Assert(msgA.Id.Equals(msgB.Id), Equals, true)
	c.Assert(queryHeightsA, DeepEquals, []int64{120})
	c.Assert(queryHeightsB, DeepEquals, []int64{120})
}

func (s *SolvencySuite) TestReportHeightKeepsTronLegacyGrid(c *C) {
	client := &mockChainClient{
		chain: common.TRONChain,
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 10,
		},
		account: common.Account{Coins: common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(1)))},
	}
	reporter := newSolvencyReporter(client, &thorMockBridge{})

	height, ok := reporter.reportHeight(120)
	c.Assert(ok, Equals, true)
	c.Assert(height, Equals, int64(101))

	height, ok = reporter.reportHeight(125)
	c.Assert(ok, Equals, true)
	c.Assert(height, Equals, int64(101))

	reporter.setLastSolvencyCheckHeight(101)
	c.Assert(reporter.shouldReportAtHeight(129), Equals, false)
	c.Assert(reporter.shouldReportAtHeight(130), Equals, true)
}

func (s *SolvencySuite) TestReportHeightUsesConfiguredSolanaCadence(c *C) {
	client := &mockChainClient{
		chain: common.SOLChain,
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 100,
		},
		account: common.Account{Coins: common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(1)))},
	}
	reporter := newSolvencyReporter(client, &thorMockBridge{})

	_, ok := reporter.reportHeight(100)
	c.Assert(ok, Equals, false)

	height, ok := reporter.reportHeight(101)
	c.Assert(ok, Equals, true)
	c.Assert(height, Equals, int64(100))

	height, ok = reporter.reportHeight(199)
	c.Assert(ok, Equals, true)
	c.Assert(height, Equals, int64(100))

	height, ok = reporter.reportHeight(200)
	c.Assert(ok, Equals, true)
	c.Assert(height, Equals, int64(100))

	height, ok = reporter.reportHeight(201)
	c.Assert(ok, Equals, true)
	c.Assert(height, Equals, int64(200))
}

func (s *SolvencySuite) TestReportHeightBoundaryStraddleConvergesNextBucket(c *C) {
	client := &mockChainClient{
		chain: common.ETHChain,
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 20,
		},
		account: common.Account{Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))},
	}
	reporter := newSolvencyReporter(client, &thorMockBridge{})

	height, ok := reporter.reportHeight(139)
	c.Assert(ok, Equals, true)
	c.Assert(height, Equals, int64(120))

	height, ok = reporter.reportHeight(141)
	c.Assert(ok, Equals, true)
	c.Assert(height, Equals, int64(140))

	height, ok = reporter.reportHeight(159)
	c.Assert(ok, Equals, true)
	c.Assert(height, Equals, int64(140))
}

func (s *SolvencySuite) TestShouldReportRequiresNonZeroGas(c *C) {
	gaiaAsset, err := common.NewAsset("gaia.atom")
	c.Assert(err, IsNil)

	testCases := []struct {
		name  string
		chain common.Chain
		asset common.Asset
	}{
		{name: "gaia", chain: common.GAIAChain, asset: gaiaAsset},
		{name: "sol", chain: common.SOLChain, asset: common.SOLAsset},
	}

	for _, tc := range testCases {
		client := &mockChainClient{
			chain:                 tc.chain,
			blockScannerIsHealthy: true,
			gas:                   cosmos.NewUint(0),
			cfg: config.BifrostChainConfiguration{
				SolvencyBlocks: 10,
			},
			account:            common.Account{Coins: common.NewCoins(common.NewCoin(tc.asset, cosmos.NewUint(1)))},
			chainHeight:        20,
			blockScannerHeight: 20,
		}

		reporter := newSolvencyReporter(client, &thorMockBridge{})
		reporter.setLastSolvencyCheckHeight(0)

		c.Assert(reporter.ShouldReport(fromBlockScanner, 20), Equals, false, Commentf(tc.name))
	}
}

func (s *SolvencySuite) TestShouldReportRejectsNonPositiveHeight(c *C) {
	client := &mockChainClient{
		chain:                 common.ETHChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 20,
		},
		account: common.Account{Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))},
	}
	reporter := newSolvencyReporter(client, &thorMockBridge{})
	reporter.setLastSolvencyCheckHeight(0)

	c.Assert(reporter.ShouldReport(fromBlockScanner, 0), Equals, false)
	c.Assert(reporter.ShouldReport(fromRunner, -1), Equals, false)
}

// TestShouldReportSkipsTronHeightsBeforeFinalityFloor verifies the added TRON
// low-height guard prevents non-positive solvency message heights.
func (s *SolvencySuite) TestShouldReportSkipsTronHeightsBeforeFinalityFloor(c *C) {
	client := &mockChainClient{
		chain:                 common.TRONChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 10,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 100,
			},
		},
		account:     common.Account{Coins: common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(1)))},
		chainHeight: 20,
	}

	reporter := newSolvencyReporter(client, &thorMockBridge{})
	c.Assert(reporter.ShouldReport(fromBlockScanner, 10), Equals, false)
	c.Assert(reporter.ShouldReport(fromBlockScanner, 20), Equals, true)
}

// TestCollectAdvancesCadenceOnNonDeferredPartialFailure verifies a persistent
// non-deferred vault lookup failure does not force every subsequent eligible
// block to recheck the healthy vaults.
func (s *SolvencySuite) TestCollectAdvancesCadenceOnNonDeferredPartialFailure(c *C) {
	solventVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000_000))),
	}
	failingVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(2_000_000))),
	}

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{solventVault, failingVault}, nil
	}

	client := &mockChainClient{
		chain:                 common.BTCChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 10,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 10,
			},
		},
		accounts: map[string]common.Account{
			solventVault.PubKey.String(): {Coins: solventVault.Coins},
		},
		accountErrors: map[string]error{
			failingVault.PubKey.String(): fmt.Errorf("boom"),
		},
		chainHeight: 30,
	}

	reporter := newSolvencyReporter(client, bridge)
	batch, err := reporter.Collect(fromBlockScanner, 20)
	c.Assert(err, IsNil)
	c.Assert(batch, HasLen, 0)
	c.Assert(reporter.getLastSolvencyCheckHeight(), Equals, int64(20))
	c.Assert(reporter.ShouldReport(fromBlockScanner, 21), Equals, false)
	c.Assert(reporter.ShouldReport(fromBlockScanner, 30), Equals, true)
}

// TestCollectDoesNotAdvanceCadenceWhenAllVaultsFail verifies a full lookup
// failure still retries on the next eligible trigger because no vault state was
// checked successfully.
func (s *SolvencySuite) TestCollectDoesNotAdvanceCadenceWhenAllVaultsFail(c *C) {
	failingVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(2_000_000))),
	}

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{failingVault}, nil
	}

	client := &mockChainClient{
		chain:                 common.BTCChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 10,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 10,
			},
		},
		accountErrors: map[string]error{
			failingVault.PubKey.String(): fmt.Errorf("boom"),
		},
		chainHeight: 30,
	}

	reporter := newSolvencyReporter(client, bridge)
	batch, err := reporter.Collect(fromBlockScanner, 20)
	c.Assert(err, IsNil)
	c.Assert(batch, HasLen, 0)
	c.Assert(reporter.getLastSolvencyCheckHeight(), Equals, int64(0))
	c.Assert(reporter.ShouldReport(fromBlockScanner, 21), Equals, true)
}

func (s *SolvencySuite) TestCollectDefersOnlyPendingReconciliationVault(c *C) {
	deferredVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000_000_000))),
	}
	deferredVault.PubKeyFrost = deferredVault.PubKey
	insolventVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(2_000_000_000))),
	}
	insolventVault.PubKeyFrost = insolventVault.PubKey

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{deferredVault, insolventVault}, nil
	}

	client := &mockChainClient{
		chain:                 common.XMRChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 10,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 10,
			},
		},
		accounts: map[string]common.Account{
			insolventVault.PubKey.String(): {
				Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
			},
		},
		accountErrors: map[string]error{
			deferredVault.PubKey.String(): solvencyDeferredTestError{},
		},
		chainHeight: 30,
	}

	reporter := newSolvencyReporter(client, bridge)
	batch, err := reporter.Collect(fromBlockScanner, 20)
	c.Assert(err, IsNil)
	c.Assert(batch, HasLen, 1)
	c.Assert(reporter.getLastSolvencyCheckHeight(), Equals, int64(0))
	c.Assert(reporter.getLastSolvencyAttemptHeight(), Equals, int64(20))

	msg, ok := batch[0].(*ctypes.MsgSolvency)
	c.Assert(ok, Equals, true)
	c.Assert(msg.PubKey.Equals(insolventVault.PubKey), Equals, true)
	delete(client.accountErrors, deferredVault.PubKey.String())
	// Even after reconciliation succeeds, the incomplete round remains
	// ineligible within the same canonical bucket. Recovery intentionally waits
	// until the next cadence bucket so the insolvent sibling is not recreated and
	// gossiped on every runner tick.
	c.Assert(reporter.ShouldReport(fromBlockScanner, 29), Equals, false)
	c.Assert(reporter.ShouldReport(fromBlockScanner, 30), Equals, true)
}

func (s *SolvencySuite) TestCollectPreservesPerVaultDeferralTracking(c *C) {
	deferredVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000_000_000))),
	}
	deferredVault.PubKeyFrost = deferredVault.PubKey
	solventVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(2_000_000_000))),
	}
	solventVault.PubKeyFrost = solventVault.PubKey

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{deferredVault, solventVault}, nil
	}

	baseClient := &mockChainClient{
		chain:                 common.XMRChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 10,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 10,
			},
		},
		accounts: map[string]common.Account{
			deferredVault.PubKey.String(): {Coins: deferredVault.Coins},
			solventVault.PubKey.String():  {Coins: solventVault.Coins},
		},
		accountErrors: map[string]error{
			deferredVault.PubKey.String(): solvencyDeferredTestError{},
		},
		chainHeight: 80,
	}
	reporter := newSolvencyReporter(baseClient, bridge)
	var logBuf bytes.Buffer
	reporter.logger = zerolog.New(&logBuf)
	deferralRounds := func(pubKey common.PubKey) int {
		reporter.solvencyDeferralMu.Lock()
		defer reporter.solvencyDeferralMu.Unlock()
		return reporter.solvencyDeferralRounds[pubKey]
	}

	for i := 1; i <= xmrSolvencyDeferralEscalationRounds; i++ {
		logBuf.Reset()
		height := int64(i * 10)
		batch, err := reporter.Collect(fromBlockScanner, height)
		c.Assert(err, IsNil)
		c.Assert(batch, HasLen, 0)
		c.Assert(deferralRounds(deferredVault.PubKey), Equals, i)
		c.Assert(deferralRounds(solventVault.PubKey), Equals, 0)
		c.Assert(strings.Contains(logBuf.String(), `"consecutive_rounds":`+fmt.Sprint(i)), Equals, true)
		c.Assert(strings.Contains(logBuf.String(), `"last_complete_height":0`), Equals, true)
		c.Assert(strings.Contains(logBuf.String(), `"level":"error"`), Equals, i >= xmrSolvencyDeferralEscalationRounds)
		c.Assert(strings.Contains(logBuf.String(), `"level":"warn"`), Equals, i < xmrSolvencyDeferralEscalationRounds)
	}

	delete(baseClient.accountErrors, deferredVault.PubKey.String())
	batch, err := reporter.Collect(fromBlockScanner, 70)
	c.Assert(err, IsNil)
	c.Assert(batch, HasLen, 0)
	c.Assert(deferralRounds(deferredVault.PubKey), Equals, 0)
}

func (s *SolvencySuite) TestCollectPrunesDeferralsForChurnedVaults(c *C) {
	deferredVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000_000_000))),
	}
	deferredVault.PubKeyFrost = deferredVault.PubKey
	activeVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(2_000_000_000))),
	}
	activeVault.PubKeyFrost = activeVault.PubKey

	chainVaults := ctypes.Vaults{deferredVault, activeVault}
	bridge := &thorMockBridge{getAsgardsFunc: func() (ctypes.Vaults, error) {
		return chainVaults, nil
	}}
	client := &mockChainClient{
		chain:                 common.XMRChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 10,
		},
		accounts: map[string]common.Account{
			activeVault.PubKey.String(): {Coins: activeVault.Coins},
		},
		accountErrors: map[string]error{
			deferredVault.PubKey.String(): solvencyDeferredTestError{},
		},
	}
	reporter := newSolvencyReporter(client, bridge)

	_, err := reporter.Collect(fromBlockScanner, 10)
	c.Assert(err, IsNil)
	reporter.solvencyDeferralMu.Lock()
	c.Assert(reporter.solvencyDeferralRounds[deferredVault.PubKey], Equals, 1)
	reporter.solvencyDeferralMu.Unlock()

	// An empty chain-vault response is incomplete rather than authoritative and
	// must not reset a streak that is close to escalation.
	chainVaults = nil
	_, err = reporter.Collect(fromBlockScanner, 20)
	c.Assert(err, IsNil)
	reporter.solvencyDeferralMu.Lock()
	c.Assert(reporter.solvencyDeferralRounds[deferredVault.PubKey], Equals, 1)
	reporter.solvencyDeferralMu.Unlock()

	// Once a non-empty authoritative set excludes the deferred vault, prune it.
	chainVaults = ctypes.Vaults{activeVault}
	_, err = reporter.Collect(fromBlockScanner, 30)
	c.Assert(err, IsNil)
	reporter.solvencyDeferralMu.Lock()
	_, retained := reporter.solvencyDeferralRounds[deferredVault.PubKey]
	reporter.solvencyDeferralMu.Unlock()
	c.Assert(retained, Equals, false)
}

func (s *SolvencySuite) TestSolvencyDeferralEscalationRoundsAreChainSpecific(c *C) {
	xmrReporter := newSolvencyReporter(&mockChainClient{chain: common.XMRChain}, &thorMockBridge{})
	c.Assert(xmrReporter.solvencyDeferralEscalationRounds(), Equals, xmrSolvencyDeferralEscalationRounds)

	btcReporter := newSolvencyReporter(&mockChainClient{chain: common.BTCChain}, &thorMockBridge{})
	c.Assert(btcReporter.solvencyDeferralEscalationRounds(), Equals, 0)
}

func (s *SolvencySuite) TestCollectDoesNotAutoUnhaltWithPendingReconciliation(c *C) {
	deferredVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000_000_000))),
	}
	deferredVault.PubKeyFrost = deferredVault.PubKey
	solventVault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(2_000_000_000))),
	}
	solventVault.PubKeyFrost = solventVault.PubKey

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{deferredVault, solventVault}, nil
	}

	client := &mockChainClient{
		chain:                 common.XMRChain,
		blockScannerIsHealthy: false,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 1,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 10,
			},
		},
		accounts: map[string]common.Account{
			solventVault.PubKey.String(): {Coins: solventVault.Coins},
		},
		accountErrors: map[string]error{
			deferredVault.PubKey.String(): solvencyDeferredTestError{},
		},
		chainHeight: 3,
	}

	reporter := newSolvencyReporter(client, bridge)
	batch, err := reporter.Collect(fromRunner, 2)
	c.Assert(err, IsNil)
	c.Assert(batch, HasLen, 0)
	c.Assert(reporter.getLastSolvencyCheckHeight(), Equals, int64(0))
}

// TestCollectDoesNotAdvanceCadenceOnEmptyAsgards verifies an empty Asgard list
// does not consume the next cadence window for height-gated chains.
func (s *SolvencySuite) TestCollectDoesNotAdvanceCadenceOnEmptyAsgards(c *C) {
	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{}, nil
	}

	client := &mockChainClient{
		chain:                 common.BTCChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 1,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 10,
			},
		},
		chainHeight: 3,
	}

	reporter := newSolvencyReporter(client, bridge)
	batch, err := reporter.Collect(fromBlockScanner, 2)
	c.Assert(err, IsNil)
	c.Assert(batch, HasLen, 0)
	c.Assert(reporter.getLastSolvencyCheckHeight(), Equals, int64(0))
	c.Assert(reporter.ShouldReport(fromBlockScanner, 3), Equals, true)
}

func (s *SolvencySuite) TestCollectUsesAlgoPubKeyForSolanaLookup(c *C) {
	secpPubKey := ctypes.GetRandomPubKey()
	eddsaPubKey := ctypes.GetRandomEd25519PubKey()
	vault := ctypes.Vault{
		PubKey:      secpPubKey,
		PubKeyEddsa: eddsaPubKey,
		Coins:       common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(1_000_000_000))),
	}

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{vault}, nil
	}

	client := &mockChainClient{
		chain:                 common.SOLChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 50,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 40,
			},
		},
		// Fallback account is intentionally insolvent; if the reporter wrongly uses secp key
		// for SOL lookup, this account is selected and a report is generated.
		account: common.Account{
			Coins: common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(1))),
		},
		accounts: map[string]common.Account{
			eddsaPubKey.String(): {
				Coins: common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(1_000_000_000))),
			},
		},
		chainHeight:        100,
		blockScannerHeight: 100,
	}

	reporter := newSolvencyReporter(client, bridge)
	batch, err := reporter.Collect(fromBlockScanner, client.blockScannerHeight)
	c.Assert(err, IsNil)
	// Scanner is healthy and SOL account looked up via ed25519 key is solvent,
	// so no message should be emitted.
	c.Assert(batch, HasLen, 0)
}

func (s *SolvencySuite) TestDispatcherUsesLegacyTronCadenceAndMsgHeight(c *C) {
	vault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(1_000_000_000))),
	}

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{vault}, nil
	}

	client := &mockChainClient{
		chain:                 common.TRONChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 10,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 100,
			},
		},
		// Insolvent account to guarantee a report would be emitted if cadence allowed it.
		account: common.Account{
			Coins: common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(1))),
		},
		chainHeight: 30,
	}

	d := &dispatcher{
		logger:      log.Logger,
		reportQueue: make(chan ReportBatch, 1),
		stopper:     make(chan struct{}),
		chains:      map[common.Chain]ChainClient{common.TRONChain: client},
		reporters: map[common.Chain]Reporters{
			common.TRONChain: {newSolvencyReporter(client, bridge)},
		},
	}

	resolved, err := d.resolveTaskHeight(common.TRONChain, fromBlockScanner, 30)
	c.Assert(err, IsNil)
	c.Assert(resolved, Equals, int64(30))

	d.processInternal(common.TRONChain, fromBlockScanner, 30)
	c.Assert(len(d.reportQueue), Equals, 1)

	batch := <-d.reportQueue
	c.Assert(batch, HasLen, 1)
	msg, ok := batch[0].(*ctypes.MsgSolvency)
	c.Assert(ok, Equals, true)
	c.Assert(msg.Height, Equals, int64(11))
}

func (s *SolvencySuite) TestDispatcherUsesConfiguredSolanaMsgHeightFloor(c *C) {
	vault := ctypes.Vault{
		PubKey:      ctypes.GetRandomPubKey(),
		PubKeyEddsa: ctypes.GetRandomEd25519PubKey(),
		Coins:       common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(1_000_000_000))),
	}

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{vault}, nil
	}

	client := &mockChainClient{
		chain:                 common.SOLChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 100,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 100,
			},
		},
		// Insolvent account to force solvency message emission.
		account: common.Account{
			Coins: common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(1))),
		},
		chainHeight: 130,
	}

	d := &dispatcher{
		logger:      log.Logger,
		reportQueue: make(chan ReportBatch, 1),
		stopper:     make(chan struct{}),
		chains:      map[common.Chain]ChainClient{common.SOLChain: client},
		reporters: map[common.Chain]Reporters{
			common.SOLChain: {newSolvencyReporter(client, bridge)},
		},
	}

	resolved, err := d.resolveTaskHeight(common.SOLChain, fromBlockScanner, 111)
	c.Assert(err, IsNil)
	c.Assert(resolved, Equals, int64(111))

	d.processInternal(common.SOLChain, fromBlockScanner, 111)
	c.Assert(len(d.reportQueue), Equals, 1)

	batch := <-d.reportQueue
	c.Assert(batch, HasLen, 1)
	msg, ok := batch[0].(*ctypes.MsgSolvency)
	c.Assert(ok, Equals, true)
	c.Assert(msg.Height, Equals, int64(100))
}

func (s *SolvencySuite) TestSendCollectedToThorchainContinuesWhenNodeStatusUnavailable(c *C) {
	bridge := &thorMockBridge{
		fetchNodeStatusFunc: func() (ctypes.NodeStatus, error) {
			return ctypes.NodeStatus_Unknown, fmt.Errorf("status unavailable")
		},
	}
	attester := &mockSolvencyAttester{}
	d := &dispatcher{
		ctx:              context.Background(),
		logger:           log.Logger,
		thorchainBridge:  bridge,
		solvencyAttester: attester,
	}

	msg := bridge.GetSolvencyMsg(123, common.BCHChain, ctypes.GetRandomPubKey(), common.NewCoins(common.NewCoin(common.BCHAsset, cosmos.NewUint(42))))
	d.sendCollectedToThorchain(ReportBatch{msg})

	attested := attester.getMsgs()
	c.Assert(attested, HasLen, 1)
	c.Assert(attested[0].Chain.Equals(common.BCHChain), Equals, true)
	c.Assert(attested[0].Height, Equals, int64(123))
	c.Assert(attested[0].Id.IsEmpty(), Equals, true)
	c.Assert(bridge.getMsgs(), HasLen, 0)
}

func (s *SolvencySuite) TestSendMsgTimesOutBlockedAttestation(c *C) {
	origBlockTime := constants.ThorchainBlockTime
	defer func() { constants.ThorchainBlockTime = origBlockTime }()
	constants.ThorchainBlockTime = 25 * time.Millisecond

	bridge := &thorMockBridge{}
	attester := newBlockingSolvencyAttester()
	d := &dispatcher{
		ctx:              context.Background(),
		solvencyAttester: attester,
	}

	msg := bridge.GetSolvencyMsg(123, common.BCHChain, ctypes.GetRandomPubKey(), common.NewCoins(common.NewCoin(common.BCHAsset, cosmos.NewUint(42))))
	err := d.sendMsg(msg)
	c.Assert(err, Equals, context.DeadlineExceeded)
	c.Assert(<-attester.done, Equals, context.DeadlineExceeded)
}

func (s *SolvencySuite) TestDispatcherShutdownCancelsBlockedAttestation(c *C) {
	origBlockTime := constants.ThorchainBlockTime
	defer func() { constants.ThorchainBlockTime = origBlockTime }()
	constants.ThorchainBlockTime = 2 * time.Second

	stopper := make(chan struct{})
	wg := &sync.WaitGroup{}
	attester := newBlockingSolvencyAttester()
	bridge := &thorMockBridge{}

	disp, ok := NewDispatcher(nil, bridge, attester, nil, stopper, wg).(*dispatcher)
	c.Assert(ok, Equals, true)

	stopped := false
	defer func() {
		if !stopped {
			close(stopper)
		}
		wg.Wait()
	}()

	msg := bridge.GetSolvencyMsg(123, common.BCHChain, ctypes.GetRandomPubKey(), common.NewCoins(common.NewCoin(common.BCHAsset, cosmos.NewUint(42))))
	disp.reportQueue <- ReportBatch{msg}

	select {
	case <-attester.started:
	case <-time.After(time.Second):
		c.Fatal("report dispatcher did not start the solvency attestation")
	}

	close(stopper)
	stopped = true

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		c.Fatal("report dispatcher did not stop after its context was cancelled")
	}
	c.Assert(<-attester.done, Equals, context.Canceled)
}

func (s *SolvencySuite) TestSendCollectedToThorchainRequiresSolvencyAttester(c *C) {
	var logBuf bytes.Buffer

	bridge := &thorMockBridge{}
	errCounter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "report_failures_total",
		Help: "test counter",
	}, []string{"type", "chain"})
	d := &dispatcher{
		logger:     zerolog.New(&logBuf),
		errCounter: errCounter,
	}

	msg := bridge.GetSolvencyMsg(123, common.BCHChain, ctypes.GetRandomPubKey(), common.NewCoins(common.NewCoin(common.BCHAsset, cosmos.NewUint(42))))
	d.sendCollectedToThorchain(ReportBatch{msg})
	c.Assert(bridge.getMsgs(), HasLen, 0)
	c.Assert(logBuf.String(), Matches, `(?s).*missing solvency attester for report message.*fail to attest solvency.*`)
	c.Assert(testutil.ToFloat64(errCounter.WithLabelValues("fail_to_broadcast_solvency", "")), Equals, float64(1))
}

// TestNewDispatcherBuffersReportQueuePerChain verifies the queue is small but
// intentionally buffered relative to the number of configured chains.
func (s *SolvencySuite) TestNewDispatcherBuffersReportQueuePerChain(c *C) {
	stopper := make(chan struct{})
	wg := &sync.WaitGroup{}
	chains := map[common.Chain]ChainClient{
		common.ETHChain: &mockChainClient{chain: common.ETHChain},
		common.BTCChain: &mockChainClient{chain: common.BTCChain},
	}

	disp, ok := NewDispatcher(chains, &thorMockBridge{}, nil, nil, stopper, wg).(*dispatcher)
	c.Assert(ok, Equals, true)
	c.Assert(cap(disp.reportQueue), Equals, len(chains))

	close(stopper)
	wg.Wait()
}

// TestProcessInternalReturnsOnStopWhenReportQueueBackedUp verifies shutdown
// unblocks a producer even if the report queue is currently full.
func (s *SolvencySuite) TestProcessInternalReturnsOnStopWhenReportQueueBackedUp(c *C) {
	origBlockTime := constants.ThorchainBlockTime
	defer func() { constants.ThorchainBlockTime = origBlockTime }()
	constants.ThorchainBlockTime = 200 * time.Millisecond

	vault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1_000_000_000))),
	}

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{vault}, nil
	}

	client := &mockChainClient{
		chain:                 common.ETHChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 20,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 100,
			},
		},
		account: common.Account{
			Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1))),
		},
		chainHeight: 100,
	}

	stopper := make(chan struct{})
	d := &dispatcher{
		logger:      log.Logger,
		reportQueue: make(chan ReportBatch, 1),
		stopper:     stopper,
		chains:      map[common.Chain]ChainClient{common.ETHChain: client},
		reporters: map[common.Chain]Reporters{
			common.ETHChain: {newSolvencyReporter(client, bridge)},
		},
	}

	d.processInternal(common.ETHChain, fromBlockScanner, 100)
	c.Assert(len(d.reportQueue), Equals, 1)

	done := make(chan struct{})
	go func() {
		d.processInternal(common.ETHChain, fromBlockScanner, 120)
		close(done)
	}()

	time.Sleep(constants.ThorchainBlockTime / 10)
	close(stopper)

	select {
	case <-done:
	case <-time.After(constants.ThorchainBlockTime / 2):
		c.Fatal("processInternal did not unblock after stopper closed")
	}
}

func (s *SolvencySuite) TestProcessInternalIncrementsErrCounterOnReportQueueTimeout(c *C) {
	origBlockTime := constants.ThorchainBlockTime
	defer func() { constants.ThorchainBlockTime = origBlockTime }()
	constants.ThorchainBlockTime = 20 * time.Millisecond

	vault := ctypes.Vault{
		PubKey: ctypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1_000_000_000))),
	}

	bridge := &thorMockBridge{}
	bridge.getAsgardsFunc = func() (ctypes.Vaults, error) {
		return ctypes.Vaults{vault}, nil
	}

	client := &mockChainClient{
		chain:                 common.ETHChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(10),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 20,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ObservationFlexibilityBlocks: 100,
			},
		},
		account: common.Account{
			Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1))),
		},
		chainHeight: 100,
	}

	errCounter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reporter_dispatcher_test_errors_total",
		Help: "test counter",
	}, []string{"error_name", "additional"})

	d := &dispatcher{
		logger:      log.Logger,
		reportQueue: make(chan ReportBatch, 1),
		stopper:     make(chan struct{}),
		errCounter:  errCounter,
		chains:      map[common.Chain]ChainClient{common.ETHChain: client},
		reporters: map[common.Chain]Reporters{
			common.ETHChain: {newSolvencyReporter(client, bridge)},
		},
	}

	d.processInternal(common.ETHChain, fromBlockScanner, 100)
	c.Assert(len(d.reportQueue), Equals, 1)

	d.processInternal(common.ETHChain, fromBlockScanner, 120)

	metric, err := errCounter.GetMetricWithLabelValues("fail_to_enqueue_report_batch", "")
	c.Assert(err, IsNil)
	c.Assert(testutil.ToFloat64(metric), Equals, float64(1))
}
