package oracle

import (
	"fmt"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"math/big"
	"sort"
)

const (
	maxConversionSteps  = 3
	maxVolumePercentage = 0.4
	minProviders        = 1
	maxDeviation        = 1.5
)

type Converter struct {
	tickers map[string]map[string]types.Ticker // by symbol, provider: tickers[BTC/USDC][binance]
	rates   map[string]map[string]types.Ticker // by denom, provider: rates[BTC][binance]
	metrics metrics.Metrics
}

func NewConverter(tickers map[string]map[string]types.Ticker) *Converter {
	return &Converter{
		tickers: tickers,
		rates:   map[string]map[string]types.Ticker{},
	}
}

func (c *Converter) AddRate(denom, provider string, rate types.Ticker) {
	_, found := c.rates[denom]
	if !found {
		c.rates[denom] = map[string]types.Ticker{}
	}

	c.rates[denom][provider] = rate
}

// GetRate returns the USD rate of the provided denom by filtering known
// prices in regards to maxDeviation and computing the volume weighted average
func (c *Converter) GetRate(denom string) (*big.Float, *big.Float, error) {
	rates, found := c.rates[denom]
	if !found || len(rates) < minProviders {
		return nil, nil, fmt.Errorf("not enough rates")
	}

	var err error
	rates, err = FilterDeviations(rates)
	if err != nil {
		return nil, nil, err
	}

	volume := new(big.Float)

	// replace volume totals with capped percentages
	values := map[string]*big.Float{}
	for provider, rate := range rates {
		values[provider] = new(big.Float).Copy(rate.Volume)
		volume.Add(volume, rate.Volume)
	}

	threshold := big.NewFloat(maxVolumePercentage)
	for provider, percent := range ComputeCappedPercentage(values, threshold) {
		rates[provider].Volume.Copy(percent)
	}

	rate, err := ComputeVwap(rates)
	if err != nil {
		return nil, nil, err
	}

	return rate, volume, nil
}

func (c *Converter) ConvertToUsd() (map[string]*big.Float, error) {
	pairsByQuote := map[string][]types.CurrencyPair{}
	symbols := map[string]struct{}{}
	for _, tickers := range c.tickers {
		for provider, ticker := range tickers {
			if ticker.Pair.Base == "USD" {
				// we are looking for the base price in USD,
				// this wouldn't make sense
				continue
			}

			quote := ticker.Pair.Quote

			if quote == "USD" {
				c.AddRate(ticker.Pair.Base, provider, ticker)
				continue
			}

			_, found := pairsByQuote[quote]
			if !found {
				pairsByQuote[quote] = []types.CurrencyPair{}
			}

			// dedup ticker symbols
			symbol := ticker.Pair.String()
			_, found = symbols[symbol]
			if found {
				continue
			}
			symbols[symbol] = struct{}{}

			pairsByQuote[quote] = append(pairsByQuote[quote], ticker.Pair)
		}
	}

	for round := 0; round < maxConversionSteps; round++ {
		rates := []types.Ticker{}
		for denom := range c.rates {
			rate, volume, err := c.GetRate(denom)
			if err != nil {
				continue
			}
			rates = append(rates, types.Ticker{
				Pair:   types.CurrencyPair{Base: denom, Quote: "USD"},
				Price:  rate,
				Volume: new(big.Float).Mul(volume, rate),
			})
		}

		// sort rates by volume, so we convert pairs with the highes volume for
		// the quote asset first

		sort.Slice(rates, func(i, j int) bool {
			return rates[i].Volume.Cmp(rates[j].Volume) > 0
		})

		for i := range rates {
			// rate is BTC/USD, so we are looking for x/BTC pairs
			quote := rates[i].Pair.Base
			pairs, found := pairsByQuote[quote]
			if !found {
				continue
			}

			// get fresh rate, in case it was updated in the during this round
			rate, _, err := c.GetRate(quote)
			if err != nil {
				continue
			}

			for _, pair := range pairs {
				symbol := pair.String()
				base := pair.Base

				// get all tickers for the current symbol
				tickers := c.tickers[symbol]
				for provider, ticker := range tickers {
					c.AddRate(base, provider, types.Ticker{
						Price:  new(big.Float).Mul(ticker.Price, rate),
						Volume: new(big.Float).Copy(ticker.Volume),
						Time:   ticker.Time,
					})
				}

				delete(pairsByQuote, quote)
			}
		}

		// Stop if all pairs could be resolved
		if len(pairsByQuote) == 0 {
			break
		}
	}

	// get all rates

	rates := map[string]*big.Float{}

	for denom := range c.rates {
		rate, _, err := c.GetRate(denom)
		if err != nil {
			continue
		}
		rates[denom] = new(big.Float).Copy(rate)
	}

	return rates, nil
}

