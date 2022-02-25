package thorscan

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	coretypes "github.com/tendermint/tendermint/rpc/core/types"
	tendertypes "github.com/tendermint/tendermint/types"
	tctypes "gitlab.com/thorchain/thornode/x/thorchain/types"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/std"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/rs/zerolog/log"
	tmhttp "github.com/tendermint/tendermint/rpc/client/http"
)

// -------------------------------------------------------------------------------------
// Config
// -------------------------------------------------------------------------------------

const (
	// ---------- environment keys ----------

	EnvPreFetch    = "PREFETCH"
	EnvRPCEndpoint = "RPC_ENDPOINT"

	// ---------- static configuration ----------

	RPCWebsocketPath  = "/websocket"
	BlockBuffer       = 8               // necessary to avoid websocket hangup
	TxBuffer          = 1024            // necessary to avoid websocket hangup
	WebsocketTimeout  = 3 * time.Minute // allow for some block lag
	BlockFetchBackoff = 3 * time.Second
)

// -------------------------------------------------------------------------------------
// Init
// -------------------------------------------------------------------------------------

var (
	// ---------- environment config ----------

	PreFetch    = 32
	RPCEndpoint = "http://rpc-http.ninerealms.com:80" // current tendermint missing tls

	// ---------- decoders ----------

	DecodeTx     sdktypes.TxDecoder
	EncodeTx     sdktypes.TxEncoder
	DecodeTxJSON sdktypes.TxDecoder
	EncodeTxJSON sdktypes.TxEncoder

	// internal
	client *tmhttp.HTTP
)

func init() {
	var err error

	// set config from env
	if e := os.Getenv(EnvRPCEndpoint); e != "" {
		log.Info().Str("endpoint", e).Msg("setting rpc endpoint")
		RPCEndpoint = e
	}
	if e := os.Getenv(EnvPreFetch); e != "" {
		log.Info().Str("prefetch", e).Msg("setting prefetch blocks")
		PreFetch, err = strconv.Atoi(e)
		if err != nil {
			log.Fatal().Err(err).Msg("bad prefetch value")
		}
	}

	// setup serializers
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	std.RegisterInterfaces(interfaceRegistry)
	tctypes.RegisterInterfaces(interfaceRegistry)
	cdc := codec.NewProtoCodec(interfaceRegistry)
	DecodeTx = tx.DefaultTxDecoder(cdc)
	EncodeTx = tx.DefaultTxEncoder()
	DecodeTxJSON = tx.DefaultJSONTxDecoder(cdc)
	EncodeTxJSON = tx.DefaultJSONTxEncoder(cdc)

	// create new client with better connection reuse
	httpClient := &http.Client{
		Transport: http.DefaultTransport.(*http.Transport).Clone(),
	}
	httpClient.Transport.(*http.Transport).MaxIdleConns = PreFetch
	httpClient.Transport.(*http.Transport).MaxIdleConnsPerHost = PreFetch

	// setup client
	client, err = tmhttp.NewWithClient(RPCEndpoint, RPCWebsocketPath, httpClient)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create tendermint client")
	}
}

// -------------------------------------------------------------------------------------
// Exported - Scanners
// -------------------------------------------------------------------------------------

func ScanBlocks(start int) <-chan *coretypes.ResultBlock {
	ctx := context.Background()

	// create queue for block heights to fetch
	queue := make(chan int64)
	go func() {
		for height := int64(start); ; height++ {
			queue <- height
		}
	}()

	// setup ring buffer for block prefetching with routine per slot
	ring := make([]chan *coretypes.ResultBlock, PreFetch)
	shutdown := make(chan struct{}, PreFetch-1)
	for i := 0; i < PreFetch; i++ {
		ring[i] = make(chan *coretypes.ResultBlock)
		go func() {
			for height := range queue {
				for {
					b, err := client.Block(ctx, &height)
					if err != nil {
						if !strings.Contains(err.Error(), "less than or equal to the current blockchain height") {
							log.Error().Err(err).Int64("height", height).Msg("failed to fetch block")
						}
						time.Sleep(BlockFetchBackoff)
						continue
					}
					ring[int(height)%PreFetch] <- b

					// allow all but one routine to exit once we reach tip
					if b.Block.Header.Time.After(time.Now().Add(-time.Minute)) {
						select {
						case shutdown <- struct{}{}:
							log.Info().Int64("height", height).Msg("shutting down extra worker")
							return
						default:
						}
					}

					break
				}
			}
		}()
	}

	// start sequential reader to send to blocks channel
	out := make(chan *coretypes.ResultBlock)
	go func() {
		for height := int64(start); ; height++ {
			out <- <-ring[int(height)%PreFetch]
		}
	}()

	return out
}

