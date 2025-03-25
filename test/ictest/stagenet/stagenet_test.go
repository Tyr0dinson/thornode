package stagenet

import (
	"fmt"
	"os"
	"testing"

	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/Peersyst/xrpl-go/xrpl/wallet"
	"github.com/stretchr/testify/require"

	sdkmath "cosmossdk.io/math"
)

// Send XRP from Lper to Swapper
func TestSendXrp(t *testing.T) {
	rpcConfig, err := rpc.NewClientConfig("https://xrplcluster.com/")
	//rpcConfig, err := rpc.NewClientConfig("https://s.altnet.rippletest.net:51234/")
	require.NoError(t, err)
	r := rpc.NewClient(rpcConfig)

	lpSeed := os.Getenv("XRP_LP_SEED")
	require.NotEmpty(t, lpSeed)

	swapSeed := os.Getenv("XRP_SWAP_SEED")
	require.NotEmpty(t, swapSeed)

	lpWallet, err := wallet.FromSeed(lpSeed, "")
	require.NoError(t, err)

	swapWallet, err := wallet.FromSeed(swapSeed, "")
	require.NoError(t, err)

	txHash, err := XrpSendFundsWithNote(r, lpWallet, swapWallet.ClassicAddress.String(), 5000000, "")
	require.NoError(t, err)

	fmt.Println("txHash:", txHash)
}

// Send XRP from Swapper to Lper
func TestSendXrpBack(t *testing.T) {
	rpcConfig, err := rpc.NewClientConfig("https://xrplcluster.com/")
	//rpcConfig, err := rpc.NewClientConfig("https://s.altnet.rippletest.net:51234/")
	require.NoError(t, err)
	r := rpc.NewClient(rpcConfig)


	swapSeed := os.Getenv("XRP_SWAP_SEED")
	require.NotEmpty(t, swapSeed)

	swapWallet, err := wallet.FromSeed(swapSeed, "")
	require.NoError(t, err)

	txHash, err := XrpSendFundsWithNote(r, swapWallet, "r4KYDZBbcAaJJ5MQPwRR9apJ2p5EBz8bq8", 1000000, "")
	require.NoError(t, err)

	fmt.Println("txHash:", txHash)
}

// Send atom from Lper to Swapper
func TestSendAtom(t *testing.T) {

}

// Send atom from Swapper to Lper
func TestSendAtomBack(t *testing.T) {

}

// Fund thorchain LPer
func TestFundThorchainLPer(t *testing.T) {
	fmt.Println("thornode tx thorchain send sthor1685296z3g85jfc0nsaame5hwjz52406q9wczr8 1000000000000rune --from stagenet-master --chain-id thorchain-stagenet-2 --node http://localhost:27147")
	// thornode q bank balances sthor1685296z3g85jfc0nsaame5hwjz52406q9wczr8 --node http://localhost:27147
}

// Fund thorchain Swapper
func TestFundThorchainSwapper(t *testing.T) {
	fmt.Println("thornode tx thorchain send sthor18vwkl0m5mlnrpxvwyvql08g6ke4hkpytwj8ffd 1000000000000rune --from stagenet-master --chain-id thorchain-stagenet-2 --node http://localhost:27147")
	// thornode q bank balances sthor18vwkl0m5mlnrpxvwyvql08g6ke4hkpytwj8ffd --node http://localhost:27147
}

// Fund gaia pool (dual lp)
// thornode tx thorchain mimir ChurnInterval --from stagenet-master --chain-id <your-chain-id> --node http://localhost:27147 -- 100
func TestFundGaiaLP(t *testing.T) {
	runeAmount := 150
	thorchainMemo := "+:GAIA.ATOM:cosmos1egl3el03ttszyw2f5wps7u2ypq2jxwjzkt3ye2"
	thorchainCmd := fmt.Sprintf("thornode tx thorchain deposit %d rune %s --from stagenet-lp --chain-id thorchain-stagenet-2 --node http://localhost:27147", runeAmount*100000000, thorchainMemo)
	fmt.Println("Run this cmd:", thorchainCmd)

	vaultAddr, _, err := APIGetInboundAddress("GAIA")
	require.NoError(t, err)

	gaiaMemo := "+:GAIA.ATOM:sthor1685296z3g85jfc0nsaame5hwjz52406q9wczr8"
	fmt.Println("Send atom to this vault:", vaultAddr, "with this memo", gaiaMemo)
}

