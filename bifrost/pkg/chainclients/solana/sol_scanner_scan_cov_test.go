package solana

import (
	"errors"
	"net/http/httptest"
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// covScanBridge answers the scan loop's halt mimirs and network-fee read.
type covScanBridge struct {
	*covBridge
	feeRate uint64
	feeErr  error
}

func (b *covScanBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) {
	return 1, b.feeRate, b.feeErr
}

type ScanLoopCovSuite struct{}

var _ = Suite(&ScanLoopCovSuite{})

func (s *ScanLoopCovSuite) SetUpSuite(c *C) {
	thorchain.SetupConfigForTest()
}

type scanCovFixture struct {
	scanner   *SOLScanner
	rpc       *covSolRPC
	server    *httptest.Server
	bridge    *covScanBridge
	storage   *blockscanner.BlockScannerStorageSolana
	solvency  []int64
	vaultAddr string
}

func newScanCovFixture(c *C) *scanCovFixture {
	m := newCovSolRPC()
	server := httptest.NewServer(m)
	bridge := &covScanBridge{covBridge: &covBridge{
		mockSOLThorchainBridge: &mockSOLThorchainBridge{},
		mimirs:                 map[string]int64{},
		mimirErr:               map[string]error{},
	}}
	storage, err := blockscanner.NewBlockScannerStorageSolana("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	pkm, err := pubkeymanager.NewPubKeyManager(bridge, nil)
	c.Assert(err, IsNil)
	vaultPK := types2.GetRandomEd25519PubKey()
	pkm.AddPubKey(vaultPK, true, common.SigningAlgoEd25519)
	addr, err := vaultPK.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)

	cfg := getConfigForTest(server.URL)
	cfg.Solana.ScanInterval = time.Millisecond
	cfg.Solana.FeeUpdateInterval = 100
	cfg.Solana.FeeSampleSlots = 10

	f := &scanCovFixture{rpc: m, server: server, bridge: bridge, storage: storage, vaultAddr: addr.String()}
	scanner, err := NewSOLScanner(make(chan struct{}), cfg, storage, bridge, GetMetricForTest(c),
		newSolRPCForServer(server), pkm, func(h int64) error {
			f.solvency = append(f.solvency, h)
			return errors.New("solvency not reported")
		}, newTestSignerCacheManager(c))
	c.Assert(err, IsNil)
	scanner.globalTxsQueue = make(chan stypes.TxIn, 10)
	scanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 10)
	f.scanner = scanner
	return f
}

func covVaultTx(vault string) string {
	return `{"slot":100,"transaction":{"signatures":["txsig1"],"message":{"accountKeys":["` +
		testSender + `","` + vault + `","` + testSystemProgram + `","` + testMemoProgram + `"],"instructions":[` +
		`{"programIdIndex":2,"accounts":[0,1],"data":"` + makeTransferData(1_000_000_000) + `"},` +
		`{"programIdIndex":3,"accounts":[0],"data":"` + makeMemoData("ADD:SOL.SOL") + `"}` +
		`],"recentBlockhash":"bh"}},"meta":{"fee":5000,"preBalances":[100,200],"postBalances":[90,210],"status":{"Ok":null}}}`
}