func ScanBlockResults(start int) <-chan *coretypes.ResultBlockResults {
	ctx := context.Background()

	// create queue for block heights to fetch
	queue := make(chan int64)
	go func() {
		for height := int64(start); ; height++ {
			queue <- height
		}
	}()

	// setup ring buffer for block prefetching with routine per slot
	ring := make([]chan *coretypes.ResultBlockResults, PreFetch)
	for i := 0; i < PreFetch; i++ {
		ring[i] = make(chan *coretypes.ResultBlockResults)
		go func() {
			for height := range queue {
				for {
					b, err := client.BlockResults(ctx, &height)
					if err != nil {
						if !strings.Contains(err.Error(), "could not find results for height") &&
							!strings.Contains(err.Error(), "less than or equal to the current blockchain height") {
							log.Error().Err(err).Int64("height", height).Msg("failed to fetch block results")
						}
						time.Sleep(BlockFetchBackoff)
						continue
					}

					ring[int(height)%PreFetch] <- b
					break
				}
			}
		}()
	}

	// start sequential reader to send to blocks channel
	out := make(chan *coretypes.ResultBlockResults)
	go func() {
		for height := int64(start); ; height++ {
			out <- <-ring[int(height)%PreFetch]
		}
	}()

	return out
}

// -------------------------------------------------------------------------------------
// Exported - Subscribers
// -------------------------------------------------------------------------------------

func SubscribeBlocks() <-chan *tendertypes.EventDataNewBlock {
	ctx := context.Background()

	// setup client
	subClient, err := tmhttp.New(RPCEndpoint, RPCWebsocketPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create block tendermint client")
	}
	err = subClient.Start()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to start block tendermint client")
	}

	// subscribe to blocks
	sub, err := subClient.Subscribe(ctx, "", "tm.event = 'NewBlock'", PreFetch)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to subscribe to new blocks")
	}

	// start routine to extract data from subscription
	out := make(chan *tendertypes.EventDataNewBlock, BlockBuffer)
	go func() {
		for {
			timeout := time.After(WebsocketTimeout)
			select {
			case <-timeout:
				log.Fatal().Msg("websocket block read timeout")
			case e := <-sub:
				b, ok := e.Data.(tendertypes.EventDataNewBlock)
				if !ok {
					log.Fatal().Msg("failed invariant")
				}
				out <- &b
			}
		}
	}()

	return out
}

func SubscribeTxs() <-chan *tendertypes.EventDataTx {
	ctx := context.Background()

	// setup client
	subClient, err := tmhttp.New(RPCEndpoint, RPCWebsocketPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create tx tendermint client")
	}
	err = subClient.Start()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to start tx tendermint client")
	}

	// subscribe to txs
	sub, err := subClient.Subscribe(ctx, "", "tm.event = 'Tx'", PreFetch)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to subscribe to new transactions")
	}

	// start routine to extract data from subscription
	out := make(chan *tendertypes.EventDataTx, TxBuffer)
	go func() {
		for {
			timeout := time.After(WebsocketTimeout)
			select {
			case <-timeout:
				log.Fatal().Msg("websocket tx read timeout")
			case e := <-sub:
				b, ok := e.Data.(tendertypes.EventDataTx)
				if !ok {
					log.Fatal().Msg("failed invariant")
				}
				out <- &b
			}
		}
	}()

	return out
}
