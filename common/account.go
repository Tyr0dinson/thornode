package common

type AccountCoin struct {
	Denom  string `json:"denom"`
	Amount int64  `json:"amount"`
}

type AccountCoins []AccountCoin

type Account struct {
	Sequence      int64
	AccountNumber int64
	Coins         AccountCoins
	HasMemoFlag   bool
}

func NewAccountCoin(denom string, amt int64) AccountCoin {
	return AccountCoin{
		Denom:  denom,
		Amount: amt,
	}
}

// NewAccount
func NewAccount(sequence, accountNumber int64, coins AccountCoins, hasMemoFlag bool) Account {
	return Account{
		Sequence:      sequence,
		AccountNumber: accountNumber,
		Coins:         coins,
		HasMemoFlag:   hasMemoFlag,
	}
}
