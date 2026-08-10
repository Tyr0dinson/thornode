package thornode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	sdk "gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

////////////////////////////////////////////////////////////////////////////////////////
// Init
////////////////////////////////////////////////////////////////////////////////////////

var thornodeURL string

func init() {
	config.Init()
	thornodeURL = config.GetBifrost().Thorchain.ChainHost
	if !strings.HasPrefix(thornodeURL, "http") {
		thornodeURL = "http://" + thornodeURL
	}
}

func BaseURL() string {
	return thornodeURL
}

////////////////////////////////////////////////////////////////////////////////////////
// Exported
////////////////////////////////////////////////////////////////////////////////////////

func GetBalances(addr common.Address) (common.Coins, error) {
	url := fmt.Sprintf("%s/cosmos/bank/v1beta1/balances/%s", thornodeURL, addr)
	var balances struct {
		Balances []struct {
			Denom  string `json:"denom"`
			Amount string `json:"amount"`
		} `json:"balances"`
	}
	err := Get(url, &balances)
	if err != nil {
		return nil, err
	}

	// convert to common.Coins
	coins := make(common.Coins, 0, len(balances.Balances))
	for _, balance := range balances.Balances {
		var amount uint64
		amount, err = strconv.ParseUint(balance.Amount, 10, 64)
		if err != nil {
			return nil, err
		}
		var asset common.Asset
		asset, err = common.NewAsset(strings.ToUpper(balance.Denom))
		if err != nil {
			return nil, err
		}
		coins = append(coins, common.NewCoin(asset, sdk.NewUint(amount)))
	}

	return coins, nil
}

// ErrInboundAddressNotFound means a successful inbound-address query had no
// entry for the requested chain. Request and malformed-response errors differ.
var ErrInboundAddressNotFound = errors.New("no inbound address found")

func GetInboundAddress(chain common.Chain) (address common.Address, router *common.Address, err error) {
	url := fmt.Sprintf("%s/thorchain/inbound_addresses", thornodeURL)
	var inboundAddresses []openapi.InboundAddress
	err = Get(url, &inboundAddresses)
	if err != nil {
		return "", nil, err
	}

	if inboundAddresses == nil {
		return "", nil, fmt.Errorf("invalid null inbound address response")
	}

	// find address for chain
	for _, inboundAddress := range inboundAddresses {
		if inboundAddress.Chain == nil || *inboundAddress.Chain == "" {
			return "", nil, fmt.Errorf("inbound address entry has no chain")
		}
		if *inboundAddress.Chain == string(chain) {
			if inboundAddress.Address == nil || *inboundAddress.Address == "" {
				return "", nil, fmt.Errorf("inbound address entry for chain %s has no address", chain)
			}
			if inboundAddress.Router != nil {
				router = new(common.Address)
				*router = common.Address(*inboundAddress.Router)
			}
			return common.Address(*inboundAddress.Address), router, nil
		}
	}

	return "", nil, fmt.Errorf("%w for chain %s", ErrInboundAddressNotFound, chain)
}

func GetInboundAddressPubKey(chain common.Chain) (common.PubKey, error) {
	url := fmt.Sprintf("%s/thorchain/inbound_addresses", thornodeURL)
	var inboundAddresses []openapi.InboundAddress
	err := Get(url, &inboundAddresses)
	if err != nil {
		return common.EmptyPubKey, err
	}

	for _, inboundAddress := range inboundAddresses {
		if *inboundAddress.Chain != string(chain) {
			continue
		}
		if inboundAddress.PubKey == nil || *inboundAddress.PubKey == "" {
			return common.EmptyPubKey, fmt.Errorf("no inbound pubkey found for chain %s", chain)
		}
		return common.NewPubKey(*inboundAddress.PubKey)
	}

	return common.EmptyPubKey, fmt.Errorf("no inbound pubkey found for chain %s", chain)
}

func GetRouterAddress(chain common.Chain) (common.Address, error) {
	url := fmt.Sprintf("%s/thorchain/inbound_addresses", thornodeURL)
	var inboundAddresses []openapi.InboundAddress
	err := Get(url, &inboundAddresses)
	if err != nil {
		return "", err
	}

	// find address for chain
	for _, inboundAddress := range inboundAddresses {
		if *inboundAddress.Chain == string(chain) {
			return common.Address(*inboundAddress.Router), nil
		}
	}

	return "", fmt.Errorf("no inbound address found for chain %s", chain)
}

