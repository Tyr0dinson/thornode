package reporter

import (
	"fmt"
	"maps"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	. "gopkg.in/check.v1"

	sdk "github.com/cosmos/cosmos-sdk/types"
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

// mockChainClient is a mock that satisfies the chainclients.ChainClient interface
type mockChainClient struct {
	chain                 common.Chain
	cfg                   config.BifrostChainConfiguration
	gas                   cosmos.Uint
	account               common.Account
	accounts              map[string]common.Account
	err                   error
	blockScannerHeight    int64
	blockScannerIsHealthy bool
	chainHeight           int64
}

func (m *mockChainClient) GetChain() common.Chain                      { return m.chain }
func (m *mockChainClient) GetConfig() config.BifrostChainConfiguration { return m.cfg }
func (m *mockChainClient) GetCurrentGas() cosmos.Uint                  { return m.gas }
func (m *mockChainClient) GetAccount(pk common.PubKey, height *big.Int) (common.Account, error) {
	if m.accounts != nil {
		if acct, ok := m.accounts[pk.String()]; ok {
			return acct, m.err
		}
	}
	return m.account, m.err
}
func (m *mockChainClient) GetBlockScannerHeight() (int64, error) { return m.blockScannerHeight, nil }
func (m *mockChainClient) IsBlockScannerHealthy() bool           { return m.blockScannerIsHealthy }
func (m *mockChainClient) Start(globalTxsQueue chan types.TxIn, globalErrataQueue chan types.ErrataBlock, reportDispatcher Dispatcher) {
}
func (m *mockChainClient) Stop()                                                 {}
func (m *mockChainClient) GetHeight() (int64, error)                             { return m.chainHeight, nil }
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
	lowerKey := strings.ToUpper(key)
	mm.mimir[lowerKey] = value
}

func (mm *mimirMap) lookup(key string) (int64, bool) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	lowerKey := strings.ToUpper(key)
	value, ok := mm.mimir[lowerKey]
	return value, ok
}

func (mm *mimirMap) get(key string) int64 {
	value, ok := mm.lookup(key)
	if !ok {
		return -1
	}
	return value
}

func (mm *mimirMap) items() map[string]int64 {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	copyMimir := make(map[string]int64, len(mm.mimir))
	maps.Copy(copyMimir, mm.mimir)
	return copyMimir
}

func (mm *mimirMap) clear() {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mm.mimir = map[string]int64{}
}

func (mm *mimirMap) setZeros() {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	zeros := make(map[string]int64, len(mm.mimir))
	for key := range mm.mimir {
		zeros[key] = 0
	}
	mm.mimir = zeros
}

func (mm *mimirMap) setFull(full map[string]int64) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mm.mimir = map[string]int64{}
	for k, v := range full {
		mm.mimir[strings.ToUpper(k)] = v
	}
}

// mockThorchainBridge is a mock for the bridge
type thorMockBridge struct {
	thorclient.ThorchainBridge
	getAsgardsFunc func() (ctypes.Vaults, error)
	msgs           []ctypes.MsgSolvency
	mimir          mimirMap
	mu             sync.Mutex
}

func (m *thorMockBridge) GetAsgards() (ctypes.Vaults, error) {
	return m.getAsgardsFunc()
}

