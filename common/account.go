package common

import (
	"github.com/binance-chain/go-sdk/common/types"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

type AccountCoin struct {
	Amount cosmos.Uint
	Denom  string
}

type AccountCoins []AccountCoin

type Account struct {
	Sequence      int64
	AccountNumber int64
	Coins         AccountCoins
}

// GetCoins transforms from binance coins
func GetCoins(accCoins []types.Coin) AccountCoins {
	coins := make(AccountCoins, 0)
	for _, coin := range accCoins {
		coins = append(coins, AccountCoin{Amount: cosmos.NewUint(uint64(coin.Amount)), Denom: coin.Denom})
	}
	return coins
}

// NewAccount
func NewAccount(sequence, accountNumber int64, coins AccountCoins) Account {
	return Account{
		Sequence:      sequence,
		AccountNumber: accountNumber,
		Coins:         coins,
	}
}
