package common

import (
	"github.com/tendermint/tendermint/crypto"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

type Account interface {
	GetAddress() cosmos.AccAddress
	SetAddress(address cosmos.AccAddress) error // errors if already set.

	GetPubKey() crypto.PubKey // can return nil.
	SetPubKey(crypto.PubKey) error

	GetAccountNumber() int64
	SetAccountNumber(int64) error

	GetSequence() int64
	SetSequence(int64) error

	GetCoins() common.AccountCoins
	SetCoins(common.AccountCoins) error
	Clone() Account

	GetFlags() uint64
	SetFlags(flags uint64)
}

// AppAccount definition
type AppAccount struct {
	BaseAccount `json:"base"`
	Name        string `json:"name"`
	FrozenCoins Coins  `json:"frozen"`
	LockedCoins Coins  `json:"locked"`
	Flags       uint64 `json:"flags"`
}

type BaseAccount struct {
	Address       cosmos.AccAddress `json:"address"`
	Coins         Coins             `json:"coins"`
	PubKey        crypto.PubKey     `json:"public_key"`
	AccountNumber int64             `json:"account_number"`
	Sequence      int64             `json:"sequence"`
}

// Coin def
type Coin struct {
	Denom  string `json:"denom"`
	Amount int64  `json:"amount"`
}

// Coins def
type Coins []Coin
