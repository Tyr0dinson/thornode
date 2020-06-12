package ethereum

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	ecrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hashicorp/go-multierror"
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
	Zero                = "0x0000000000000000000000000000000000000000"
	ByteCode            = "608060405234801561001057600080fd5b50336000806101000a81548173ffffffffffffffffffffffffffffffffffffffff021916908373ffffffffffffffffffffffffffffffffffffffff160217905550610a5c806100606000396000f3fe60806040526004361061003f5760003560e01c806318aad847146100445780638077f5ab1461009b578063beabacc8146100ec578063bfe07da614610167575b600080fd5b34801561005057600080fd5b5061005961024c565b604051808273ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16815260200191505060405180910390f35b3480156100a757600080fd5b506100ea600480360360208110156100be57600080fd5b81019080803573ffffffffffffffffffffffffffffffffffffffff169060200190929190505050610271565b005b3480156100f857600080fd5b506101656004803603606081101561010f57600080fd5b81019080803573ffffffffffffffffffffffffffffffffffffffff169060200190929190803573ffffffffffffffffffffffffffffffffffffffff169060200190929190803590602001909291905050506103d9565b005b61024a6004803603606081101561017d57600080fd5b81019080803573ffffffffffffffffffffffffffffffffffffffff16906020019092919080359060200190929190803590602001906401000000008111156101c457600080fd5b8201836020820111156101d657600080fd5b803590602001918460018302840111640100000000831117156101f857600080fd5b91908080601f016020809104026020016040519081016040528093929190818152602001838380828437600081840152601f19601f8201169050808301925050505050505091929192905050506106a5565b005b6000809054906101000a900473ffffffffffffffffffffffffffffffffffffffff1681565b6000809054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff1614610333576040517f08c379a000000000000000000000000000000000000000000000000000000000815260040180806020018281038252600e8152602001807f4d7573742062652041736761726400000000000000000000000000000000000081525060200191505060405180910390fd5b806000806101000a81548173ffffffffffffffffffffffffffffffffffffffff021916908373ffffffffffffffffffffffffffffffffffffffff1602179055507f3d6017a0fe091729e8fa2ce2345c813da543a07f4ae7636bf382d5cf4f20068981604051808273ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16815260200191505060405180910390a150565b6000809054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff161461049b576040517f08c379a000000000000000000000000000000000000000000000000000000000815260040180806020018281038252600e8152602001807f4d7573742062652041736761726400000000000000000000000000000000000081525060200191505060405180910390fd5b600073ffffffffffffffffffffffffffffffffffffffff168273ffffffffffffffffffffffffffffffffffffffff16141561053e576040517f08c379a000000000000000000000000000000000000000000000000000000000815260040180806020018281038252601e8152602001807f4574686572206d7573742062652073656e742066726f6d20417367617264000081525060200191505060405180910390fd5b8173ffffffffffffffffffffffffffffffffffffffff1663a9059cbb84836040518363ffffffff1660e01b8152600401808373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16815260200182815260200192505050602060405180830381600087803b1580156105c557600080fd5b505af11580156105d9573d6000803e3d6000fd5b505050506040513d60208110156105ef57600080fd5b8101908080519060200190929190505050507fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef838383604051808473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020018373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff168152602001828152602001935050505060405180910390a1505050565b6000821161071b576040517f08c379a00000000000000000000000000000000000000000000000000000000081526004018080602001828103825260158152602001807f75736572206d7573742073656e6420617373657473000000000000000000000081525060200191505060405180910390fd5b600073ffffffffffffffffffffffffffffffffffffffff168373ffffffffffffffffffffffffffffffffffffffff161415610851578134146107c5576040517f08c379a00000000000000000000000000000000000000000000000000000000081526004018080602001828103825260188152602001807f75736572206d7573742073656e6420746865206574686572000000000000000081525060200191505060405180910390fd5b6000809054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163460405180600001905060006040518083038185875af1925050503d8060008114610844576040519150601f19603f3d011682016040523d82523d6000602084013e610849565b606091505b505050610949565b8273ffffffffffffffffffffffffffffffffffffffff166323b872dd3330856040518463ffffffff1660e01b8152600401808473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020018373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020018281526020019350505050602060405180830381600087803b15801561090c57600080fd5b505af1158015610920573d6000803e3d6000fd5b505050506040513d602081101561093657600080fd5b8101908080519060200190929190505050505b7f643e927b32d5bfd08eccd2fcbd97057ad413850f857a2359639114e8e8dd3d7b838383604051808473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16815260200183815260200180602001828103825283818151815260200191508051906020019080838360005b838110156109e55780820151818401526020810190506109ca565b50505050905090810190601f168015610a125780820380516001836020036101000a031916815260200191505b5094505050505060405180910390a150505056fea26469706673582212208cb64977b4c91fd065c4d8a53314dee39d322591fec151fa9e93014968e8c91364736f6c63430006050033"
	VaultJSON           = `[{"inputs":[],"stateMutability":"nonpayable","type":"constructor"},{"anonymous":false,"inputs":[{"indexed":false,"internalType":"address","name":"asset","type":"address"},{"indexed":false,"internalType":"uint256","name":"value","type":"uint256"},{"indexed":false,"internalType":"string","name":"memo","type":"string"}],"name":"Deposit","type":"event"},{"anonymous":false,"inputs":[{"indexed":false,"internalType":"address","name":"asgard","type":"address"}],"name":"NewAsgard","type":"event"},{"anonymous":false,"inputs":[{"indexed":false,"internalType":"address","name":"to","type":"address"},{"indexed":false,"internalType":"address","name":"token","type":"address"},{"indexed":false,"internalType":"uint256","name":"value","type":"uint256"}],"name":"Transfer","type":"event"},{"inputs":[],"name":"asgard","outputs":[{"internalType":"addresspayable","name":"","type":"address"}],"stateMutability":"view","type":"function"},{"inputs":[{"internalType":"addresspayable","name":"asset","type":"address"},{"internalType":"uint256","name":"value","type":"uint256"},{"internalType":"string","name":"memo","type":"string"}],"name":"deposit","outputs":[],"stateMutability":"payable","type":"function"},{"inputs":[{"internalType":"address","name":"receiver","type":"address"},{"internalType":"address","name":"token","type":"address"},{"internalType":"uint256","name":"value","type":"uint256"}],"name":"transfer","outputs":[],"stateMutability":"nonpayable","type":"function"},{"inputs":[{"internalType":"addresspayable","name":"newAsgard","type":"address"}],"name":"updateAsgard","outputs":[],"stateMutability":"nonpayable","type":"function"}]`
	ContractCreationGas = uint64(5000000)
	Confirmations       = 12
	NewAsgardGas        = uint64(50000)
	TransferMethod      = "transfer"
	ETHTransferGas      = uint64(21000)
	MasterPrivKey       = "ef235aacf90d9f4aadd8c92e4b2562e1d9eb97f0df9ba3b508258739cb013db2"
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
	keySignPartyMgr *thorclient.KeySignPartyMgr
	contractAddress ecommon.Address
	contractABI     *abi.ABI
	contracts       map[common.PubKey]ecommon.Address
}