func GetLiquidityProviders(asset common.Asset) ([]openapi.LiquidityProvider, error) {
	url := fmt.Sprintf("%s/thorchain/pool/%s/liquidity_providers", thornodeURL, asset.String())
	var liquidityProviders []openapi.LiquidityProvider
	err := Get(url, &liquidityProviders)
	return liquidityProviders, err
}

func GetPools() ([]openapi.Pool, error) {
	url := fmt.Sprintf("%s/thorchain/pools", thornodeURL)
	var pools []openapi.Pool
	err := Get(url, &pools)
	return pools, err
}

func GetVault(pubkey string) (openapi.Vault, error) {
	url := fmt.Sprintf("%s/thorchain/vault/%s", thornodeURL, pubkey)
	var vault openapi.Vault
	err := Get(url, &vault)
	return vault, err
}

func GetVaults() ([]openapi.Vault, error) {
	url := fmt.Sprintf("%s/thorchain/vaults/asgard", thornodeURL)
	var vaults []openapi.Vault
	err := Get(url, &vaults)
	return vaults, err
}

// GetInactiveVaults uses the same eligible inactive-vault list as Bifrost.
// The Asgard listing omits inactive vaults without a deficit, so it cannot
// discover destinations for the inactive-vault refund simulation.
func GetInactiveVaults() ([]openapi.Vault, error) {
	var keys openapi.VaultPubkeysResponse
	if err := Get(thornodeURL+"/thorchain/vaults/pubkeys", &keys); err != nil {
		return nil, err
	}
	vaults := make([]openapi.Vault, 0, len(keys.Inactive))
	for _, key := range keys.Inactive {
		if key.PubKey == "" {
			return nil, fmt.Errorf("inactive vault entry has no public key")
		}
		vault, err := GetVault(key.PubKey)
		if err != nil {
			return nil, fmt.Errorf("query inactive vault %s: %w", key.PubKey, err)
		}
		vaults = append(vaults, vault)
	}
	return vaults, nil
}

func GetNetwork() (openapi.NetworkResponse, error) {
	url := fmt.Sprintf("%s/thorchain/network", thornodeURL)
	var network openapi.NetworkResponse
	err := Get(url, &network)
	return network, err
}

// GetMempoolSwapUsageTOR returns the aggregate TOR reserved by active mempool
// swaps. The generated OpenAPI NetworkResponse does not currently expose this
// field, so decode the narrow raw network response here instead.
func GetMempoolSwapUsageTOR() (sdk.Uint, error) {
	url := fmt.Sprintf("%s/thorchain/network", thornodeURL)
	var network struct {
		MempoolSwapUsageTOR string `json:"mempool_swap_usage_tor"`
	}
	if err := Get(url, &network); err != nil {
		return sdk.ZeroUint(), err
	}

	usage, err := sdk.ParseUint(network.MempoolSwapUsageTOR)
	if err != nil {
		return sdk.ZeroUint(), fmt.Errorf("invalid mempool_swap_usage_tor %q: %w", network.MempoolSwapUsageTOR, err)
	}
	return usage, nil
}

func GetNodes() ([]openapi.Node, error) {
	return GetNodesAtHeight(0)
}

// GetNodesAtHeight returns node accounts as committed at height, or at the
// latest height when height is not positive.
func GetNodesAtHeight(height int64) ([]openapi.Node, error) {
	url := fmt.Sprintf("%s/thorchain/nodes", thornodeURL)
	if height > 0 {
		url = fmt.Sprintf("%s?height=%d", url, height)
	}
	var nodes []openapi.Node
	err := Get(url, &nodes)
	return nodes, err
}

func GetTHORName(name string) (openapi.Thorname, error) {
	endpoint := fmt.Sprintf("%s/thorchain/thorname/%s", thornodeURL, url.PathEscape(name))
	var thorname openapi.Thorname
	err := Get(endpoint, &thorname)
	return thorname, err
}

func GetPool(asset common.Asset) (openapi.Pool, error) {
	url := fmt.Sprintf("%s/thorchain/pool/%s", thornodeURL, asset.String())
	var pool openapi.Pool
	err := Get(url, &pool)
	return pool, err
}

