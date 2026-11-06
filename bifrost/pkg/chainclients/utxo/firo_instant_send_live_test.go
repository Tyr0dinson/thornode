package utxo

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcutil"
	"github.com/rs/zerolog"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

// Driven in separate processes by instantsend.py, reopening both real journals.
func (s *FiroSuite) TestFiroLiveInstantSend(c *C) {
	endpoints := os.Getenv("FIRO_IS_RPC_URLS")
	if endpoints == "" {
		c.Skip("run test/integration/firo/instantsend.py")
	}
	ttypes.SetupConfigForTest()
	key, _ := btcec.PrivKeyFromBytes(btcec.S256(), []byte{1})
	pubkey, err := bech32AccountPubKey(key)
	c.Assert(err, IsNil)
	address, err := pubkey.GetAddress(common.FIROChain)
	c.Assert(err, IsNil)
	anchor, err := strconv.ParseInt(os.Getenv("FIRO_IS_ANCHOR"), 10, 64)
	c.Assert(err, IsNil)
	deposit, phase := os.Getenv("FIRO_IS_DEPOSIT"), os.Getenv("FIRO_IS_PHASE")
	var observations [][]byte
	urls := strings.Split(endpoints, ",")
	c.Assert(urls, HasLen, 2)
	for i, endpoint := range urls {
		u, err := url.Parse(endpoint)
		c.Assert(err, IsNil)
		user, pass := u.User.Username(), ""
		pass, _ = u.User.Password()
		u.User = nil
		r, err := rpc.NewClient(u.String(), user, pass, 0, 5*time.Second, common.FIROChain, zerolog.Nop())
		c.Assert(err, IsNil)
		path := filepath.Join(os.Getenv("FIRO_IS_DB"), strconv.Itoa(i))
		storage, err := blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
		c.Assert(err, IsNil)
		defer storage.Close()
		temporal, err := sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 1000)
		c.Assert(err, IsNil)
		bridge := &instantSendPolicyBridge{status: ttypes.NodeStatus_Active, values: map[string]int64{
			FiroInstantSendEnabledMimirKey: 1, FiroInstantSendMaxAmountMimirKey: 2 * common.One,
		}}
		client := &Client{
			rpc: r, log: zerolog.Nop(), bridge: bridge, temporalStorage: temporal,
			cfg: config.BifrostChainConfiguration{ChainID: common.FIROChain}, regexpRemoveTrailingZeros: regexp.MustCompile(`(00)+$`),
		}
		client.cfg.BlockScanner.DBPath = path
		client.cfg.UTXO.TransactionBatchSize = 100
		client.cfg.UTXO.MaxUTXOsToSpend = 10
		client.cfg.InstantSend.MaxMempoolAge = 2 * time.Minute
		client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{address}, FetchedAt: time.Now()})
		client.initInstantSend()
		// Production registers vaults before receiving payments. Core's rescan
		// discovers mined history, not mempool txs received before watch import.
		c.Assert(client.RegisterPublicKey(pubkey), IsNil)
		c.Assert(client.instantSend.refreshSettings(), IsNil)
		height, err := client.GetHeight()
		c.Assert(err, IsNil)
		fetcher := &dashHealthFetcher{}
		fetcher.height.Store(height)
		stop := startInstantSendWarmupScanner(c, client, fetcher)
		defer stop()
		switch phase {
		case "init":
			admission, err := client.instantSend.prepareAllowance(nil)
			c.Assert(err, IsNil)
			c.Assert(admission.allowed, Equals, false)
			state, err := temporal.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Started, Equals, true)
			c.Assert(state.Reserved, Equals, uint64(0))
		case "observe":
			raw, err := r.GetRawTransactionVerboseFIRO(deposit)
			c.Assert(err, IsNil)
			c.Assert(raw.InstantLock, Equals, true)
			c.Assert(raw.ChainLock, Equals, false)
			c.Assert(raw.Confirmations, Equals, uint64(0))
			_, err = client.FetchMemPool(height)
			c.Assert(err, IsNil)
			batch, err := client.FetchMemPool(height)
			c.Assert(err, IsNil)
			c.Assert(batch.TxArray, HasLen, 1)
			c.Assert(batch.MemPool, Equals, false)
			item := batch.TxArray[0]
			c.Assert(item.Tx, Equals, deposit)
			c.Assert(item.BlockHeight, Equals, anchor)
			c.Assert(item.Memo, Equals, "NOOP")
			c.Assert(item.Coins.GetCoin(common.FIROAsset).Amount.Uint64(), Equals, uint64(common.One))
			encoded, err := json.Marshal(item) // callbacks are local; compare signed observation fields
			c.Assert(err, IsNil)
			observations = append(observations, encoded)
			if i == 0 {
				// Model a durable Observer handoff for one client. The Observer package
				// tests exercise its shared storage and callback ordering.
				c.Assert(storage.GetInternalDb().Put([]byte("test-observer/"+deposit), encoded, &opt.WriteOptions{Sync: true}), IsNil)
				item.OnObservedTxStored()
			} // client 1 deliberately loses the handoff and must use the block fallback
			state, err := temporal.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Reserved, Equals, uint64(common.One))
			// Recognition of locked assets remains independent of the local switch.
			bridge.values[FiroInstantSendEnabledMimirKey] = 0
			c.Assert(client.instantSend.refreshSettings(), IsNil)
			account, err := client.getAccountFIRO(pubkey)
			c.Assert(err, IsNil)
			c.Assert(account.Coins.GetCoin(common.FIROAsset).Amount.Uint64(), Equals, uint64(common.One))
			_, err = client.getUtxoToSpendFIRO(pubkey, btcutil.Amount(common.One/2), 0)
			c.Assert(err, ErrorMatches, ".*insufficient confirmed FIRO inputs.*")
		case "settle":
			bridge.values[FiroInstantSendEnabledMimirKey] = 0
			c.Assert(client.instantSend.refreshSettings(), IsNil)
			_, err := client.instantSend.prepareAllowance(nil)
			c.Assert(err, IsNil)
			state, err := temporal.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Reserved, Equals, uint64(0))
			raw, err := r.GetRawTransactionVerboseFIRO(deposit)
			c.Assert(err, IsNil)
			block, err := client.getBlockFIRO(raw.Height)
			c.Assert(err, IsNil)
			batch, err := client.extractTxs(block)
			c.Assert(err, IsNil)
			if i == 0 {
				c.Assert(batch.TxArray, HasLen, 0) // acknowledged observation survived restart
				stored, readErr := storage.GetInternalDb().Get([]byte("test-observer/"+deposit), nil)
				c.Assert(readErr, IsNil)
				c.Assert(len(stored) > 0, Equals, true)
			} else {
				c.Assert(batch.TxArray, HasLen, 1) // lost handoff recovers at the mined height
				c.Assert(batch.TxArray[0].Tx, Equals, deposit)
				c.Assert(batch.TxArray[0].BlockHeight, Equals, raw.Height)
			}
			batch, err = client.extractTxs(block)
			c.Assert(err, IsNil)
			c.Assert(batch.TxArray, HasLen, 0)
		default:
			c.Fatalf("unknown FIRO test phase %s", phase)
		}
	}
	if phase == "observe" {
		c.Assert(observations[0], DeepEquals, observations[1])
	}
}