// NewClient create new instance of Ethereum client
func NewClient(thorKeys *thorclient.Keys, cfg config.ChainConfiguration, server *tssp.TssServer, thorchainBridge *thorclient.ThorchainBridge, m *metrics.Metrics, keySignPartyMgr *thorclient.KeySignPartyMgr) (*Client, error) {
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
		keySignPartyMgr: keySignPartyMgr,
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

	c.ethScanner, err = NewBlockScanner(c.cfg.BlockScanner, storage, c.chainID, c.client, c.thorchainBridge, m)
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

// waitBlocks waits for 12 confirmations
func (c *Client) waitBlocks(hash string) (*etypes.Receipt, error) {
	startHeight, err := c.GetHeight()
	if err != nil {
		return nil, err
	}
	if c.chainID != types.Localnet {
		for {
			time.Sleep(time.Millisecond * 100)
			height, err := c.GetHeight()
			if err != nil {
				c.logger.Error().Err(err).Msg("fail to get current Ethereum block height")
				continue
			}
			if height > startHeight+Confirmations {
				break
			}
		}
	}
	receipt, err := c.getReceipt(hash)
	for err != nil {
		time.Sleep(time.Millisecond * 100)
		receipt, err = c.getReceipt(hash)
	}
	return receipt, nil
}

// sendETH sends ETH
func (c *Client) sendETH(value *big.Int, toAddr string) error {
	createdTx := etypes.NewTransaction(0, ecommon.HexToAddress(toAddr), value, ETHTransferGas, c.GetGasPrice(), []byte{})
	pk, err := ecrypto.HexToECDSA(MasterPrivKey)
	if err != nil {
		return err
	}
	tx, err := etypes.SignTx(createdTx, eipSigner, pk)
	if err != nil {
		return fmt.Errorf("fail to sign tx: %w", err)
	}
	if err := c.client.SendTransaction(context.Background(), tx); err != nil {
		return err
	}
	_, err = c.waitBlocks(tx.Hash().Hex())
	if err != nil {
		return err
	}
	return nil
}

// sendETH sends ETH
func (c *Client) moveFunds(value *big.Int, toAddr string) error {
	from := c.GetAddress(c.pk)
	balance, err := c.GetBalance(from)
	if err != nil {
		return err
	}
	value := balance
	gasPrice := c.GetGasPrice()
	value = value.Sub(value, common.MakeETHGas(gasPrice, ETHTransferGas))
	nonce, err := c.GetNonce(from)
	if err != nil {
		return err
	}
	createdTx := etypes.NewTransaction(nonce, ecommon.HexToAddress(toAddr), value, ETHTransferGas, gasPrice, []byte{})
	
	currentHeight, err := c.GetHeight()
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get current Ethereum block height")
		return nil, err
	}

	rawTx, err := c.sign(createdTx, from, c.pk, currentHeight, stypes.TxOutItem{VaultPubKey: c.pk})
	if err != nil || len(rawTx) == 0 {
		return nil, fmt.Errorf("fail to sign message: %w", err)
	}
	err = c.BroadcastTx(rawTx)
	if err != nil {
		return err
	}
	tx := &etypes.Transaction{}
	if err := tx.UnmarshalJSON(rawTx); err != nil {
		return err
	}
	_, err = c.waitBlocks(tx.Hash().Hex())
	if err != nil {
		return err
	}
	return nil
}

