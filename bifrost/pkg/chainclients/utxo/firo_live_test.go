package utxo

import (
	"bytes"
	"encoding/hex"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Driven by test/integration/firo/chainlocks.py against a real six-node quorum.
func TestFiroLiveChainLocks(t *testing.T) {
	endpoint := os.Getenv("FIRO_TEST_RPC_URL")
	if endpoint == "" {
		t.Skip("run test/integration/firo/chainlocks.py")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal("invalid test RPC URL")
	}
	user, pass := "", ""
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
		u.User = nil
	}
	r, err := rpc.NewClient(u.String(), user, pass, 0, 5*time.Second, common.FIROChain, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{rpc: r, cfg: config.BifrostChainConfiguration{ChainID: common.FIROChain}}
	expected, err := strconv.ParseInt(os.Getenv("FIRO_TEST_LOCKED_HEIGHT"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	height, err := client.GetHeight()
	if expected < 0 {
		if err == nil || client.finality.Load() != nil {
			t.Fatal("accepted daemon without a ChainLock")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if height != expected {
		t.Fatalf("final height %d, expected %d", height, expected)
	}
	block, err := client.getBlockFIRO(height)
	if err != nil {
		t.Fatal(err)
	}
	if block.Height != height {
		t.Fatal("wrong finalized block")
	}
	state := client.finality.Load()
	if os.Getenv("FIRO_TEST_HEALTHY") == "true" && !state.isHealthyFIRO(time.Now(), height) {
		t.Fatal("caught-up finalized node is unhealthy")
	}
	if os.Getenv("FIRO_TEST_HEALTHY") == "false" && state.isHealthyFIRO(time.Now(), height) {
		t.Fatal("stalled ChainLock is healthy")
	}
	if state.blockHeight > height {
		_, err := client.getBlockFIRO(state.blockHeight)
		if err == nil {
			t.Fatal("scanned unlocked tip")
		}
	}
	if deposit := os.Getenv("FIRO_TEST_DEPOSIT"); deposit != "" {
		testFiroLivePayment(t, client, deposit, os.Getenv("FIRO_TEST_OUTBOUND_FILE"))
	}
}

func testFiroLivePayment(t *testing.T, client *Client, deposit, resultPath string) {
	t.Helper()
	ttypes.SetupConfigForTest()
	key, _ := btcec.PrivKeyFromBytes(btcec.S256(), []byte{1})
	pubkey, err := bech32AccountPubKey(key)
	if err != nil {
		t.Fatal(err)
	}
	address, err := pubkey.GetAddress(common.FIROChain)
	if err != nil {
		t.Fatal(err)
	}
	client.nodePubKey, client.nodePrivKey = pubkey, key
	client.bridge = &firoTestBridge{}
	client.log = zerolog.Nop()
	client.regexpRemoveTrailingZeros = regexp.MustCompile(`(00)+$`)
	client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{address}, FetchedAt: time.Now()})
	storage, err := blockscanner.NewBlockScannerStorage(t.TempDir(), config.LevelDBOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	client.signerCacheManager, err = signercache.NewSignerCacheManager(storage.GetInternalDb())
	if err != nil {
		t.Fatal(err)
	}
	client.cfg.UTXO.MaxSatsPerVByte = 9765
	client.cfg.UTXO.MaxUTXOsToSpend = 10
	client.cfg.SignerCacheExpiry = time.Hour
	client.updateNetworkInfo()
	err = client.registerAddressFIRO(address.String())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := client.rpc.GetRawTransactionVerboseFIRO(deposit)
	if err != nil {
		t.Fatal(err)
	}
	err = client.requireFiroTransactionFinal(raw)
	if err != nil {
		t.Fatal(err)
	}
	item, err := client.getTxInFIRO(&raw.TxRawResult, raw.Height, false, []common.Address{address}, make(map[string]*rpc.FiroTxResult))
	if err != nil || item == nil || item.Memo != "NOOP" || item.Coins.GetCoin(common.FIROAsset).Amount.Uint64() != common.One {
		t.Fatalf("deposit observation: %+v, %v", item, err)
	}
	account, err := client.getAccountFIRO(pubkey)
	if err != nil || account.Coins.GetCoin(common.FIROAsset).Amount.Uint64() != common.One {
		t.Fatalf("vault balance: %+v, %v", account, err)
	}
	destination, _ := firoutil.NewAddress(bytes.Repeat([]byte{3}, 20), firoutil.PubKeyHash, common.FIRONetwork())
	out := stypes.TxOutItem{
		Chain: common.FIROChain, VaultPubKey: pubkey, ToAddress: common.Address(destination.String()),
		Memo: "OUT:" + strings.Repeat("1", 64), GasRate: 25, Coins: common.Coins{common.NewCoin(common.FIROAsset, cosmos.NewUint(common.One/2))},
		MaxGas: common.Gas{common.NewCoin(common.FIROAsset, cosmos.NewUint(50000))},
	}
	signed, checkpoint, auto, err := client.SignTx(out, 1)
	if err != nil || len(signed) == 0 || checkpoint != nil || auto == nil {
		t.Fatalf("production SignTx failed: %v", err)
	}
	id, err := client.BroadcastTx(out, signed)
	if err != nil {
		t.Fatal(err)
	}
	if id != auto.Tx {
		t.Fatal("signer observation and broadcast ID differ")
	}
	var daemonHex string
	err = client.rpc.Call(&daemonHex, "getrawtransaction", id, false)
	if err != nil || daemonHex != hex.EncodeToString(signed) {
		t.Fatal("Core did not retain exact signed bytes")
	}
	err = os.WriteFile(resultPath, []byte(id), 0o600)
	if err != nil {
		t.Fatal(err)
	}
}
