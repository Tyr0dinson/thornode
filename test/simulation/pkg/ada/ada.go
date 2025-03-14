package ada

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	sdkmath "cosmossdk.io/math"
	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	utxocardano "github.com/utxorpc/go-codegen/utxorpc/v1alpha/cardano"
	adatx "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/cardano"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cardano"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const testnetNetworkMagic = 42

type Client struct {
	clientConn *ouroboros.Connection
	logger     *slog.Logger
	key        ed25519.PrivateKey
	address    cardano.Address
	protocol   *utxocardano.PParams

	txWait  sync.WaitGroup
	txMu    sync.Mutex
	txWatch string

	inFlight map[string]struct{} // used to track in-flight transactions

	tip   ocommon.Point // used to track the latest block tip
	tipMu sync.Mutex
}

func NewConstructor(n2cAddress string) simtypes.LiteChainClientConstructor {
	return func(_ common.Chain, keys *thorclient.Keys) (simtypes.LiteChainClient, error) {
		return NewClient(n2cAddress, keys)
	}
}

func NewClient(n2cAddress string, keys *thorclient.Keys) (*Client, error) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	c := &Client{logger: logger}
	// create n2c connection
	n2cConn, err := net.Dial("tcp", n2cAddress)
	if err != nil {
		return nil, fmt.Errorf("fail to dial n2c host: %w", err)
	}
	networkError := make(chan error)

	go func() {
		for {
			err := <-networkError
			logger.Error(fmt.Sprintf("ouroboros network error: %s", err))
			time.Sleep(time.Millisecond * 500)
		}
	}()
	networkLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	c.clientConn, err = ouroboros.NewConnection(
		ouroboros.WithConnection(n2cConn),
		ouroboros.WithErrorChan(networkError),
		ouroboros.WithLogger(networkLogger),
		ouroboros.WithNetworkMagic(testnetNetworkMagic),
		ouroboros.WithKeepAlive(true),
		ouroboros.WithChainSyncConfig(chainsync.NewConfig(
			chainsync.WithRollForwardFunc(c.chainSyncRollForwardHandler),
			chainsync.WithRollBackwardFunc(c.chainSyncRollBackwardHandler))),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to create ouroboros n2c connection: %w", err)
	}

	// fetch network protocol parameters, needed for tx signing
	protocolParams, err := c.clientConn.LocalStateQuery().Client.GetCurrentProtocolParams()
	if err != nil {
		return nil, fmt.Errorf("fail to get protocol parameters: %w", err)
	}
	c.protocol = protocolParams.Utxorpc()

	privKey, err := keys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}
	var buf bytes.Buffer
	buf.Write(privKey.Bytes())
	_, c.key, err = ed25519.GenerateKey(&buf)
	if err != nil {
		return nil, fmt.Errorf("fail to generate ed25519 key")
	}
	pub := c.key.Public().(ed25519.PublicKey)
	c.address, err = cardano.PaymentOnlyTestnetAddressFromPubkey(pub)
	if err != nil {
		return nil, fmt.Errorf("fail to create cardano address: %w", err)
	}

	secpBech32, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, privKey.PubKey())
	if err != nil {
		return nil, fmt.Errorf("fail to bech32ify pubkey: %w", err)
	}
	pubBech32, err := cardano.NewThorPubKeyBech32(pub)
	if err != nil {
		return nil, fmt.Errorf("fail to get thor pub key bech32: %w", err)
	}
	cardano.SetEd25519(secpBech32, pubBech32)

	if c.address.String() == "addr_test1vpqcmkzt6wtvfwk7k2j6qcm5ry8l892ll5gj2lkmqkuttpg3znall" {
		err = c.clientConn.ChainSync().Client.Sync([]ocommon.Point{ocommon.NewPointOrigin()})
		if err != nil {
			return nil, fmt.Errorf("fail to start chain sync: %w", err)
		}
	}

	return c, nil
}

func (c *Client) chainSyncRollForwardHandler(
	ctx chainsync.CallbackContext,
	blockType uint,
	blockData any,
	tip chainsync.Tip,
) error {
	c.tipMu.Lock()
	defer c.tipMu.Unlock()
	c.tip = tip.Point
	switch block := blockData.(type) {
	case ledger.Block:
		c.logger.Info("block received",
			"slot", block.SlotNumber(),
			"height", block.BlockNumber(),
			"hash", block.Hash())
		for _, tx := range block.Transactions() {
			if tx.Hash() == c.txWatch {
				c.logger.Info("transaction included in block", "hash", tx.Hash())
				c.txWatch = ""
				c.txWait.Done()
			}
		}
	case ledger.BlockHeader:
		c.logger.Warn("block header, n2c excpects full block", "hash", block.Hash())
	}
	return nil
}

func (c *Client) chainSyncRollBackwardHandler(
	ctx chainsync.CallbackContext,
	point ocommon.Point,
	tip chainsync.Tip,
) error {
	c.tipMu.Lock()
	defer c.tipMu.Unlock()
	c.tip = tip.Point
	fmt.Printf("roll backward: point = (%d, %x), tip = (%d, %x)\n",
		point.Slot, point.Hash,
		tip.Point.Slot, tip.Point.Hash,
	)
	return nil
}