// Churn changes asgard owner
func (c *Client) Churn(pk common.PubKey) error {
	newAsgard := c.GetAddress(pk)
	gasPrice := c.GetGasPrice()
	nonce, err := c.GetNonce(from)
	if err != nil {
		return err
	}
	data, err := c.inputAsgard(newAsgard)
	if err != nil {
		return err
	}
	createdTx := etypes.NewTransaction(nonce, ecommon.HexToAddress(toAddr), big.NewInt(0), NewAsgardGas, gasPrice, data)

	currentHeight, err := c.GetHeight()
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get current Ethereum block height")
		return err
	}

	rawTx, err := c.sign(createdTx, from, c.pk, currentHeight, stypes.TxOutItem{VaultPubKey: c.pk})
	if err != nil || len(rawTx) == 0 {
		return fmt.Errorf("fail to sign message: %w", err)
	}

	err = c.BroadcastTx(rawTx)
	if err != nil {
		return err
	}

	tx := &etypes.Transaction{}
	if err := tx.UnmarshalJSON(rawTx); err != nil {
		return err
	}
	_, err = c.waitBlocks(tx.Hash().Hex())
	if err != nil {
		return err
	}

	err = c.moveFunds(newAsgard)
	if err != nil {
		return err
	}

	c.contracts[pk] = c.contracts[c.pk]
	delete(c.contracts, c.pk)
	c.pk = pk
}

