package blockscanner

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type rollbackWindowBridge struct {
	thorclient.ThorchainBridge
	mimirs    map[string]int64
	mimirErrs map[string]error
}

func (rollbackWindowBridge) GetLastObservedInHeight(common.Chain) (int64, error) {
	return 100, nil
}

func (rollbackWindowBridge) GetConstants() (map[string]int64, error) {
	return map[string]int64{
		constants.ObservationDelayFlexibility.String(): 100,
		constants.ThorchainBlockTime.String():          int64(6 * time.Second),
	}, nil
}

func (b rollbackWindowBridge) GetMimir(key string) (int64, error) {
	value, found := b.mimirs[key]
	if !found {
		value = -1
	}
	return value, b.mimirErrs[key]
}

func (b rollbackWindowBridge) GetMimirWithRef(template, ref string) (int64, error) {
	return b.GetMimir(fmt.Sprintf(template, ref))
}

func TestRollbackObservationWindow(t *testing.T) {
	key := constants.ObservationDelayFlexibility.String()
	dashKey := key + common.DASHChain.String()
	maxKey := fmt.Sprintf(constants.MimirTemplateMaxConfirmations, common.DASHChain)
	lookupErr := errors.New("mimir unavailable")
	// The constant is 100 THORChain blocks: four DASH blocks or 50 ETH blocks.
	for _, tc := range []struct {
		name      string
		chain     common.Chain
		mimirs    map[string]int64
		mimirErrs map[string]error
		want      int64
	}{
		{
			name: "larger chain override", chain: common.DASHChain,
			mimirs: map[string]int64{dashKey: 200, key: 50}, want: 92,
		},
		{
			name: "smaller chain override", chain: common.DASHChain,
			mimirs: map[string]int64{dashKey: 50, key: 200}, want: 98,
		},
		{
			name: "missing chain override", chain: common.DASHChain,
			mimirs: map[string]int64{key: 50}, want: 98,
		},
		{
			name: "negative chain override", chain: common.DASHChain,
			mimirs: map[string]int64{dashKey: -2, key: 50}, want: 98,
		},
		{
			name: "chain lookup error", chain: common.DASHChain,
			mimirs:    map[string]int64{dashKey: 200, key: 50},
			mimirErrs: map[string]error{dashKey: lookupErr}, want: 98,
		},
		{
			name: "missing global setting", chain: common.DASHChain, want: 96,
		},
		{
			name: "negative global setting", chain: common.DASHChain,
			mimirs: map[string]int64{key: -2}, want: 96,
		},
		{
			name: "global lookup error", chain: common.DASHChain,
			mimirs:    map[string]int64{key: 50},
			mimirErrs: map[string]error{key: lookupErr}, want: 96,
		},
		{
			name: "both lookups fail", chain: common.DASHChain,
			mimirErrs: map[string]error{dashKey: lookupErr, key: lookupErr}, want: 96,
		},
		{
			name: "zero chain override", chain: common.DASHChain,
			mimirs: map[string]int64{dashKey: 0, key: 200}, want: 99,
		},
		{
			name: "zero global setting", chain: common.DASHChain,
			mimirs: map[string]int64{key: 0}, want: 99,
		},
		{
			name: "chain override is isolated", chain: common.ETHChain,
			mimirs: map[string]int64{dashKey: 200, key: 50}, want: 75,
		},
		{
			name: "other chains accept overrides", chain: common.ETHChain,
			mimirs: map[string]int64{key + common.ETHChain.String(): 20, key: 50}, want: 90,
		},
		{
			name: "conversion rounds down", chain: common.DASHChain,
			mimirs: map[string]int64{dashKey: 124}, want: 96,
		},
		{
			name: "minimum one block", chain: common.DASHChain,
			mimirs: map[string]int64{dashKey: 1}, want: 99,
		},
		{
			name: "max confirmations wins", chain: common.DASHChain,
			mimirs: map[string]int64{dashKey: 200, maxKey: 10}, want: 90,
		},
		{
			name: "window exceeds max confirmations", chain: common.DASHChain,
			mimirs: map[string]int64{dashKey: 200, maxKey: 2}, want: 92,
		},
		{
			name: "rollback stops at zero", chain: common.DASHChain,
			mimirs: map[string]int64{dashKey: 5000}, want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scanner := &BlockScanner{
				cfg: config.BifrostBlockScannerConfiguration{ChainID: tc.chain},
				thorchainBridge: rollbackWindowBridge{
					mimirs: tc.mimirs, mimirErrs: tc.mimirErrs,
				},
				rollbackChan: make(chan int64, 1),
				stopChan:     make(chan struct{}),
			}
			err := scanner.RollbackToLastObserved()
			if err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-scanner.rollbackChan:
				if got != tc.want {
					t.Fatalf("rollback height = %d, want %d", got, tc.want)
				}
			default:
				t.Fatal("rollback was not queued")
			}
		})
	}
}