func (c *Client) BroadcastTx(txBytes []byte) (string, error) {
	tx, err := ledger.NewConwayTransactionFromCbor(txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to create transaction: %w", err)
	}
	c.txMu.Lock()
	defer c.txMu.Unlock()
	c.txWait.Add(1)
	c.txWatch = tx.Hash()

	era, err := c.clientConn.LocalStateQuery().Client.GetCurrentEra()
	if err != nil {
		return "", fmt.Errorf("failed to get current era: %w", err)
	}
	err = c.clientConn.LocalTxSubmission().Client.SubmitTx(uint16(era), txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to submit transaction bytes=%x: %w", txBytes, err)
	}

	// wait for tx to be included in a block
	c.txWait.Wait()
	return c.txWatch, nil
}

func (c *Client) GetAccount(_ *common.PubKey) (*common.Account, error) {
	adaAddr, err := ledger.NewAddress(c.address.String())
	if err != nil {
		return &common.Account{}, fmt.Errorf("fail to create address: %w", err)
	}
	queryClient := c.clientConn.LocalStateQuery().Client
	if err = queryClient.AcquireVolatileTip(); err != nil {
		return &common.Account{}, fmt.Errorf("fail to acquire tip: %w", err)
	}
	defer queryClient.Release()
	res, err := queryClient.GetUTxOByAddress([]ledger.Address{adaAddr})
	if err != nil {
		return &common.Account{}, fmt.Errorf("fail to query address: %w", err)
	}
	var balance uint64
	for _, utxo := range res.Results {
		balance += utxo.Amount()
	}
	coin := common.NewCoin(common.ADAAsset, sdkmath.NewUint(balance))
	c.logger.Info("get account", "address", c.address.String(), "adaAddress", adaAddr.String(), "balance", balance)
	return &common.Account{
		Coins: common.Coins{coin},
	}, nil
}

func (c *Client) SignTx(tx simtypes.SimTx) ([]byte, error) {
	txBuilder := adatx.NewTxBuilder(c.protocol, []ed25519.PrivateKey{c.key})
	toAddr, err := cardano.NewAddressFromBech32(tx.ToAddress.String())
	if err != nil {
		return nil, fmt.Errorf("fail to create address: %w", err)
	}
	txIns, err := c.getUtxoToSpend(tx.Coin.Amount.Uint64())
	if err != nil {
		return nil, fmt.Errorf("fail to get utxo: %w", err)
	}
	txBuilder.AddInputs(txIns...)
	txBuilder.AddOutputs(adatx.NewTxOutput(toAddr, tx.Coin.Amount.Uint64()))
	if err := txBuilder.SetMemo(tx.Memo); err != nil {
		return nil, fmt.Errorf("fail to set memo: %w", err)
	}
	if err := txBuilder.AddChangeIfNeeded(c.address); err != nil {
		return nil, fmt.Errorf("fail to add change: %w", err)
	}
	signedTx, err := txBuilder.Build()
	if err != nil {
		return nil, fmt.Errorf("fail to build tx: %w", err)
	}
	return signedTx.Bytes()
}

func (c *Client) getUtxoToSpend(total uint64) ([]adatx.TxInput, error) {
	// 1 tx at a time globally.. better to lock on addr iff an in-flight utxo is seen
	// in response below
	c.txWait.Wait()
	addr, err := ledger.NewAddress(c.address.String())
	if err != nil {
		return nil, fmt.Errorf("fail to create address: %w", err)
	}
	queryClient := c.clientConn.LocalStateQuery().Client
	// c.tipMu.Lock()
	// tip := ocommon.NewPoint(c.tip.Slot, c.tip.Hash)
	// c.tipMu.Unlock()
	// c.logger.Info("querying utxo", "address", addr.String(), "slot", tip.Slot)
	if err := queryClient.AcquireVolatileTip(); err != nil {
		return nil, fmt.Errorf("fail to acquire tip: %w", err)
	}
	defer queryClient.Release()
	res, err := queryClient.GetUTxOByAddress([]ledger.Address{addr})
	if err != nil {
		return nil, fmt.Errorf("fail to query address: %w", err)
	}
	var txIns []adatx.TxInput
	var sum uint64
	for txId, utxo := range res.Results {
		txIns = append(txIns, adatx.TxInput{
			TxHash: txId.Hash[:],
			Index:  uint16(txId.Idx),
			Amount: utxo.Amount(),
		})
		c.logger.Info("utxo TxIn", "txId", fmt.Sprintf("%x#%d", txId.Hash[:], txId.Idx), "amount", utxo.Amount())
		sum += utxo.Amount()
		if sum >= total {
			break
		}
	}
	if sum < total {
		return nil, fmt.Errorf("insufficient balance")
	}
	return txIns, nil
}

var _ simtypes.LiteChainClient = &Client{}