// InitContract, should be done only once for mainnet on genesis block
func (c *Client) InitContract() error {
	bytes, err := hex.DecodeString(ByteCode)
	if err != nil {
		return fmt.Errorf("failed to decode code: %w", err)
	}
	createdTx := etypes.NewContractCreation(0, big.NewInt(0), ContractCreationGas, c.GetGasPrice(), bytes)
	// send initial ETH to create contract
	err = c.sendETH(big.NewInt(int64(createdTx.Gas())), c.GetAddress(c.pk))
	if err != nil {
		return fmt.Errorf("failed to send init ETH: %w", err)
	}

	height, err := c.GetHeight()
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get current Ethereum block height")
		return fmt.Errorf("fail to get block height: %w", err)
	}
	from := c.GetAddress(c.pk)
	rawTx, err := c.sign(createdTx, fromAddr, c.pk, height, stypes.TxOutItem{}, 0)
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
	receipt, err := c.waitBlocks(hash)
	if err != nil {
		return fmt.Errorf("fail to wait blocks and get receipt: %w", err)
	}

	c.contracts[c.pk] = receipt.ContractAddress
	if len(receipt.ContractAddress.String()) == 0 || receipt.ContractAddress.String() == Zero {
		return fmt.Errorf("incorrect contract creation")
	}
	c.ethScanner.Contracts[receipt.ContractAddress] = c.pk
	vaultABI := &abi.ABI{}
	err = vaultABI.UnmarshalJSON([]byte(VaultJSON))
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

// Start
func (c *Client) Start(globalTxsQueue chan stypes.TxIn, globalErrataQueue chan stypes.ErrataBlock) {
	c.blockScanner.Start(globalTxsQueue)
	c.ethScanner.globalErrataQueue = globalErrataQueue
}

// Stop
func (c *Client) Stop() {
	c.blockScanner.Stop()
	c.client.Close()
}

// GetConfig
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

// GetChain get chain
func (c *Client) GetChain() common.Chain {
	return common.ETHChain
}

// GetHeight gets height from eth scanner
func (c *Client) GetHeight() (int64, error) {
	return c.ethScanner.GetHeight()
}

// createInput for contract method
func (c *Client) inputAsgard(newAsgard string) ([]byte, error) {
	input, err := c.contractABI.Pack("NewAsgard", ecommon.HexToAddress(newAsgard))
	if err != nil {
		return nil, err
	}
	return input, nil
}

// createInput for contract method
func (c *Client) createInput(token, receiver string, value *big.Int) ([]byte, error) {
	input, err := c.contractABI.Pack(TransferMethod, ecommon.HexToAddress(token), ecommon.HexToAddress(receiver), value)
	if err != nil {
		return nil, err
	}
	return input, nil
}

// getReceipt gets receipt
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

// GetGasFee gets gas fee
func (c *Client) GetGasFee(count uint64) common.Gas {
	return common.GetETHGasFee(big.NewInt(1), count)
}

// GetGasPrice gets gas price from eth scanner
func (c *Client) GetGasPrice() *big.Int {
	gasPrice := c.ethScanner.GetGasPrice()
	// increase by 40%
	if c.chainID != types.Localnet {
		gasPrice = gasPrice.Mul(gasPrice, 7)
		gasPrice = gasPrice.Div(gasPrice, 5)
	}
	return gasPrice
}

// estimateGas estimates gas for tx
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

// GetNonce gets nonce
func (c *Client) GetNonce(addr string) (uint64, error) {
	nonce, err := c.client.PendingNonceAt(context.Background(), ecommon.HexToAddress(addr))
	if err != nil {
		return 0, fmt.Errorf("fail to get account nonce: %w", err)
	}
	return nonce, nil
}

