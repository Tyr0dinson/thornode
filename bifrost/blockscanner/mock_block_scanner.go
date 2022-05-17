package blockscanner

import (
	"gitlab.com/thorchain/thornode/bifrost/thorclient/types"
)

type MockBlockScanner struct {
	Healthy bool
}

func (b *MockBlockScanner) Start(globalTxsQueue chan types.TxIn) {

}
func (b *MockBlockScanner) Stop() {

}
func (b *MockBlockScanner) IsHealthy() bool {
	return b.Healthy
}
