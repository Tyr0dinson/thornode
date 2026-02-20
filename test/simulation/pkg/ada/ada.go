package ada

import (
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	sdkmath "cosmossdk.io/math"
	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
	"github.com/rs/zerolog/log"
	utxocardano "github.com/utxorpc/go-codegen/utxorpc/v1alpha/cardano"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/cardano"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const testnetNetworkMagic = 42

type Client struct {
	n2c      *ouroboros.Connection
	mtx      sync.Mutex
	logger   *slog.Logger
	key      ed25519.PrivateKey
	address  lcommon.Address
	protocol *utxocardano.PParams
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

	var err error
	c.n2c, err = ouroboros.NewConnection(
		ouroboros.WithErrorChan(networkError),
		ouroboros.WithLogger(networkLogger),
		ouroboros.WithNetworkMagic(testnetNetworkMagic),
	)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create ouroboros connection")
	}

	err = c.n2c.Dial("tcp", n2cAddress)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to dial ouroboros node")
	}

	// fetch network protocol parameters, needed for tx signing
	client := c.client()
	protocolParams, err := client.GetCurrentProtocolParams()
	c.release()
	if err != nil {
		return nil, fmt.Errorf("fail to get protocol parameters: %w", err)
	}
	c.protocol, err = protocolParams.Utxorpc()
	if err != nil {
		return nil, fmt.Errorf("fail to get utxorpc params: %w", err)
	}

	priv, err := keys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}
	c.key = priv.Key
	pub := c.key.Public().(ed25519.PublicKey)

	c.address, err = cardano.AddressFromBytes(pub)
	if err != nil {
		return nil, fmt.Errorf("fail to create cardano address: %w", err)
	}

	fmt.Printf("cardano client created pub=%x address=%s\n", pub, c.address)

	return c, nil
}

func (c *Client) BroadcastTx(txBytes []byte) (string, error) {
	tx, err := ledger.NewConwayTransactionFromCbor(txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to create transaction: %w", err)
	}

	txHash := tx.Hash().String()
	log.Info().Str("hash", txHash).Msg("broadcasting transaction")

	err = c.n2c.LocalTxSubmission().Client.SubmitTx(
		ledger.TxTypeConway, txBytes,
	)
	if err != nil {
		return "", fmt.Errorf("failed to submit transaction bytes=%x: %w", txBytes, err)
	}

	// wait for tx to be included in a block
	start := time.Now()
	for {
		if time.Since(start) > 30*time.Second {
			return "", fmt.Errorf("timeout waiting for tx to be included in a block")
		}

		client := c.client()
		utxos, err := client.GetUTxOByAddress([]lcommon.Address{
			tx.Outputs()[0].Address()},
		)
		c.release()

		if err != nil {
			continue
		}
		for id := range utxos.Results {
			if id.Hash.String() == txHash {
				return txHash, nil // tx is on-chain
			}
		}

		time.Sleep(250 * time.Millisecond)
	}
	return txHash, nil
}

func (c *Client) GetAccount(_ *common.PubKey) (*common.Account, error) {
	client := c.client()
	res, err := client.GetUTxOByAddress([]ledger.Address{c.address})
	c.release()
	if err != nil {
		return &common.Account{}, fmt.Errorf("fail to query address: %w", err)
	}
	var balance uint64
	for _, utxo := range res.Results {
		balance += utxo.Amount().Uint64()
	}

	balance *= 100 // 1e6 => 1e8

	coin := common.NewCoin(common.ADAAsset, sdkmath.NewUint(balance))
	c.logger.Info("get account", "address", c.address.String(), "balance", balance)
	return &common.Account{
		Coins: common.Coins{coin},
	}, nil
}

func (c *Client) SignTx(simTx simtypes.SimTx) ([]byte, error) {
	fmt.Println("SignTx", simTx)

	coin := simTx.Coin

	if coin.IsEmpty() {
		err := fmt.Errorf("no ADA coins found")
		c.logger.Error("error", err)
		return nil, err
	}

	fromAddress, err := ledger.NewAddress(c.address.String())
	if err != nil {
		c.logger.Error("failed to get address", "error", err)
		return nil, err
	}

	toAddress, err := ledger.NewAddress(simTx.ToAddress.String())
	if err != nil {
		c.logger.Error("failed to get address", "error", err)
	}

	client := c.client()
	result, err := client.GetUTxOByAddress([]ledger.Address{fromAddress})
	c.release()
	if err != nil {
		c.logger.Error("failed to get utxos by address", "error", err)
		return nil, err
	}

	minFeeA := uint64(0)
	minFeeB := uint64(500_000)

	tx, err := cardano.CreateConwayTx(
		fromAddress,
		toAddress,
		coin.Amount.Uint64()/100, // 1e8 => 1e6
		minFeeA,
		minFeeB,
		simTx.Memo,
		result.Results,
	)

	if err != nil {
		c.logger.Error("fail to create conway tx", "error", err)
		return nil, err
	}

	encoded, err := cbor.Encode(tx.Body)
	hash := lcommon.Blake2b256Hash(encoded)

	signature := ed25519.Sign(c.key, hash[:])

	pub := c.key.Public().(ed25519.PublicKey)

	tx.WitnessSet.VkeyWitnesses = cbor.NewSetType(
		[]lcommon.VkeyWitness{
			{
				Vkey:      pub,
				Signature: signature,
			},
		},
		false,
	)

	signed, err := cbor.Encode(tx)
	if err != nil {
		c.logger.Error("failed to encode transaction", "error", err)
		return nil, err
	}

	return signed, nil
}

func (c *Client) release() {
	err := c.n2c.LocalStateQuery().Client.Release()
	if err != nil {
		c.logger.Error("failed to release client", "error", err)
	}
	c.mtx.Unlock()
}

func (c *Client) client() *localstatequery.Client {
	c.mtx.Lock()
	return c.n2c.LocalStateQuery().Client
}