// Fund xrp pool (dual lp)
func TestFundXrpLP(t *testing.T) {
	runeAmount := 300
	thorchainMemo := "+:XRP.XRP:rLYdNBsBbcC72VKXXimYVh2AKxou3vbgA8"
	thorchainCmd := fmt.Sprintf("thornode tx thorchain deposit %d rune %s --from stagenet-lp --chain-id thorchain-stagenet-2 --node http://localhost:27147", runeAmount*100000000, thorchainMemo)
	fmt.Println("Run this cmd:", thorchainCmd)

	vaultAddr, _, err := APIGetInboundAddress("XRP")
	require.NoError(t, err)

	xrpMemo := "+:XRP.XRP:sthor1685296z3g85jfc0nsaame5hwjz52406q9wczr8"
	fmt.Println("Send atom to this vault:", vaultAddr, "with this memo", xrpMemo)

	rpcConfig, err := rpc.NewClientConfig("https://xrplcluster.com/")
	//rpcConfig, err := rpc.NewClientConfig("https://s.altnet.rippletest.net:51234/")
	require.NoError(t, err)
	r := rpc.NewClient(rpcConfig)

	lpSeed := os.Getenv("XRP_LP_SEED")
	require.NotEmpty(t, lpSeed)

	lpWallet, err := wallet.FromSeed(lpSeed, "")
	require.NoError(t, err)

	txHash, err := XrpSendFundsWithNote(r, lpWallet, vaultAddr, 60000000, xrpMemo)
	require.NoError(t, err)

	fmt.Println("txHash:", txHash)
}

// Swap atom to xrp
func TestSwapGaiaToXrp(t *testing.T) {
	swapAmount := 40000000 // 1 atom
	swapQuote, err := APIGetSwapQuote("GAIA.ATOM", "XRP.XRP", sdkmath.NewUint(uint64(swapAmount)))
	require.NoError(t, err)

	fmt.Println("Expected amount out:", swapQuote.ExpectedAmountOut)

	vaultAddr, _, err := APIGetInboundAddress("GAIA")
	require.NoError(t, err)

	gaiaMemo := "=:XRP.XRP:r4KYDZBbcAaJJ5MQPwRR9apJ2p5EBz8bq8"
	fmt.Println("Send atom to this vault:", vaultAddr, "with this memo", gaiaMemo)
}

// Swap xrp to atom
func TestSwapXrpToGaia(t *testing.T) {
	swapAmount := 120000000 // 3.4 XRP
	swapQuote, err := APIGetSwapQuote("XRP.XRP", "GAIA.ATOM", sdkmath.NewUint(uint64(swapAmount)))
	require.NoError(t, err)

	fmt.Println("Expected amount out:", swapQuote.ExpectedAmountOut)

	vaultAddr, _, err := APIGetInboundAddress("XRP")
	require.NoError(t, err)

	xrpMemo := "=:GAIA.ATOM:cosmos19rdz5sm4qdp50sj86j0va2ww0aw4f65p5xm9lh"
	fmt.Println("Send atom to this vault:", vaultAddr, "with this memo ", xrpMemo)
	
	rpcConfig, err := rpc.NewClientConfig("https://xrplcluster.com/")
	//rpcConfig, err := rpc.NewClientConfig("https://s.altnet.rippletest.net:51234/")
	require.NoError(t, err)
	r := rpc.NewClient(rpcConfig)

	swapSeed := os.Getenv("XRP_SWAP_SEED")
	require.NotEmpty(t, swapSeed)

	swapWallet, err := wallet.FromSeed(swapSeed, "")
	require.NoError(t, err)

	txHash, err := XrpSendFundsWithNote(r, swapWallet, vaultAddr, 1100000, xrpMemo)
	require.NoError(t, err)

	fmt.Println("txHash:", txHash)
}
