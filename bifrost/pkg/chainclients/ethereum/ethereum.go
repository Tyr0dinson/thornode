package ethereum

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	tssp "gitlab.com/thorchain/tss/go-tss/tss"

	"gitlab.com/thorchain/thornode/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/bifrost/config"
	"gitlab.com/thorchain/thornode/bifrost/metrics"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/ethereum/types"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/bifrost/tss"
	"gitlab.com/thorchain/thornode/common"
)

const (
	ZeroContract        = "0x0000000000000000000000000000000000000000"
	ContractCreationGas = 500000
	Confirmations       = 12
	TransferMethod      = "transfer"
	ABIPath             = "contract/vault.json"
	ByteCodePath        = "contract/bytecode"
)

// Client is a structure to sign and broadcast tx to Ethereum chain used by signer mostly
type Client struct {
	logger          zerolog.Logger
	cfg             config.ChainConfiguration
	chainID         types.ChainID
	pk              common.PubKey
	client          *ethclient.Client
	kw              *KeySignWrapper
	ethScanner      *BlockScanner
	thorchainBridge *thorclient.ThorchainBridge
	blockScanner    *blockscanner.BlockScanner
	contractAddress ecommon.Address
	contractABI     *abi.ABI
}

// NewClient create new instance of Ethereum client
func NewClient(thorKeys *thorclient.Keys, cfg config.ChainConfiguration, server *tssp.TssServer, thorchainBridge *thorclient.ThorchainBridge, m *metrics.Metrics) (*Client, error) {
	tssKm, err := tss.NewKeySign(server)
	if err != nil {
		return nil, fmt.Errorf("fail to create tss signer: %w", err)
	}

	priv, err := thorKeys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}

	pk, err := common.NewPubKeyFromCrypto(priv.PubKey())
	if err != nil {
		return nil, fmt.Errorf("fail to get pub key: %w", err)
	}

	if thorchainBridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}

	ethPrivateKey, err := getETHPrivateKey(priv)
	if err != nil {
		return nil, err
	}

	keysignWrapper := &KeySignWrapper{
		privKey:       ethPrivateKey,
		pubKey:        pk,
		tssKeyManager: tssKm,
		logger:        log.With().Str("module", "local_signer").Str("chain", common.ETHChain.String()).Logger(),
	}
	ethClient, err := ethclient.Dial(cfg.RPCHost)
	if err != nil {
		return nil, err
	}
	c := &Client{
		logger:          log.With().Str("module", "ethereum").Logger(),
		cfg:             cfg,
		client:          ethClient,
		pk:              pk,
		kw:              keysignWrapper,
		thorchainBridge: thorchainBridge,
	}
	c.InitChainID()

	var path string // if not set later, will in memory storage
	if len(c.cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", c.cfg.BlockScanner.DBPath, c.cfg.BlockScanner.ChainID)
	}
	storage, err := blockscanner.NewBlockScannerStorage(path)
	if err != nil {
		return c, fmt.Errorf("fail to create blockscanner storage: %w", err)
	}

	c.ethScanner, err = NewBlockScanner(c.cfg.BlockScanner, storage, c.chainID, c.client, m)
	if err != nil {
		return c, fmt.Errorf("fail to create eth block scanner: %w", err)
	}

	c.blockScanner, err = blockscanner.NewBlockScanner(c.cfg.BlockScanner, storage, m, c.thorchainBridge, c.ethScanner)
	if err != nil {
		return c, fmt.Errorf("fail to create block scanner: %w", err)
	}

	if err = c.InitContract(); err != nil {
		return c, fmt.Errorf("fail to create contract: %w", err)
	}
	return c, nil
}

