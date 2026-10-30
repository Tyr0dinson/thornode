package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
)

// TestStreamingSwapDeltaUsesStreamStartPrices replays mainnet swap 0605B960…D007148D:
// 21.358 ETH streamed into the shallow ZEC pool, partially filled for 24.0487 ZEC
// (~$1,288/ZEC) and refunded 7.709 ETH. By the last sub-swap the swap had pushed
// the pool to $2,267/ZEC, so valuing the outbound at that block reported a +48.55%
// delta that never existed.
func TestStreamingSwapDeltaUsesStreamStartPrices(t *testing.T) {
	const (
		inHash          = "0605B960375ABA00D0D1ADEFE746AC24796A8DEC66FC6C89F7EF83B3D007148D"
		consensusHeight = 28451324
		outboundHeight  = 28452882
	)
	pools := map[string]string{
		"28451323": `[{"asset":"ETH.ETH","asset_tor_price":"232747202124"},{"asset":"ZEC.ZEC","asset_tor_price":"132201920156"}]`,
		"28452882": `[{"asset":"ETH.ETH","asset_tor_price":"227033676543"},{"asset":"ZEC.ZEC","asset_tor_price":"226740224598"}]`,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		height := r.URL.Query().Get("height")
		switch r.URL.Path {
		case "/thorchain/pools":
			body, ok := pools[height]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, body)
		case "/thorchain/tx/details/" + inHash:
			fmt.Fprintf(w, `{"consensus_height":%d,"finalised_height":%d,"outbound_height":%d}`,
				consensusHeight, outboundHeight, outboundHeight)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	orig := config.Get()
	defer config.Set(orig)
	cfg := orig
	cfg.Endpoints.Thornode = ts.URL
	cfg.MaxRetries = 1
	config.Set(cfg)
	util.InitCache()

	inbound := common.NewCoin(common.ETHAsset, cosmos.NewUint(2135800000))
	zecAsset, err := common.NewAsset("ZEC.ZEC")
	require.NoError(t, err)
	outbounds := []common.Coin{
		common.NewCoin(zecAsset, cosmos.NewUint(2404865370)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(770942545)),
	}

	// pre-stream prices precede the first sub-swap at the consensus height
	priceHeight := streamStartPriceHeight(outboundHeight, inHash)
	require.Equal(t, int64(consensusHeight-1), priceHeight)

	// the outbound block prices reproduce the bogus alert
	delta, inboundUSD := swapDeltaUSD(outboundHeight, inbound, outbounds)
	require.InDelta(t, 48.55, delta/inboundUSD*100, 0.01)

	// stream start prices show the swap filled near market
	delta, inboundUSD = swapDeltaUSD(priceHeight, inbound, outbounds)
	require.InDelta(t, 0.05, delta/inboundUSD*100, 0.01)
	require.Less(t, int64(delta/inboundUSD*10000), int64(100))
}

func TestStreamStartPriceHeightFallsBackToOutboundHeight(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// consensus at the outbound height, as for a single block swap
		fmt.Fprint(w, `{"consensus_height":100}`)
	}))
	defer ts.Close()

	orig := config.Get()
	defer config.Set(orig)
	cfg := orig
	cfg.Endpoints.Thornode = ts.URL
	cfg.MaxRetries = 1
	config.Set(cfg)
	util.InitCache()

	require.Equal(t, int64(100), streamStartPriceHeight(100, "ABC"))
}