// GetOutboundFee returns the current fee in the requested asset's 1e8 units.
func GetOutboundFee(asset common.Asset) (sdk.Uint, error) {
	url := fmt.Sprintf("%s/thorchain/outbound_fee/%s", thornodeURL, asset)
	var fees []openapi.OutboundFee
	if err := Get(url, &fees); err != nil {
		return sdk.ZeroUint(), err
	}
	for _, fee := range fees {
		if !strings.EqualFold(fee.Asset, asset.String()) {
			continue
		}
		amount, err := sdk.ParseUint(fee.OutboundFee)
		if err != nil || amount.IsZero() {
			return sdk.ZeroUint(), fmt.Errorf("invalid outbound fee for %s: %q", asset, fee.OutboundFee)
		}
		return amount, nil
	}
	return sdk.ZeroUint(), fmt.Errorf("no outbound fee found for %s", asset)
}

func GetSwapQuote(from, to common.Asset, amount sdk.Uint, destination, refundAddress common.Address, extended bool) (openapi.QuoteSwapResponse, error) {
	baseURL := fmt.Sprintf("%s/thorchain/quote/swap", thornodeURL)
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return openapi.QuoteSwapResponse{}, err
	}
	params := url.Values{}
	params.Add("from_asset", from.String())
	params.Add("to_asset", to.String())
	params.Add("amount", amount.String())
	if from.GetChain().RequiresExplicitRefundAddress() {
		if refundAddress.IsEmpty() {
			return openapi.QuoteSwapResponse{}, fmt.Errorf("refund address is required for source chain %s", from.GetChain())
		}
		params.Add("refund_address", refundAddress.String())
	}
	if !destination.IsEmpty() {
		params.Add("destination", destination.String())
	}
	if extended {
		params.Add("extended", "true")
	}
	parsedURL.RawQuery = params.Encode()
	url := parsedURL.String()

	var quote openapi.QuoteSwapResponse
	err = Get(url, &quote)
	return quote, err
}

func GetTxStages(txid string) (openapi.TxStagesResponse, error) {
	url := fmt.Sprintf("%s/thorchain/tx/stages/%s", thornodeURL, txid)
	var stages openapi.TxStagesResponse
	err := Get(url, &stages)
	return stages, err
}

func GetTxDetails(txid string) (openapi.TxDetailsResponse, error) {
	url := fmt.Sprintf("%s/thorchain/tx/details/%s", thornodeURL, txid)
	var details openapi.TxDetailsResponse
	err := Get(url, &details)
	return details, err
}

func GetStagedOutbounds() (openapi.StagedOutboundResponse, error) {
	url := fmt.Sprintf("%s/thorchain/queue/staged", thornodeURL)
	var wire stagedOutboundResponseWire
	if err := Get(url, &wire); err != nil {
		return openapi.StagedOutboundResponse{}, err
	}

	staged := openapi.StagedOutboundResponse{
		StagedOutboundItems: make([]openapi.StagedOutboundItem, 0, len(wire.StagedOutboundItems)),
	}
	for _, item := range wire.StagedOutboundItems {
		staged.StagedOutboundItems = append(staged.StagedOutboundItems, openapi.StagedOutboundItem{
			TxOutItem:       item.TxOutItem.openAPI(),
			ScheduledHeight: flexibleInt64Pointer(item.ScheduledHeight),
			StagedHeight:    flexibleInt64Pointer(item.StagedHeight),
		})
	}
	return staged, nil
}

// Protobuf JSON encodes int64 values as strings, while the generated OpenAPI
// model expects JSON numbers. These wire types deliberately accept either so
// the simulation remains compatible with both gateway representations.
type flexibleInt64 int64

func (v *flexibleInt64) UnmarshalJSON(data []byte) error {
	raw := strings.Trim(strings.TrimSpace(string(data)), `"`)
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("parse flexible int64 %q: %w", raw, err)
	}
	*v = flexibleInt64(parsed)
	return nil
}

func flexibleInt64Pointer(value *flexibleInt64) *int64 {
	if value == nil {
		return nil
	}
	result := int64(*value)
	return &result
}

type stagedOutboundResponseWire struct {
	StagedOutboundItems []stagedOutboundItemWire `json:"staged_outbound_items"`
}

type stagedOutboundItemWire struct {
	TxOutItem       stagedTxOutItemWire `json:"tx_out_item"`
	ScheduledHeight *flexibleInt64      `json:"scheduled_height"`
	StagedHeight    *flexibleInt64      `json:"staged_height"`
}