func (m *thorMockBridge) FetchNodeStatus() (ctypes.NodeStatus, error) {
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

// ------- Tests
func (s *SolvencySuite) SetUpSuite(c *C) {
	ctypes.SetupConfigForTest()
}

func (s *SolvencySuite) TestReportSolvencyReporter(c *C) {
	// speed up Thorchain for quick tests
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
			name:                    "2. skip report when height is less than the default SolvencyBlocks value (1000)",
			height:                  959,
			shouldReport:            false,
			client:                  stdMockClient,
			mockSetup:               stdMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: false,
			expectReportFromRunner:  false,
		},
		{
			name:                    "2a. - Insolvent Vault - skip report when height is less than the default SolvencyBlocks value (1000)",
			height:                  959,
			shouldReport:            false,
			client:                  insolventStdMockClient, // <<-- vault is insolvent
			mockSetup:               stdMockBridgeSetup,
			expectedError:           "",
			expectReportFromCollect: false, // scanner won't report at this height
			expectReportFromRunner:  false, // runner shouldn't report at this height regardless of the vault state
		},
		{
			name:         "3. should report when height is a multiple of the configured SolvencyBlocks value (123) for EVM chains",
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
			name:         "3a. - Insolvent Vault - should report when height is a multiple of the configured SolvencyBlocks value (123) for EVM chains",
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
			name:         "4. skip report when height is not a multiple of the configured SolvencyBlocks value (1500) for EVM chains",
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
		fmt.Printf("Test: %s", tc.name)
		// setup environment
		mockBridge := &thorMockBridge{}
		tc.mockSetup(mockBridge)

		// helper functions
		verifyReport := func(from triggeredFrom, msg ctypes.MsgSolvency) {
			var expectedHeight int64
			if from == fromBlockScanner {
				expectedHeight = tc.client.blockScannerHeight
			} else {
				expectedHeight = tc.client.chainHeight
			}
			c.Assert(msg.Height, Equals, expectedHeight)
			c.Assert(msg.Chain, Equals, tc.client.GetChain())
			c.Assert(msg.PubKey.Equals(asgards[0].PubKey), Equals, true)
			c.Assert(msg.Coins, HasLen, 1)
			c.Assert(msg.Coins[0].Asset.Equals(tc.client.account.Coins[0].Asset), Equals, true)
			c.Assert(msg.Coins[0].Amount.Equal(tc.client.account.Coins[0].Amount), Equals, true)
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
		verifyResults := func(from triggeredFrom, expectReport bool) {
			// wait for message to arrive
			for range 20 {
				time.Sleep(constants.ThorchainBlockTime / 15)
				if mockBridge.msgs != nil {
					break
				}
			}
			msgs := mockBridge.getMsgs()
			if expectReport {
				c.Assert(msgs, HasLen, 1)
				msg := msgs[0]
				verifyReport(from, msg)
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
		// set default ObservationFlexibilityBlocks if not set
		if tc.client.cfg.BlockScanner.ObservationFlexibilityBlocks == 0 {
			tc.client.cfg.BlockScanner.ObservationFlexibilityBlocks = 40
		}
		tc.client.blockScannerHeight = tc.height
		reporter := newSolvencyReporter(tc.client, mockBridge)
		reporter.lastSolvencyCheckHeight = 0

		// test ShouldReport
		shouldReport := reporter.ShouldReport(fromBlockScanner, 0)
		c.Assert(shouldReport, Equals, tc.shouldReport, Commentf(tc.name))

		// test Collect
		if shouldReport {
			allMsgs, err := reporter.Collect(fromBlockScanner)
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
			chain := tc.client.GetChain()
			chains := map[common.Chain]ChainClient{chain: tc.client}
			defer func() {
				// stop goroutines
				close(stopper)
			}()

			// prepare dispatcher
			disp := NewDispatcher(chains, mockBridge, nil, stopper)

			// now trigger reporter checks
			disp.Process(chain, 0)

			// verify the solvency message was broadcast
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
				solvencyRep.lastSolvencyCheckHeight = 0 // reset to 0
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
	reporter.lastSolvencyCheckHeight = 0

	batch, err := reporter.Collect(fromRunner)
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
	reporter.lastSolvencyCheckHeight = 0

	batch, err := reporter.Collect(fromRunner)
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
	reporter.lastSolvencyCheckHeight = 0

	c.Assert(reporter.shouldReportAtHeight(100), Equals, false)
	c.Assert(reporter.shouldReportAtHeight(101), Equals, true)
}

func (s *SolvencySuite) TestShouldReportRequiresNonZeroGas(c *C) {
	gaiaAsset, err := common.NewAsset("gaia.atom")
	c.Assert(err, IsNil)
	client := &mockChainClient{
		chain:                 common.GAIAChain,
		blockScannerIsHealthy: true,
		gas:                   cosmos.NewUint(0),
		cfg: config.BifrostChainConfiguration{
			SolvencyBlocks: 10,
		},
		account: common.Account{Coins: common.NewCoins(common.NewCoin(gaiaAsset, cosmos.NewUint(1)))},
	}
	client.chainHeight = 20
	client.blockScannerHeight = 20

	reporter := newSolvencyReporter(client, &thorMockBridge{})
	reporter.lastSolvencyCheckHeight = 0

	c.Assert(reporter.ShouldReport(fromBlockScanner, 20), Equals, false)
}
