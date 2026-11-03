package ethereum

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/blocktime"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type asgardCountingBridge struct {
	thorclient.ThorchainBridge
	calls int
}

func (b *asgardCountingBridge) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	b.calls++
	return nil, errors.New("unavailable")
}

// The asgard address cache lives for one THORChain block at its current block
// time, as the other clients' caches do, not for the fixed default block time.
func TestAsgardAddressCacheFollowsBlockTime(t *testing.T) {
	t.Cleanup(func() { blocktime.Set(constants.ThorchainBlockTime.Milliseconds()) })
	bridge := &asgardCountingBridge{}
	c := &Client{bridge: bridge}
	cached := []common.Address{common.Address("0x0000000000000000000000000000000000000001")}
	// fetched half a default block ago
	c.asgardCache.Store(&utxo.AsgardCache{Addresses: cached, FetchedAt: time.Now().Add(-constants.ThorchainBlockTime / 2)})

	// at the default block time the cache is still fresh
	addrs, err := c.getAsgardAddress()
	require.NoError(t, err)
	require.Equal(t, cached, addrs)
	require.Zero(t, bridge.calls)

	// at a quarter of it the entry is a block old, so it is refreshed (and kept
	// when the refresh fails)
	blocktime.Set(constants.ThorchainBlockTime.Milliseconds() / 4)
	addrs, err = c.getAsgardAddress()
	require.Error(t, err)
	require.Equal(t, cached, addrs)
	require.Equal(t, 1, bridge.calls)
}