func (s *ScanLoopCovSuite) TestScanObservesVaultAndAdvances(c *C) {
	f := newScanCovFixture(c)
	defer f.server.Close()
	f.rpc.setResult("getSlot", "250")
	f.rpc.setResult("getSignaturesForAddress", `[{"signature":"txsig1","slot":100}]`)
	f.rpc.setResult("getTransaction", covVaultTx(f.vaultAddr))
	f.rpc.setResult("getBlocks", "[]")
	f.bridge.feeRate = 7
	// a stale mimir check re-reads the halt mimirs (none set: not paused)
	f.scanner.lastMimirCheck = time.Time{}
	f.scanner.netFeeSlotMultiple = 1

	f.scanner.scan()

	c.Assert(f.scanner.isChainPaused, Equals, false)
	c.Assert(f.scanner.IsHealthy(), Equals, true)
	c.Assert(f.scanner.getLastHeight(), Equals, uint64(250))
	c.Assert(f.scanner.CurrentScanSlot(), Equals, int64(250))
	c.Assert(f.solvency, DeepEquals, []int64{250})
	// the fee threshold was crossed: THORChain's rate is cached and the multiple advanced
	c.Assert(f.scanner.lastFeeRate.Load(), Equals, uint64(7))
	c.Assert(f.scanner.netFeeSlotMultiple, Equals, uint64(2))
	c.Assert(f.scanner.vaultScanStatuses[f.vaultAddr], DeepEquals, vaultScanStatus{lastSig: "txsig1", lastSlot: 250})
	_, initialized := f.scanner.initialHeight[f.vaultAddr]
	c.Assert(initialized, Equals, true)
	c.Assert(len(f.scanner.globalTxsQueue), Equals, 1)
	txIn := <-f.scanner.globalTxsQueue
	c.Assert(txIn.TxArray, HasLen, 1)
	c.Assert(txIn.TxArray[0].Tx, Equals, "txsig1")

	// Second pass: no new signatures. The vault keeps its cursor from memory and its
	// slot advances; the initial-height path is no longer used.
	f.rpc.setResult("getSlot", "260")
	f.rpc.setResult("getSignaturesForAddress", `[]`)
	f.bridge.feeErr = errors.New("fee down")
	f.scanner.scan()
	c.Assert(f.scanner.getLastHeight(), Equals, uint64(260))
	c.Assert(f.scanner.vaultScanStatuses[f.vaultAddr], DeepEquals, vaultScanStatus{lastSig: "txsig1", lastSlot: 260})
	c.Assert(f.solvency, DeepEquals, []int64{250, 260})
	c.Assert(f.scanner.lastFeeRate.Load(), Equals, uint64(7))
}

func (s *ScanLoopCovSuite) TestScanFloorAndFailure(c *C) {
	f := newScanCovFixture(c)
	defer f.server.Close()
	f.scanner.lastMimirCheck = time.Now()
	f.scanner.netFeeSlotMultiple = 10 // fee threshold not crossed
	f.scanner.cfg.FloorBlockHeight = 120
	f.scanner.initialHeight[f.vaultAddr] = struct{}{}
	f.rpc.setResult("getSlot", "300")
	f.rpc.setError("getSignaturesForAddress", "history unavailable")

	f.scanner.scan()

	c.Assert(f.scanner.IsHealthy(), Equals, false)
	c.Assert(f.scanner.getLastHeight(), Equals, uint64(300))
	// an unsuccessful vault query does not advance that vault's cursor
	c.Assert(f.scanner.vaultScanStatuses[f.vaultAddr], DeepEquals, vaultScanStatus{})
}

func (s *ScanLoopCovSuite) TestScanSkipsWhenRPCBehind(c *C) {
	f := newScanCovFixture(c)
	defer f.server.Close()
	f.scanner.lastMimirCheck = time.Now()
	f.scanner.netFeeSlotMultiple = 10
	f.scanner.setLastHeight(500)
	f.rpc.setResult("getSlot", "400")

	f.scanner.scan()

	c.Assert(f.scanner.getLastHeight(), Equals, uint64(500))
	c.Assert(f.solvency, HasLen, 0)
	c.Assert(f.rpc.count("getSignaturesForAddress"), Equals, 0)
}

func (s *ScanLoopCovSuite) TestStartStopsAndHonoursFloor(c *C) {
	f := newScanCovFixture(c)
	defer f.server.Close()
	f.rpc.setResult("getSlot", "1234")
	f.rpc.setResult("getBlocks", "[]")
	f.scanner.cfg.FloorBlockHeight = 900
	close(f.scanner.stopChan)

	f.scanner.Start()

	// the floor was applied to the resume cursor and persisted
	c.Assert(f.scanner.getLastHeight(), Equals, uint64(900))
	pos, err := f.storage.GetScanPos()
	c.Assert(err, IsNil)
	c.Assert(pos, Equals, uint64(900))
	c.Assert(f.scanner.netFeeSlotMultiple, Equals, uint64(12))
	c.Assert(f.rpc.count("getBlocks"), Equals, 1)
}

func (s *ScanLoopCovSuite) TestStartStoppedBeforeInitialSlot(c *C) {
	f := newScanCovFixture(c)
	defer f.server.Close()
	f.rpc.setError("getSlot", "down")
	close(f.scanner.stopChan)

	f.scanner.Start()

	c.Assert(f.scanner.getLastHeight(), Equals, uint64(0))
	c.Assert(f.rpc.count("getBlocks"), Equals, 0)
}
