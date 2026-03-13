//go:build mocknet
// +build mocknet

package ton

import (
	"github.com/xssnick/tonutils-go/ton"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/ton/mock"
)

func getMockApi() (ton.APIClientWrapped, error) {
	return mock.NewMockApiClient(), nil
}
