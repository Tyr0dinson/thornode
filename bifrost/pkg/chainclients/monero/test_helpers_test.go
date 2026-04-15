package monero

import (
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func testFrostTxKeys() []string {
	return []string{strings.Repeat("1", 64)}
}

func testVaultPubKey(t *testing.T, _ string) common.PubKey {
	t.Helper()
	return ttypes.GetRandomEd25519PubKey()
}

func testOutboundXMRDestination() common.Address {
	return common.Address(common.XMRChecksummedDummyAddress('8'))
}