type stagedTxOutItemWire struct {
	Height                *flexibleInt64   `json:"height"`
	InHash                *string          `json:"in_hash"`
	OutHash               *string          `json:"out_hash"`
	Chain                 string           `json:"chain"`
	ToAddress             string           `json:"to_address"`
	VaultPubKey           *string          `json:"vault_pub_key"`
	VaultPubKeyEddsa      *string          `json:"vault_pub_key_eddsa"`
	VaultPubKeyFrost      *string          `json:"vault_pub_key_frost"`
	Coin                  stagedCoinWire   `json:"coin"`
	MaxGas                []stagedCoinWire `json:"max_gas"`
	GasRate               *flexibleInt64   `json:"gas_rate"`
	Memo                  *string          `json:"memo"`
	OriginalMemo          *string          `json:"original_memo"`
	Aggregator            *string          `json:"aggregator"`
	AggregatorTargetAsset *string          `json:"aggregator_target_asset"`
	AggregatorTargetLimit *string          `json:"aggregator_target_limit"`
	CloutSpent            *string          `json:"clout_spent"`
}

func (item stagedTxOutItemWire) openAPI() *openapi.TxOutItem {
	maxGas := make([]openapi.Coin, 0, len(item.MaxGas))
	for _, coin := range item.MaxGas {
		maxGas = append(maxGas, coin.openAPI())
	}
	return &openapi.TxOutItem{
		Height:                flexibleInt64Pointer(item.Height),
		InHash:                item.InHash,
		OutHash:               item.OutHash,
		Chain:                 item.Chain,
		ToAddress:             item.ToAddress,
		VaultPubKey:           item.VaultPubKey,
		VaultPubKeyEddsa:      item.VaultPubKeyEddsa,
		VaultPubKeyFrost:      item.VaultPubKeyFrost,
		Coin:                  item.Coin.openAPI(),
		MaxGas:                maxGas,
		GasRate:               flexibleInt64Pointer(item.GasRate),
		Memo:                  item.Memo,
		OriginalMemo:          item.OriginalMemo,
		Aggregator:            item.Aggregator,
		AggregatorTargetAsset: item.AggregatorTargetAsset,
		AggregatorTargetLimit: item.AggregatorTargetLimit,
		CloutSpent:            item.CloutSpent,
	}
}

type stagedCoinWire struct {
	Asset    string         `json:"asset"`
	Amount   string         `json:"amount"`
	Decimals *flexibleInt64 `json:"decimals"`
}

func (coin stagedCoinWire) openAPI() openapi.Coin {
	return openapi.Coin{
		Asset:    coin.Asset,
		Amount:   coin.Amount,
		Decimals: flexibleInt64Pointer(coin.Decimals),
	}
}

func GetOutboundQueue() ([]openapi.TxOutItem, error) {
	url := fmt.Sprintf("%s/thorchain/queue/outbound", thornodeURL)
	var outbounds []openapi.TxOutItem
	err := Get(url, &outbounds)
	return outbounds, err
}

func GetScheduledOutboundQueue() ([]openapi.TxOutItem, error) {
	url := fmt.Sprintf("%s/thorchain/queue/scheduled", thornodeURL)
	var outbounds []openapi.TxOutItem
	err := Get(url, &outbounds)
	return outbounds, err
}

func GetSwapDetails(txid string) (openapi.SwapDetailsResponse, error) {
	url := fmt.Sprintf("%s/thorchain/queue/swap/details/%s", thornodeURL, txid)
	var details openapi.SwapDetailsResponse
	err := Get(url, &details)
	return details, err
}

type VaultXMRState struct {
	VaultCreationHeightXmr int64  `json:"vault_creation_height_xmr"`
	PubKeyFrost            string `json:"pub_key_frost"`
	Status                 string `json:"status"`
}

func GetVaultXMRStates() ([]VaultXMRState, error) {
	url := fmt.Sprintf("%s/thorchain/vaults/asgard", thornodeURL)
	var vaults []VaultXMRState
	err := Get(url, &vaults)
	return vaults, err
}

type XMRSpentOutputRefs struct {
	SpentOutputRefs []common.OutputRef `json:"spent_output_refs"`
}

func GetXMRSpentOutputRefs(vaultPubKey string) (XMRSpentOutputRefs, error) {
	url := fmt.Sprintf("%s/thorchain/xmr/spent_refs/%s", thornodeURL, vaultPubKey)
	var result XMRSpentOutputRefs
	err := Get(url, &result)
	return result, err
}

