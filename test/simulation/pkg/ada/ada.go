package ada

import (
	"crypto/ed25519"
	"fmt"
	"time"

	sdkmath "cosmossdk.io/math"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	utxocardano "github.com/utxorpc/go-codegen/utxorpc/v1alpha/cardano"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/cardano"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

type Client struct {
	n2c      cardano.ClientWrapper
	key      ed25519.PrivateKey
	logger   zerolog.Logger
	address  lcommon.Address
	protocol *utxocardano.PParams
}

func NewConstructor(n2cAddress string) simtypes.LiteChainClientConstructor {
	return func(_ common.Chain, keys *thorclient.Keys) (simtypes.LiteChainClient, error) {
		return NewClient(n2cAddress, keys)
	}
}

func NewClient(n2cAddress string, keys *thorclient.Keys) (*Client, error) {
	c := &Client{
		logger: log.With().Logger(),
	}

	var err error
	c.n2c, err = cardano.NewClientConnection(c.logger, n2cAddress)
	if err != nil {
		return nil, fmt.Errorf("fail to create ouroboros connection: %w", err)
	}

	// fetch network protocol parameters, needed for tx signing
	protocolParams, err := c.n2c.GetCurrentProtocolParams()
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

	err = c.n2c.SubmitTx(ledger.TxTypeConway, txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to submit transaction bytes=%x: %w", txBytes, err)
	}

	// wait for tx to be included in a block
	start := time.Now()
	for {
		if time.Since(start) > 30*time.Second {
			return "", fmt.Errorf("timeout waiting for tx to be included in a block")
		}

		utxos, err := c.n2c.GetUTxOByAddress([]lcommon.Address{
			tx.Outputs()[0].Address(),
		}, nil,
		)
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
}

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	var address ledger.Address
	if pk != nil {
		bz, err := pk.Bytes()
		if err != nil {
			return nil, err
		}
		address, err = cardano.AddressFromBytes(bz)
		if err != nil {
			return nil, err
		}
	} else {
		address = c.address
	}

	res, err := c.n2c.GetUTxOByAddress([]ledger.Address{address}, nil)
	if err != nil {
		return &common.Account{}, fmt.Errorf("fail to query address: %w", err)
	}
	var balance uint64
	for _, utxo := range res.Results {
		balance += utxo.Amount().Uint64()
	}

	balance *= 100 // 1e6 => 1e8

	coin := common.NewCoin(common.ADAAsset, sdkmath.NewUint(balance))
	return &common.Account{
		Coins: common.Coins{coin},
	}, nil
}

func (c *Client) SignTx(simTx simtypes.SimTx) ([]byte, error) {
	fmt.Println("SignTx", simTx)

	coin := simTx.Coin

	if coin.IsEmpty() {
		err := fmt.Errorf("no ADA coins found")
		c.logger.Error().Msg(err.Error())
		return nil, err
	}

	fromAddress, err := ledger.NewAddress(c.address.String())
	if err != nil {
		c.logger.Err(err).Msg("failed to get address")
		return nil, err
	}

	toAddress, err := ledger.NewAddress(simTx.ToAddress.String())
	if err != nil {
		c.logger.Err(err).Msg("failed to get address")
		return nil, err
	}

	result, err := c.n2c.GetUTxOByAddress([]ledger.Address{fromAddress}, nil)
	if err != nil {
		c.logger.Err(err).Msg("failed to get utxos by address")
		return nil, err
	}

	var utxos []cardano.Utxo
	for id, output := range result.Results {
		utxos = append(utxos, cardano.Utxo{
			Hash:   id.Hash.String(),
			Index:  id.Idx,
			Amount: output.Amount().Uint64(),
		})
	}

	minFeeA := uint64(0)
	minFeeB := uint64(200_000)

	tx, err := cardano.CreateConwayTx(
		fromAddress,
		toAddress,
		coin.Amount.Uint64()/100, // 1e8 => 1e6
		minFeeA,
		minFeeB,
		500_000,
		simTx.Memo,
		utxos,
	)
	if err != nil {
		c.logger.Err(err).Msg("failed to create conway tx")
		return nil, err
	}

	encoded, err := cbor.Encode(tx.Body)
	if err != nil {
		c.logger.Err(err).Msg("failed to encode tx")
		return nil, err
	}

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
		c.logger.Err(err).Msg("failed to encode transaction")
		return nil, err
	}

	return signed, nil
}