// ----------------------------------------------------------------------------

// TODO: Use MAD instead
// ComputeStandardDeviation computes and returns the standard deviation and mean
// of provided ticker prices
func ComputeStandardDeviation(values []*big.Float) (*big.Float, *big.Float, error) {
	if len(values) < 2 {
		return nil, nil, fmt.Errorf("not enough tickers for calculation")
	}

	sum := new(big.Float)

	for _, value := range values {
		sum.Add(sum, value)
	}

	count := big.NewFloat(float64(len(values)))
	mean := new(big.Float).Quo(sum, count)

	variance := new(big.Float)
	for _, value := range values {
		deviation := new(big.Float).Sub(value, mean)
		variance.Add(variance, deviation.Mul(deviation, deviation))
	}

	variance.Quo(variance, count)

	deviation := new(big.Float).Sqrt(variance)

	return deviation, mean, nil
}

func FilterDeviations(tickers map[string]types.Ticker) (map[string]types.Ticker, error) {
	values := []*big.Float{}
	for _, ticker := range tickers {
		values = append(values, ticker.Price)
	}

	deviation, mean, err := ComputeStandardDeviation(values)
	if err != nil {
		return nil, err
	}

	margin := new(big.Float).Mul(deviation, big.NewFloat(maxDeviation))

	lowerBound := new(big.Float).Sub(mean, margin)
	upperBound := new(big.Float).Add(mean, margin)

	filtered := map[string]types.Ticker{}
	for provider, ticker := range tickers {
		price := ticker.Price
		if price.Cmp(lowerBound) >= 0 && price.Cmp(upperBound) <= 0 {
			filtered[provider] = ticker
		}
	}

	return filtered, nil
}

func ComputeVwap(tickers map[string]types.Ticker) (*big.Float, error) {
	if len(tickers) == 0 {
		return nil, fmt.Errorf("not enough tickers for vwap calculation")
	}

	volumeSum := new(big.Float)
	weightedPriceSum := new(big.Float)

	for _, ticker := range tickers {
		volumeSum.Add(volumeSum, ticker.Volume)
		weightedPrice := new(big.Float).Mul(ticker.Price, ticker.Volume)
		weightedPriceSum.Add(weightedPriceSum, weightedPrice)
	}

	// zero
	if volumeSum.Cmp(new(big.Float)) == 0 {
		volumeSum = big.NewFloat(1)
	}

	return new(big.Float).Quo(weightedPriceSum, volumeSum), nil
}

func ComputePercentage(
	values map[string]*big.Float,
) map[string]*big.Float {
	total := new(big.Float)
	for _, value := range values {
		total.Add(total, value)
	}

	percentage := map[string]*big.Float{}
	for provider, value := range values {
		percentage[provider] = new(big.Float).Quo(value, total)
	}

	return percentage
}

func ComputeCappedPercentage(
	values map[string]*big.Float,
	cap *big.Float,
) map[string]*big.Float {
	return RedistributePercentage(ComputePercentage(values), new(big.Float), cap)
}

func RedistributePercentage(
	values map[string]*big.Float,
	amount, cap *big.Float,
) map[string]*big.Float {
	total := new(big.Float)
	remain := new(big.Float)

	for _, value := range values {
		total.Add(total, value)
	}

	percentages := ComputePercentage(values)
	capped := map[string]*big.Float{}

	for k, v := range values {
		share := new(big.Float).Mul(percentages[k], amount)
		value := new(big.Float).Add(v, share)
		if value.Cmp(cap) >= 0 {
			remain.Sub(value, cap)
			capped[k] = new(big.Float).Copy(cap)
			delete(values, k)
		} else {
			values[k].Add(v, share)
		}
	}

	if remain.Cmp(cap) > 0 && len(values) > 0 {
		for k, v := range RedistributePercentage(values, remain, cap) {
			capped[k] = new(big.Float).Copy(v)
		}
	}

	for k, v := range values {
		capped[k] = new(big.Float).Copy(v)
	}

	return capped
}