func (c *Client) InitContract() error {
	fromAddr := c.GetAddress(c.pk)
	gasPrice := c.ethScanner.GetGasPrice()

	contractByteCode, err := ioutil.ReadFile(ByteCodePath)
	if err != nil {
		return err
	}
	createdTx := etypes.NewContractCreation(0, big.NewInt(0), ContractCreationGas, gasPrice, contractByteCode)

	height, err := c.GetHeight()
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get current Ethereum block height")
		return fmt.Errorf("fail to get block height: %w", err)
	}
	rawTx, err := c.sign(createdTx, fromAddr, c.pk, height, stypes.TxOutItem{})
	if err != nil || len(rawTx) == 0 {
		return fmt.Errorf("fail to sign message: %w", err)
	}
	hash, err := GetTxHash(rawTx)
	if err != nil {
		return fmt.Errorf("fail to get tx hash: %w %s", err, hash)
	}

	if err = c.BroadcastTx(stypes.TxOutItem{VaultPubKey: c.pk}, rawTx); err != nil {
		return fmt.Errorf("fail to broadcast contract: %w", err)
	}

	if c.chainID != types.Localnet {
		for {
			currentHeight, err := c.GetHeight()
			if err != nil {
				c.logger.Error().Err(err).Msg("fail to get current Ethereum block height")
				continue
			}
			if currentHeight > height+Confirmations {
				break
			}
		}
	}

	receipt, err := c.getReceipt(hash)
	if err != nil {
		return fmt.Errorf("fail to fetch receipt for tx: %w", err)
	}
	c.contractAddress = receipt.ContractAddress
	if len(c.contractAddress.String()) == 0 || c.contractAddress.String() == ZeroContract {
		return fmt.Errorf("incorrect contract creation")
	}
	c.ethScanner.ContractAddress = c.contractAddress
	vaultJSON, err := ioutil.ReadFile(ABIPath)
	if err != nil {
		return err
	}
	vaultABI := &abi.ABI{}
	err = vaultABI.UnmarshalJSON(vaultJSON)
	if err != nil {
		return err
	}
	c.ethScanner.ContractABI = vaultABI
	c.contractABI = vaultABI
	return nil
}

// GetTxHash return hex formatted value of tx hash
func GetTxHash(encodedTx []byte) (string, error) {
	tx := &etypes.Transaction{}
	if err := tx.UnmarshalJSON(encodedTx); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s", tx.Hash().Hex()), nil
}

func (c *Client) Start(globalTxsQueue chan stypes.TxIn, globalErrataQueue chan stypes.ErrataBlock) {
	c.blockScanner.Start(globalTxsQueue)
	c.ethScanner.globalErrataQueue = globalErrataQueue
}

func (c *Client) Stop() {
	c.blockScanner.Stop()
	c.client.Close()
}

func (c *Client) GetConfig() config.ChainConfiguration {
	return c.cfg
}

// IsTestNet determinate whether we are running on test net by checking the status
func (c *Client) InitChainID() {
	chainID, err := c.client.ChainID(context.Background())
	if err != nil {
		c.logger.Error().Err(err).Msg("Unable to get chain id")
		chainID = big.NewInt(types.Localnet)
	}
	c.chainID = types.ChainID(chainID.Int64())
	vByte = byte(int(vByte) + int(2*c.chainID))
	eipSigner = etypes.NewEIP155Signer(chainID)
}

func (c *Client) GetChain() common.Chain {
	return common.ETHChain
}

func (c *Client) GetHeight() (int64, error) {
	return c.ethScanner.GetHeight()
}

func (c *Client) createInput(token, receiver string, value *big.Int) ([]byte, error) {
	input, err := c.contractABI.Pack(TransferMethod, ecommon.HexToAddress(token), ecommon.HexToAddress(receiver), value)
	if err != nil {
		return nil, err
	}
	return input, nil
}

func (c *Client) getReceipt(hash string) (*etypes.Receipt, error) {
	return c.client.TransactionReceipt(context.Background(), ecommon.HexToHash(hash))
}

// GetAddress return current signer address, it will be bech32 encoded address
func (c *Client) GetAddress(poolPubKey common.PubKey) string {
	addr, err := poolPubKey.GetAddress(common.ETHChain)
	if err != nil {
		c.logger.Error().Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get pool address")
		return ""
	}
	return addr.String()
}

func (c *Client) GetGasFee(count uint64) common.Gas {
	return common.GetETHGasFee(big.NewInt(1), count)
}

func (c *Client) GetGasPrice() (*big.Int, error) {
	return c.client.SuggestGasPrice(context.Background())
}

func (c *Client) estimateGas(from string, tx *etypes.Transaction) (uint64, error) {
	return c.client.EstimateGas(context.Background(), ethereum.CallMsg{
		From:     ecommon.HexToAddress(from),
		To:       tx.To(),
		Gas:      tx.Gas(),
		GasPrice: tx.GasPrice(),
		Value:    tx.Value(),
		Data:     tx.Data(),
	})
}

func (c *Client) GetNonce(addr string) (uint64, error) {
	nonce, err := c.client.PendingNonceAt(context.Background(), ecommon.HexToAddress(addr))
	if err != nil {
		return 0, fmt.Errorf("fail to get account nonce: %w", err)
	}
	return nonce, nil
}

