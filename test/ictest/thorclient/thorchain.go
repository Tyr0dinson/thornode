package thorclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/cosmos/gogoproto/proto"
	
	"cosmossdk.io/x/tx/signing"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	sdkcodectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/test/ictest/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// Endpoint urls
const (
	AuthAccountEndpoint      = "/cosmos/auth/v1beta1/accounts"
	LastBlockEndpoint        = "/thorchain/lastblock"
)

// thorchainBridge will be used to send tx to THORChain
type thorchainBridge struct {
	logger        zerolog.Logger
	cfg           config.BifrostClientConfiguration
	keys          *Keys
	blockHeight   int64
	accountNumber uint64
	seqNumber     uint64
	httpClient    *retryablehttp.Client
	broadcastLock *sync.RWMutex
}

type ThorchainBridge interface {
	GetConfig() config.BifrostClientConfiguration
	GetContext() client.Context
	PostNetworkFee(height int64, chain common.Chain, transactionSize, transactionRate uint64) (common.TxID, error)
	GetBlockHeight() (int64, error)
	Broadcast(msgs ...sdk.Msg) (common.TxID, error)
}

// httpResponseCache used for caching HTTP responses for less frequent querying
type httpResponseCache struct {
	httpResponse        []byte
	httpResponseChecked time.Time
	httpResponseMu      *sync.Mutex
}

var (
	httpResponseCaches   = make(map[string]*httpResponseCache) // String-to-pointer map for quicker lookup
	httpResponseCachesMu = &sync.Mutex{}
)

// NewThorchainBridge create a new instance of ThorchainBridge
func NewThorchainBridge(cfg config.BifrostClientConfiguration, k *Keys) (ThorchainBridge, error) {
	// main module logger
	logger := log.With().Str("module", "thorchain_client").Logger()

	if len(cfg.ChainID) == 0 {
		return nil, errors.New("chain id is empty")
	}
	if len(cfg.ChainHost) == 0 {
		return nil, errors.New("chain host is empty")
	}

	httpClient := retryablehttp.NewClient()
	httpClient.Logger = nil

	return &thorchainBridge{
		logger:        logger,
		cfg:           cfg,
		keys:          k,
		httpClient:    httpClient,
		broadcastLock: &sync.RWMutex{},
	}, nil
}

// EncodingConfig specifies the concrete encoding types to use for a given app.
// This is provided for compatibility between protobuf and amino implementations.
type EncodingConfig struct {
	InterfaceRegistry sdkcodectypes.InterfaceRegistry
	Codec             codec.Codec
	TxConfig          client.TxConfig
	Amino             *codec.LegacyAmino
}
// MakeEncodingConfig creates an EncodingConfig for an amino based test configuration.
func MakeEncodingConfig() EncodingConfig {
	amino := codec.NewLegacyAmino()

	interfaceRegistrySigningOptions := signing.Options{
		AddressCodec: address.Bech32Codec{
			//Bech32Prefix: "tthor",
			Bech32Prefix: sdk.GetConfig().GetBech32AccountAddrPrefix(),
		},
		ValidatorAddressCodec: address.Bech32Codec{
			Bech32Prefix: sdk.GetConfig().GetBech32ValidatorAddrPrefix(),
		},
	}
	interfaceRegistry, err := sdkcodectypes.NewInterfaceRegistryWithOptions(sdkcodectypes.InterfaceRegistryOptions{
		ProtoFiles:     proto.HybridResolver,
		SigningOptions: interfaceRegistrySigningOptions,
	})
	if err != nil {
		panic(err)
	}

	marshaler := codec.NewProtoCodec(interfaceRegistry)

	return EncodingConfig{
		InterfaceRegistry: interfaceRegistry,
		Codec:             marshaler,
		TxConfig:          tx.NewTxConfig(marshaler, tx.DefaultSignModes),
		Amino:             amino,
	}
}