// SignTx sign the the given TxArrayItem
func (c *Client) SignTx(tx stypes.TxOutItem, height int64, retry uint64) ([]byte, error) {
	value := big.NewInt(0)
	token := strings.ToLower(ETHToken)
	for _, coin := range tx.Coins {
		value.Add(value, coin.Amount.BigInt())
		parts := strings.Split(coin.Asset.Symbol.String(), "-")
		if len(parts) < 2 {
			continue
		}
		token = strings.ToLower(parts[1])
	}

	to := tx.ToAddress.String()
	from := c.GetAddress(tx.VaultPubKey)
	if !c.contrats.HasKey(tx.VaultPubKey) {
		return nil, fmt.Errorf("pubkey contract is missing")
	}

	if len(to) == 0 || value.Uint64() == 0 {
		c.logger.Error().Msg("invalid tx params")
		return nil, fmt.Errorf("invalid tx params")
	}

	nonce, err := c.GetNonce(from)
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to fetch account nonce")
	}
	c.logger.Info().Uint64("nonce", nonce).Msg("account info")

	dest := ecommon.HexToAddress(to)
	data := []byte{}
	if token != strings.ToLower(ETHToken) {
		data, err = c.createInput(token, to, value)
		if err != nil {
			c.logger.Error().Err(err).Msg("fail to create input")
			return nil, err
		}
		value = big.NewInt(0)
		dest = c.contracts[tx.VaultPubKey]
	}

	gasOut := big.NewInt(0)
	for _, coin := range tx.MaxGas {
		gasOut.Add(gasOut, coin.Amount.BigInt())
	}
	gasPrice := c.GetGasPrice()
	gasOut.Div(gasOut, gasPrice)

	createdTx := etypes.NewTransaction(nonce, dest, value, gasOut.Uint64(), gasPrice, data)
	estimatedGas, err := c.estimateGas(from, createdTx)
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

	rawTx, err := c.sign(createdTx, from, tx.VaultPubKey, currentHeight, tx, retry)
	if err != nil || len(rawTx) == 0 {
		return nil, fmt.Errorf("fail to sign message: %w", err)
	}

	return rawTx, nil
}

// sign is design to sign a given message with keysign party and keysign wrapper
func (c *Client) sign(tx *etypes.Transaction, from string, poolPubKey common.PubKey, height int64, txOutItem stypes.TxOutItem, retry uint64) ([]byte, error) {
	keySignParty, err := c.keySignPartyMgr.GetKeySignParty(poolPubKey)
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get keysign party")
		return nil, err
	}
	rawBytes, err := c.kw.Sign(tx, poolPubKey, keySignParty)
	if err == nil && rawBytes != nil {
		c.keySignPartyMgr.SaveKeySignParty(poolPubKey, keySignParty)
		return rawBytes, nil
	}
	var keysignError tss.KeysignError
	if errors.As(err, &keysignError) {
		c.keySignPartyMgr.RemoveKeySignParty(poolPubKey)
		if len(keysignError.Blame.BlameNodes) == 0 {
			// TSS doesn't know which node to blame
			return nil, err
		}

		// key sign error forward the keysign blame to thorchain
		txID, errPostKeysignFail := c.thorchainBridge.PostKeysignFailure(keysignError.Blame, height, txOutItem.Memo, txOutItem.Coins, retry)
		if errPostKeysignFail != nil {
			c.logger.Error().Err(err).Msg("fail to post keysign failure to thorchain")
			return nil, multierror.Append(err, errPostKeysignFail)
		}
		c.logger.Info().Str("tx_id", txID.String()).Msgf("post keysign failure to thorchain")
	}
	c.logger.Error().Err(err).Msg("fail to sign tx")
	return nil, err
}

// GetBalance gets balance
func (c *Client) GetBalance(addr string) (*big.Int, error) {
	return c.client.BalanceAt(context.Background(), ecommon.HexToAddress(addr), nil)
}

// GetAccount gets account by address in eth client
func (c *Client) GetAccount(pk common.PubKey) (common.Account, error) {
	addr := c.GetAddress(pk)
	nonce, err := c.GetNonce(addr)
	if err != nil {
		return common.Account{}, err
	}
	balance, err := c.GetBalance(addr)
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get account nonce: %w", err)
	}
	account := common.NewAccount(int64(nonce), 0, common.AccountCoins{common.AccountCoin{Amount: balance.Uint64(), Denom: "ETH.ETH-0X0000000000000000000000000000000000000000"}})
	return account, nil
}

// BroadcastTx decodes tx and broadcasts to Ethereum chain
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