// SignTx sign the the given TxArrayItem
func (c *Client) SignTx(tx stypes.TxOutItem, height int64) ([]byte, error) {
	value := big.NewInt(0)
	token := ETHToken
	for _, coin := range tx.Coins {
		value.Add(value, coin.Amount.BigInt())
		parts := strings.Split(coin.Asset.Ticker.String(), "-")
		if len(parts) < 2 {
			continue
		}
		token = parts[1]
	}

	toAddr := tx.ToAddress.String()
	fromAddr := c.GetAddress(tx.VaultPubKey)

	if len(toAddr) == 0 || value.Uint64() == 0 {
		c.logger.Error().Msg("invalid tx params")
		return nil, nil
	}

	gasPrice := c.ethScanner.GetGasPrice()
	gasOut := big.NewInt(0)
	for _, coin := range tx.MaxGas {
		gasOut.Add(gasOut, coin.Amount.BigInt())
	}
	gasOut.Div(gasOut, gasPrice)

	nonce, err := c.GetNonce(fromAddr)
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to fetch account nonce")
	}
	c.logger.Info().Uint64("nonce", nonce).Msg("account info")

	data, err := c.createInput(token, toAddr, value)
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to create input")
		return nil, err
	}
	createdTx := etypes.NewTransaction(nonce, c.contractAddress, big.NewInt(0), gasOut.Uint64(), gasPrice, data)
	estimatedGas, err := c.estimateGas(fromAddr, createdTx)
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to estimate gas")
		return nil, err
	}
	if estimatedGas > gasOut.Uint64() {
		err = fmt.Errorf("not enough max gas %d %d", estimatedGas, gasOut.Uint64())
		c.logger.Error().Err(err).Msg("fail to create tx")
		return nil, err
	}

	currentHeight, err := c.GetHeight()
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get current Ethereum block height")
		return nil, err
	}

	rawTx, err := c.sign(createdTx, fromAddr, tx.VaultPubKey, currentHeight, tx)
	if err != nil || len(rawTx) == 0 {
		return nil, fmt.Errorf("fail to sign message: %w", err)
	}
	return rawTx, nil
}

// sign is design to sign a given message with keysign party and keysign wrapper
func (c *Client) sign(tx *etypes.Transaction, from string, poolPubKey common.PubKey, height int64, txOutItem stypes.TxOutItem) ([]byte, error) {
	keySignParty, err := c.thorchainBridge.GetKeysignParty(poolPubKey)
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get keysign party")
		return nil, err
	}
	rawBytes, err := c.kw.Sign(tx, poolPubKey, keySignParty)
	if err == nil && rawBytes != nil {
		return rawBytes, nil
	}
	var keysignError tss.KeysignError
	if errors.As(err, &keysignError) {
		if len(keysignError.Blame.BlameNodes) == 0 {
			// TSS doesn't know which node to blame
			return nil, err
		}

		// key sign error forward the keysign blame to thorchain
		txID, err := c.thorchainBridge.PostKeysignFailure(keysignError.Blame, height, txOutItem.Memo, txOutItem.Coins)
		if err != nil {
			c.logger.Error().Err(err).Msg("fail to post keysign failure to thorchain")
			return nil, err
		} else {
			c.logger.Info().Str("tx_id", txID.String()).Msgf("post keysign failure to thorchain")
			return nil, fmt.Errorf("sent keysign failure to thorchain")
		}
	}
	c.logger.Error().Err(err).Msg("fail to sign tx")
	return nil, err
}

// GetAccount gets account by address in eth client
func (c *Client) GetAccount(pkey common.PubKey) (common.Account, error) {
	addr := c.GetAddress(pkey)
	nonce, err := c.GetNonce(addr)
	if err != nil {
		return common.Account{}, err
	}
	balance, err := c.client.BalanceAt(context.Background(), ecommon.HexToAddress(addr), nil)
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get account nonce: %w", err)
	}
	account := common.NewAccount(int64(nonce), 0, common.AccountCoins{common.AccountCoin{Amount: balance.Uint64(), Denom: "ETH.ETH"}})
	return account, nil
}

// BroadcastTx decodes tx using rlp and broadcasts too Ethereum chain
func (c *Client) BroadcastTx(stx stypes.TxOutItem, hexTx []byte) error {
	tx := &etypes.Transaction{}
	if err := tx.UnmarshalJSON(hexTx); err != nil {
		return err
	}
	if err := c.client.SendTransaction(context.Background(), tx); err != nil {
		return err
	}
	return nil
}
