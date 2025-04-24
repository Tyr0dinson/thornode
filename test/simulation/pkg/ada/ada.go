package ada

import (
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
	cardano "github.com/kocubinski/gardano/address"
	adatx "github.com/kocubinski/gardano/tx"
	"github.com/rs/zerolog/log"
	utxocardano "github.com/utxorpc/go-codegen/utxorpc/v1alpha/cardano"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const testnetNetworkMagic = 42

var chainSyncOnce sync.Once

type Client struct {
	clientConn *ouroboros.Connection
	logger     *slog.Logger
	key        ed25519.PrivateKey
	address    cardano.Address
	protocol   *utxocardano.PParams

	broadcastLock sync.Mutex
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

	chainSyncOnce.Do(func() {
		n2cConn, err := net.Dial("tcp", n2cAddress)
		if err != nil {
			log.Fatal().Err(err).Msg("fail to dial n2c host")
		}
		conn, err := ouroboros.NewConnection(
			ouroboros.WithConnection(n2cConn),
			ouroboros.WithErrorChan(networkError),
			ouroboros.WithLogger(networkLogger),
			ouroboros.WithNetworkMagic(testnetNetworkMagic),
			ouroboros.WithKeepAlive(true),
			ouroboros.WithChainSyncConfig(chainsync.NewConfig(
				chainsync.WithRollForwardFunc(chainSyncRollForwardHandler),
				chainsync.WithRollBackwardFunc(chainSyncRollBackwardHandler))),
		)
		if err != nil {
			log.Fatal().Err(err).Msg("fail to create ouroboros connection")
		}
		err = conn.ChainSync().Client.Sync([]ocommon.Point{ocommon.NewPointOrigin()})
		if err != nil {
			log.Fatal().Err(err).Msg("fail to start chain sync")
		}
	})

	n2cConn, err := net.Dial("tcp", n2cAddress)
	if err != nil {
		return nil, fmt.Errorf("fail to dial n2c host: %w", err)
	}
	c.clientConn, err = ouroboros.NewConnection(
		ouroboros.WithConnection(n2cConn),
		ouroboros.WithErrorChan(networkError),
		ouroboros.WithLogger(networkLogger),
		ouroboros.WithNetworkMagic(testnetNetworkMagic),
		ouroboros.WithKeepAlive(true),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to create ouroboros connection: %w", err)
	}

	// fetch network protocol parameters, needed for tx signing
	protocolParams, err := c.clientConn.LocalStateQuery().Client.GetCurrentProtocolParams()
	if err != nil {
		return nil, fmt.Errorf("fail to get protocol parameters: %w", err)
	}
	c.protocol = protocolParams.Utxorpc()

	priv, err := keys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}
	c.key = priv.Key
	pub := c.key.Public().(ed25519.PublicKey)
	c.address, err = cardano.PaymentOnlyTestnetAddressFromPubkey(pub)
	if err != nil {
		return nil, fmt.Errorf("fail to create cardano address: %w", err)
	}

	fmt.Printf("cardano client created pub=%x address=%s\n", pub, c.address)

	return c, nil
}

func (c *Client) BroadcastTx(txBytes []byte) (string, error) {
	c.broadcastLock.Lock()
	defer c.broadcastLock.Unlock()

	tx, err := ledger.NewConwayTransactionFromCbor(txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to create transaction: %w", err)
	}
	txHash := tx.Hash().String()
	log.Info().Msgf("broadcasting transaction hash=%s bytes=%x", txHash, txBytes)

	era, err := c.clientConn.LocalStateQuery().Client.GetCurrentEra()
	if err != nil {
		return "", fmt.Errorf("failed to get current era: %w", err)
	}
	err = c.clientConn.LocalTxSubmission().Client.SubmitTx(uint16(era), txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to submit transaction bytes=%x: %w", txBytes, err)
	}

	// wait for tx to be included in a block
	start := time.Now()
	for {
		if time.Since(start) > 30*time.Second {
			return "", fmt.Errorf("timeout waiting for tx to be included in a block")
		}
		if _, ok := txInBlock.Load(txHash); ok {
			log.Info().Msgf("transaction included in block hash=%s", txHash)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return txHash, nil
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
	// balance *= 100 // 1e6 -> 1e8
	coin := common.NewCoin(common.ADAAsset, sdkmath.NewUint(balance))
	c.logger.Info("get account", "address", c.address.String(), "adaAddress", adaAddr.String(), "balance", balance)
	return &common.Account{
		Coins: common.Coins{coin},
	}, nil
}

func (c *Client) SignTx(tx simtypes.SimTx) ([]byte, error) {
	txBuilder := adatx.NewTxBuilder(c.protocol)
	toAddr, err := cardano.NewAddressFromBech32(tx.ToAddress.String())
	if err != nil {
		return nil, fmt.Errorf("fail to create address: %w", err)
	}
	// amount := tx.Coin.Amount.Quo(sdkmath.NewUint(100)) // 1e8 -> 1e6
	amount := tx.Coin.Amount
	txIns, err := c.getUtxoToSpend(amount.Uint64())
	if err != nil {
		return nil, fmt.Errorf("fail to get utxo: %w", err)
	}
	txBuilder.AddInputs(txIns...)
	txBuilder.AddOutputs(adatx.NewTxOutput(toAddr, amount.Uint64()))
	if err = txBuilder.SetMemo(tx.Memo); err != nil {
		return nil, fmt.Errorf("fail to set memo: %w", err)
	}
	if err = txBuilder.AddChangeIfNeeded(c.address); err != nil {
		return nil, fmt.Errorf("fail to add change: %w", err)
	}
	if err = txBuilder.CalculateFee(); err != nil {
		return nil, fmt.Errorf("fail to calculate fee: %w", err)
	}
	signedTx, err := txBuilder.Sign([]ed25519.PrivateKey{c.key})
	if err != nil {
		return nil, fmt.Errorf("fail to build tx: %w", err)
	}
	return signedTx.Bytes()
}

func (c *Client) getUtxoToSpend(total uint64) ([]adatx.TxInput, error) {
	// 1 tx at a time globally.. better to lock on addr iff an in-flight utxo is seen
	// in response below
	c.broadcastLock.Lock()
	defer c.broadcastLock.Unlock()
	addr, err := ledger.NewAddress(c.address.String())
	if err != nil {
		return nil, fmt.Errorf("fail to create address: %w", err)
	}
	queryClient := c.clientConn.LocalStateQuery().Client
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