type ObservedTxSpentRefs struct {
	ConsensusHeight int64 `json:"consensus_height"`
	Reverted        bool  `json:"reverted"`
	ObservedTx      struct {
		Tx              openapi.Tx         `json:"tx"`
		ObservedPubKey  string             `json:"observed_pub_key"`
		SpentOutputRefs []common.OutputRef `json:"spent_output_refs"`
		Status          string             `json:"status"`
	} `json:"tx"`
}

func GetTxSpentRefs(txid string) (ObservedTxSpentRefs, error) {
	// Read the stored outbound voter and its consensus/reversion metadata.
	// /tx/{id} can select an individual observation, and even the stored Tx's
	// status can return to incomplete when final votes replace it. Explicitly
	// select the outbound voter so inbound metadata cannot supply this evidence.
	url := fmt.Sprintf("%s/thorchain/tx/details/%s?outbound=true", thornodeURL, txid)
	var result ObservedTxSpentRefs
	err := Get(url, &result)
	return result, err
}

func GetMimirs() (map[string]int64, error) {
	url := fmt.Sprintf("%s/thorchain/mimir", thornodeURL)
	var mimirs map[string]int64
	err := Get(url, &mimirs)
	return mimirs, err
}

// GetMimir returns the effective value for a key. Unlike GetMimirs, this
// endpoint also observes mocknet environment fallbacks that are not persisted
// as on-chain Mimir entries.
func GetMimir(key string) (int64, error) {
	endpoint := fmt.Sprintf("%s/thorchain/mimir/key/%s", thornodeURL, url.PathEscape(key))
	var value int64
	err := Get(endpoint, &value)
	return value, err
}

func GetBlock(height int64) (openapi.BlockResponse, error) {
	url := fmt.Sprintf("%s/thorchain/block", thornodeURL)
	if height > 0 {
		url = fmt.Sprintf("%s?height=%d", url, height)
	}
	var block openapi.BlockResponse
	err := Get(url, &block)
	return block, err
}

func GetBlockHeight() (int64, error) {
	block, err := GetBlock(0)
	if err != nil {
		return 0, err
	}
	return block.Header.Height, nil
}

func GetMemoHash(txid string) (openapi.ReferenceMemoResponse, error) {
	url := fmt.Sprintf("%s/thorchain/memo/%s", thornodeURL, txid)
	var memoHash openapi.ReferenceMemoResponse
	err := Get(url, &memoHash)
	return memoHash, err
}

func Get(url string, target interface{}) error {
	var lastErr error
	for attempt := 1; attempt <= thornodeHTTPRetryAttempts; attempt++ {
		err := getOnce(url, target)
		if err == nil {
			return nil
		}

		lastErr = err
		if !isRetryableThornodeGetError(err) || attempt == thornodeHTTPRetryAttempts {
			return err
		}

		thornodeHTTPSleep(thornodeHTTPRetryDelay)
	}

	return lastErr
}

func getOnce(url string, target interface{}) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("(%s) HTTP: %d => %s", url, resp.StatusCode, buf)
	}

	// extract error if the request failed
	type ErrorResponse struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}

	errResp := ErrorResponse{}
	err = json.Unmarshal(buf, &errResp)
	if err == nil && errResp.Code != 0 && errResp.Message != "" {
		return fmt.Errorf("code: %d, message: %s", errResp.Code, errResp.Message)
	}

	// if target is a *[]byte, return the raw response
	if byteTarget, ok := target.(*[]byte); ok {
		*byteTarget = buf
		return nil
	}

	// decode response
	return json.Unmarshal(buf, target)
}

func isRetryableThornodeGetError(err error) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	msg := err.Error()
	for _, snippet := range []string{
		"context deadline exceeded",
		"connect: connection refused",
		"connection reset by peer",
		"EOF",
		"i/o timeout",
		"server closed idle connection",
		"HTTP: 429",
		"HTTP: 502",
		"HTTP: 503",
		"HTTP: 504",
	} {
		if strings.Contains(msg, snippet) {
			return true
		}
	}

	return false
}

////////////////////////////////////////////////////////////////////////////////////////
// Internal
////////////////////////////////////////////////////////////////////////////////////////

var httpClient = &http.Client{
	Transport: &http.Transport{
		Dial: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).Dial,
	},
	Timeout: 5 * time.Second,
}

var (
	thornodeHTTPRetryAttempts = 2
	thornodeHTTPRetryDelay    = 250 * time.Millisecond
	thornodeHTTPSleep         = time.Sleep
)
