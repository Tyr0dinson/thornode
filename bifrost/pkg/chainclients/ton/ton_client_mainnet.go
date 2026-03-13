//go:build !mocknet
// +build !mocknet

package ton

import (
	"fmt"

	"github.com/xssnick/tonutils-go/ton"
)

func getMockApi() (ton.APIClientWrapped, error) {
	return nil, fmt.Errorf("mock api not available")
}
