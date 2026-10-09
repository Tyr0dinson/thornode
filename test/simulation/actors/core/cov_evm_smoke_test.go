package core

import (
	"math/big"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"
	etypes "github.com/ethereum/go-ethereum/core/types"

	"gitlab.com/thorchain/thornode/v3/common"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestCovEVMFakeDrivesRealClient(t *testing.T) {
	client, rpc := newCovEVMClient(t, common.ETHChain, 0)
	rpc.Balance = new(big.Int).Mul(big.NewInt(3), big.NewInt(1e18))
	acct, err := client.GetAccount(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := acct.Coins.GetCoin(common.ETHAsset).Amount.Uint64(); got != 3*common.One {
		t.Fatalf("eth balance = %d", got)
	}
	signed, err := client.SignTx(simtypes.SimTx{
		Chain:     common.ETHChain,
		ToAddress: "0x0000000000000000000000000000000000000001",
		Coin:      common.NewCoin(common.ETHAsset, sdkmath.NewUint(common.One)),
		Memo:      "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	txid, err := client.BroadcastTx(signed)
	if err != nil || txid == "" {
		t.Fatalf("broadcast: %q %v", txid, err)
	}
	if len(rpc.SentTxs(t)) != 1 || rpc.CallCount("eth_getTransactionReceipt") != 1 {
		t.Fatal("expected one sent tx and one receipt lookup")
	}
	from, err := etypes.Sender(etypes.NewEIP155Signer(big.NewInt(1337)), rpc.SentTxs(t)[0])
	if err != nil {
		t.Fatal(err)
	}
	user, _ := newCovUser(t, 0, nil)
	if !strings.EqualFold(user.Address(common.ETHChain).String(), from.Hex()) {
		t.Fatalf("evm client keys must match the cov user: %s vs %s", user.Address(common.ETHChain), from.Hex())
	}
}
