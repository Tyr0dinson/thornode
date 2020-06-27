package common

import (
	"github.com/binance-chain/go-sdk/common/types"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

type Account struct {
	Sequence      int64
	AccountNumber int64
	Coins         Coins
}

// GetCoins transforms from binance coins
func GetCoins(chain Chain, accCoins []types.Coin) (Coins, error) {
	coins := make(Coins, 0)
	for _, coin := range accCoins {
		asset, err := NewAsset(chain.String() + "." + coin.Denom)
		if err != nil {
			return nil, err
		}
		coins = append(coins, NewCoin(asset, cosmos.NewUint(uint64(coin.Amount))))
	}
	return coins, nil
}

// NewAccount
func NewAccount(sequence, accountNumber int64, coins Coins) Account {
	return Account{
		Sequence:      sequence,
		AccountNumber: accountNumber,
		Coins:         coins,
	}
}
