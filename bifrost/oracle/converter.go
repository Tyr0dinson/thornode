package oracle

import (
	"fmt"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"math/big"
	"sort"
)

const (
	maxConversionSteps  = 3
	maxVolumePercentage = 0.4
	minProviders        = 1
	maxDeviation        = 1
)

type Converter struct {
	tickers map[string]map[string]types.Ticker // by symbol, provider: tickers[BTC/USDC][binance]
	rates   map[string]map[string]types.Ticker // by denom, provider: rates[BTC][binance]
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
func (c *Converter) GetRate(denom string) (*big.Float, error) {
	rates, found := c.rates[denom]
	if !found || len(rates) < minProviders {
		return nil, fmt.Errorf("not enough rates")
	}

	var err error
	rates, err = FilterVolumes(rates)
	rates, err = FilterDeviations(rates)
	if err != nil {
		return nil, err
	}

	return ComputeVwap(rates)
}

// TODO: sanity check the reported volume maybe by tracking historic ratios between all configured providers
func (c *Converter) ConvertToUsd() (map[string]*big.Float, error) {
	// dedup symbols and add usd rates
	symbols := map[string]struct{}{}
	pairs := []types.CurrencyPair{}
	for symbol, tickers := range c.tickers {
		for provider, ticker := range tickers {
			if ticker.Pair.Base == "USD" {
				// we are looking for the base price in usd,
				// this wouldn't make sense
				continue
			}

			if ticker.Pair.Quote == "USD" {
				c.AddRate(ticker.Pair.Base, provider, ticker)
				continue
			}

			_, found := symbols[symbol]
			if found {
				continue
			}

			symbols[symbol] = struct{}{}
			pairs = append(pairs, ticker.Pair)
		}
	}

	// sort pairs, so new usd rates for a given base asset can be calculated
	// at once, and in turn could be used for the next assets

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].String() < pairs[j].String()
	})

	for round := 0; round < maxConversionSteps; round++ {
		unresolved := []types.CurrencyPair{}

		for _, pair := range pairs {
			symbol := pair.String()
			base := pair.Base
			quote := pair.Quote

			rate, err := c.GetRate(quote)
			if err != nil {
				unresolved = append(unresolved, pair)
				continue
			}

			// get all tickers for the current symbol
			tickers := c.tickers[symbol]
			for provider, ticker := range tickers {
				c.AddRate(base, provider, types.Ticker{
					Price:  new(big.Float).Mul(ticker.Price, rate),
					Volume: new(big.Float).Copy(ticker.Volume),
					Time:   ticker.Time,
				})
			}
		}

		// Stop if there are no unresolved symbols left or no symbol could
		// be converted, in which case the list of unresolved symbols is
		// the same as the list of pairs
		if len(unresolved) == 0 || len(unresolved) == len(pairs) {
			break
		}

		// Try the next round with all unresolved symbols
		pairs = []types.CurrencyPair{}
		pairs = append(pairs, unresolved...)
	}

	// get all rates

	rates := map[string]*big.Float{}

	for denom := range c.rates {
		rate, err := c.GetRate(denom)
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

func FilterVolumes(tickers map[string]types.Ticker) (map[string]types.Ticker, error) {
	total := new(big.Float)
	for _, ticker := range tickers {
		total.Add(total, ticker.Volume)
	}

	filtered := map[string]types.Ticker{}
	for provider, ticker := range tickers {
		percentage := new(big.Float).Quo(ticker.Volume, total)
		if percentage.Cmp(big.NewFloat(maxVolumePercentage)) <= 1 {
			// volume is too high, discard
			continue
		}

		filtered[provider] = ticker
	}

	return filtered, nil
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
		if price.Cmp(lowerBound) == 1 && price.Cmp(upperBound) == -1 {
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