// GetContext return a valid context with all relevant values set
func (b *thorchainBridge) GetContext() client.Context {
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		panic(err)
	}
	ctx := client.Context{}
	ctx = ctx.WithKeyring(b.keys.GetKeybase())
	ctx = ctx.WithChainID(string(b.cfg.ChainID))
	ctx = ctx.WithHomeDir(b.cfg.ChainHomeFolder)
	ctx = ctx.WithFromName(b.cfg.SignerName)
	ctx = ctx.WithFromAddress(signerAddr)
	ctx = ctx.WithBroadcastMode("sync")

	encodingConfig := MakeEncodingConfig()
	ctx = ctx.WithCodec(encodingConfig.Codec)
	ctx = ctx.WithInterfaceRegistry(encodingConfig.InterfaceRegistry)
	ctx = ctx.WithTxConfig(encodingConfig.TxConfig)
	ctx = ctx.WithLegacyAmino(encodingConfig.Amino)
	ctx = ctx.WithAccountRetriever(authtypes.AccountRetriever{})

	remote := b.cfg.ChainRPC
	if !strings.HasPrefix(b.cfg.ChainHost, "http") {
		remote = fmt.Sprintf("tcp://%s", remote)
	}
	ctx = ctx.WithNodeURI(remote)
	client, err := rpchttp.New(remote, "/websocket")
	if err != nil {
		panic(err)
	}
	ctx = ctx.WithClient(client)
	return ctx
}

func (b *thorchainBridge) getWithPath(path string) ([]byte, int, error) {
	return b.get(b.getThorChainURL(path))
}

// get handle all the low level http GET calls using retryablehttp.ThorchainBridge
func (b *thorchainBridge) get(url string) ([]byte, int, error) {
	// To reduce querying time and chance of "429 Too Many Requests",
	// do not query the same endpoint more than once per block time.
	httpResponseCachesMu.Lock()
	respCachePointer := httpResponseCaches[url]
	if respCachePointer == nil {
		// Since this is the first time using this endpoint, prepare a Mutex for it.
		respCachePointer = &httpResponseCache{httpResponseMu: &sync.Mutex{}}
		httpResponseCaches[url] = respCachePointer
	}
	httpResponseCachesMu.Unlock()

	// So lengthy queries don't hold up short queries, use query-specific mutexes.
	respCachePointer.httpResponseMu.Lock()
	defer respCachePointer.httpResponseMu.Unlock()

	// When the same endpoint has been checked within the span of a single block, return the cached response.
	if time.Since(respCachePointer.httpResponseChecked) < constants.ThorchainBlockTime && respCachePointer.httpResponse != nil {
		return respCachePointer.httpResponse, http.StatusOK, nil
	}

	resp, err := b.httpClient.Get(url)
	if err != nil {
		return nil, http.StatusNotFound, fmt.Errorf("failed to GET from thorchain: %w", err)
	}
	defer func() {
		// trunk-ignore(golangci-lint/govet): shadow
		if err := resp.Body.Close(); err != nil {
			b.logger.Error().Err(err).Msg("failed to close response body")
		}
	}()

	buf, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return buf, resp.StatusCode, errors.New("Status code: " + resp.Status + " returned")
	}
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}

	// All being well with the response, save it to the cache.
	respCachePointer.httpResponse = buf
	respCachePointer.httpResponseChecked = time.Now()

	return buf, resp.StatusCode, nil
}

// getThorChainURL with the given path
func (b *thorchainBridge) getThorChainURL(path string) string {
	if strings.HasPrefix(b.cfg.ChainHost, "http") {
		return fmt.Sprintf("%s/%s", b.cfg.ChainHost, path)
	}

	uri := url.URL{
		Scheme: "http",
		Host:   b.cfg.ChainHost,
		Path:   path,
	}
	return uri.String()
}

// getAccountNumberAndSequenceNumber returns account and Sequence number required to post into thorchain
func (b *thorchainBridge) getAccountNumberAndSequenceNumber() (uint64, uint64, error) {
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get signer address: %w", err)
	}
	path := fmt.Sprintf("%s/%s", AuthAccountEndpoint, signerAddr)

	body, _, err := b.getWithPath(path)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get auth accounts: %w", err)
	}

	var resp types.AccountResp
	if err = json.Unmarshal(body, &resp); err != nil {
		return 0, 0, fmt.Errorf("failed to unmarshal account resp: %w", err)
	}
	acc := resp.Account

	b.logger.Info().Interface("account response", resp).Msg("Account response")

	return acc.AccountNumber, acc.Sequence, nil
}

// GetConfig return the configuration
func (b *thorchainBridge) GetConfig() config.BifrostClientConfiguration {
	return b.cfg
}


// PostNetworkFee send network fee message to THORNode
func (b *thorchainBridge) PostNetworkFee(height int64, chain common.Chain, transactionSize, transactionRate uint64) (common.TxID, error) {
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		return common.BlankTxID, fmt.Errorf("fail to get signer address: %w", err)
	}
	msg := types.NewMsgNetworkFee(height, chain, transactionSize, transactionRate, signerAddr)
	return b.Broadcast(msg)
}
