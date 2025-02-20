package stagenet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	// "strconv"
	// "strings"

	sdkmath "cosmossdk.io/math"

	// "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain/common"
)


// InboundAddress struct for InboundAddress.
type InboundAddress struct {
	Chain   *string `json:"chain,omitempty"`
	PubKey  *string `json:"pub_key,omitempty"`
	Address *string `json:"address,omitempty"`
	Router  *string `json:"router,omitempty"`
	// Returns true if trading is unavailable for this chain, either because trading is halted globally or specifically for this chain
	Halted bool `json:"halted"`
	// Returns true if trading is paused globally
	GlobalTradingPaused *bool `json:"global_trading_paused,omitempty"`
	// Returns true if trading is paused for this chain
	ChainTradingPaused *bool `json:"chain_trading_paused,omitempty"`
	// Returns true if LP actions are paused for this chain
	ChainLpActionsPaused *bool `json:"chain_lp_actions_paused,omitempty"`
	// The minimum fee rate used by vaults to send outbound TXs. The actual fee rate may be higher. For EVM chains this is returned in gwei (1e9).
	GasRate *string `json:"gas_rate,omitempty"`
	// Units of the gas_rate.
	GasRateUnits *string `json:"gas_rate_units,omitempty"`
	// Avg size of outbound TXs on each chain. For UTXO chains it may be larger than average, as it takes into account vault consolidation txs, which can have many vouts
	OutboundTxSize *string `json:"outbound_tx_size,omitempty"`
	// The total outbound fee charged to the user for outbound txs in the gas asset of the chain.
	OutboundFee *string `json:"outbound_fee,omitempty"`
	// Defines the minimum transaction size for the chain in base units (sats, wei, uatom). Transactions with asset amounts lower than the dust_threshold are ignored.
	DustThreshold *string `json:"dust_threshold,omitempty"`
}

func APIGetInboundAddress(chain string) (address string, router *string, err error) {
	ctx := context.Background()
	url := fmt.Sprintf("%s/thorchain/inbound_addresses", "http://localhost:1317")
	var inboundAddresses []InboundAddress
	err = get(ctx, url, &inboundAddresses)
	if err != nil {
		return "", nil, err
	}

	// find address for chain
	for _, inboundAddress := range inboundAddresses {
		if *inboundAddress.Chain == chain {
			if inboundAddress.Router != nil {
				router = new(string)
				*router = *inboundAddress.Router
			}
			return *inboundAddress.Address, router, nil
		}
	}

	return "", nil, fmt.Errorf("no inbound address found for chain %s", chain)
}


// QuoteFees struct for QuoteFees.
type QuoteFees struct {
	// the target asset used for all fees
	Asset string `json:"asset"`
	// affiliate fee in the target asset
	Affiliate *string `json:"affiliate,omitempty"`
	// outbound fee in the target asset
	Outbound *string `json:"outbound,omitempty"`
	// liquidity fees paid to pools in the target asset
	Liquidity string `json:"liquidity"`
	// total fees in the target asset
	Total string `json:"total"`
	// the swap slippage in basis points
	SlippageBps int64 `json:"slippage_bps"`
	// total basis points in fees relative to amount out
	TotalBps int64 `json:"total_bps"`
}

// QuoteSwapResponse struct for QuoteSwapResponse.
type QuoteSwapResponse struct {
	// the inbound address for the transaction on the source chain
	InboundAddress *string `json:"inbound_address,omitempty"`
	// the approximate number of source chain blocks required before processing
	InboundConfirmationBlocks *int64 `json:"inbound_confirmation_blocks,omitempty"`
	// the approximate seconds for block confirmations required before processing
	InboundConfirmationSeconds *int64 `json:"inbound_confirmation_seconds,omitempty"`
	// the number of thorchain blocks the outbound will be delayed
	OutboundDelayBlocks int64 `json:"outbound_delay_blocks"`
	// the approximate seconds for the outbound delay before it will be sent
	OutboundDelaySeconds int64     `json:"outbound_delay_seconds"`
	Fees                 QuoteFees `json:"fees"`
	// the EVM chain router contract address
	Router *string `json:"router,omitempty"`
	// expiration timestamp in unix seconds
	Expiry int64 `json:"expiry"`
	// static warning message
	Warning string `json:"warning"`
	// chain specific quote notes
	Notes string `json:"notes"`
	// Defines the minimum transaction size for the chain in base units (sats, wei, uatom). Transactions with asset amounts lower than the dust_threshold are ignored.
	DustThreshold *string `json:"dust_threshold,omitempty"`
	// The recommended minimum inbound amount for this transaction type & inbound asset. Sending less than this amount could result in failed refunds.
	RecommendedMinAmountIn *string `json:"recommended_min_amount_in,omitempty"`
	// the recommended gas rate to use for the inbound to ensure timely confirmation
	RecommendedGasRate *string `json:"recommended_gas_rate,omitempty"`
	// the units of the recommended gas rate
	GasRateUnits *string `json:"gas_rate_units,omitempty"`
	// generated memo for the swap
	Memo *string `json:"memo,omitempty"`
	// the amount of the target asset the user can expect to receive after fees
	ExpectedAmountOut string `json:"expected_amount_out"`
	// the maximum amount of trades a streaming swap can do for a trade
	MaxStreamingQuantity *int64 `json:"max_streaming_quantity,omitempty"`
	// the number of blocks the streaming swap will execute over
	StreamingSwapBlocks *int64 `json:"streaming_swap_blocks,omitempty"`
	// approx the number of seconds the streaming swap will execute over
	StreamingSwapSeconds *int64 `json:"streaming_swap_seconds,omitempty"`
	// total number of seconds a swap is expected to take (inbound conf + streaming swap + outbound delay)
	TotalSwapSeconds *int64 `json:"total_swap_seconds,omitempty"`
}

func APIGetSwapQuote(from, to string, amount sdkmath.Uint) (QuoteSwapResponse, error) {
	ctx := context.Background()
	baseURL := fmt.Sprintf("%s/thorchain/quote/swap", "http://localhost:1317")
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return QuoteSwapResponse{}, err
	}
	params := url.Values{}
	params.Add("from_asset", from)
	params.Add("to_asset", to)
	params.Add("amount", amount.String())
	parsedURL.RawQuery = params.Encode()
	url := parsedURL.String()

	var quote QuoteSwapResponse
	err = get(ctx, url, &quote)
	return quote, err
}

func get(ctx context.Context, url string, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// extract error if the request failed
	type ErrorResponse struct {
		Error string `json:"error"`
	}
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	errResp := ErrorResponse{}
	err = json.Unmarshal(buf, &errResp)
	if err == nil && errResp.Error != "" {
		return errors.New(errResp.Error)
	}

	// decode response
	return json.Unmarshal(buf, target)
}