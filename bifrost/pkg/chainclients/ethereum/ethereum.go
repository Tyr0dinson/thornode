package ethereum

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
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
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
)

const (
	Confirmations     = 12
	GasLimit          = uint64(5000000)
	AsgardTransfer    = "asgardTransfer"
	YggdrasilTransfer = "yggdrasilTransfer"
	YggdrasilPlus     = "yggdrasilPlus"
	YggdrasilMinus    = "yggdrasilMinus"
	Allowance         = "allowance"
	YggAllowance      = "yggAllowance"
	AddAsgard         = "addAsgard"
	YggPlusMemo       = "yggdrasil+"
	YggMinusMemo      = "yggdrasil-"
	ETHTransferGas    = uint64(21000)
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
	vaultABI        *abi.ABI
	erc20ABI        *abi.ABI
	vaultAddress    ecommon.Address
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

	if err = c.InitContracts(cfg); err != nil {
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

// moveFunds moves ETH and contract ownership
func (c *Client) moveFunds(to string) error {
	from := c.GetAddress(c.pk)
	balance, err := c.GetBalance(from, strings.ToLower(ETHToken))
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
	data, err := c.inputAddAsgard([]string{newAsgard, from})
	if err != nil {
		return err
	}

	to := c.vaultAddress
	createdTx := etypes.NewTransaction(nonce, to, big.NewInt(0), AddAsgardGas, gasPrice, data)

	currentHeight, err := c.GetHeight()
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get current Ethereum block height")
		return err
	}

	rawTx, err := c.sign(createdTx, from, c.pk, currentHeight, stypes.TxOutItem{VaultPubKey: c.pk}, 0)
	if err != nil || len(rawTx) == 0 {
		return fmt.Errorf("fail to sign message: %w", err)
	}

	if err = c.BroadcastTx(stypes.TxOutItem{VaultPubKey: c.pk}, rawTx); err != nil {
		return err
	}

	c.ethScanner.VaultOwner = newAsgard
	c.pk = pk
	return nil
}

// InitContracts
func (c *Client) InitContracts(cfg config.ChainConfiguration) error {
	c.vaultAddress = ecommon.HexToAddress(cfg.Contract)
	c.ethScanner.VaultAddress = ecommon.HexToAddress(cfg.Contract)
	vaultABI := &abi.ABI{}
	err := vaultABI.UnmarshalJSON([]byte(cfg.Jsons[0]))
	if err != nil {
		return err
	}
	c.ethScanner.VaultABI = vaultABI
	c.ethScanner.VaultOwner = c.GetAddress(c.pk)
	c.vaultABI = vaultABI
	erc20ABI := &abi.ABI{}
	err = erc20ABI.UnmarshalJSON([]byte(cfg.Jsons[1]))
	if err != nil {
		return err
	}
	c.ethScanner.ERC20ABI = erc20ABI
	c.erc20ABI = erc20ABI
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
	
// inputAddAsgard
func (c *Client) inputAddAsgard(newAsgards []string) ([]byte, error) {
	asgards := []ecommon.Address{}
	for _, newAsgard := range newAsgards {
		asgards = append(asgards, ecommon.HexToAddress(newAsgard))
	}
	input, err := c.vaultABI.Pack(AddAsgard, asgards)
	if err != nil {
		return nil, err
	}
	return input, nil
}

// inputTransfer
func (c *Client) inputTransfer(transferType, to, token string, value *big.Int, memo string) ([]byte, error) {
	input, err := c.vaultABI.Pack(transferType, ecommon.HexToAddress(to), ecommon.HexToAddress(token), value, memo)
	if err != nil {
		return nil, err
	}
	return input, nil
}

// inputYggdrasil
func (c *Client) inputYggdrasil(yggdrasilType, to, token string, value *big.Int) ([]byte, error) {
	input, err := c.vaultABI.Pack(yggdrasilType, ecommon.HexToAddress(to), ecommon.HexToAddress(token), value)
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
	data := []byte(tx.Memo)
	if token != strings.ToLower(ETHToken) {
		data, err = c.inputTransfer(token, to, value, tx.Memo)
		if err != nil {
			c.logger.Error().Err(err).Msg("fail to create input")
			return nil, err
		}
		if tx.Memo == YggPlusMemo {
			data, err = c.inputYggdrasil(YggdrasilPlus, to, token, value)
			if err != nil {
				c.logger.Error().Err(err).Msg("fail to create input")
				return nil, err
			}
		} else if tx.Memo == YggMinusMemo {
			data, err = c.inputYggdrasil(YggdrasilMinus, to, token, value)
			if err != nil {
				c.logger.Error().Err(err).Msg("fail to create input")
				return nil, err
			}
		} else {
			if _, ok := c.asgards[from]; ok {
				data, err = c.inputTransfer(AsgardTransfer, to, token, value, tx.Memo)
				if err != nil {
					c.logger.Error().Err(err).Msg("fail to create input")
					return nil, err
				}
			} else {
				data, err = c.inputTransfer(YggdrasilTransfer, to, token, value, tx.Memo)
				if err != nil {
					c.logger.Error().Err(err).Msg("fail to create input")
					return nil, err
				}
			}
		}
		value = big.NewInt(0)
		dest = c.vaultAddress
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

func (c *Client) GetBalance(addr, token string) (*big.Int, error) {
	if token == strings.ToLower(ETHToken) {
		return c.client.BalanceAt(context.Background(), ecommon.HexToAddress(addr), nil)
	}
	var err error
	var input []byte
	method := ""
	if _, ok := c.asgards[addr]; ok {
		method = Allowance
		addr = c.vaultAddress.String()
		input, err = c.vaultABI.Pack(method, ecommon.HexToAddress(token))
	} else {
		method = YggAllowance
		input, err = c.vaultABI.Pack(method, ecommon.HexToAddress(token), ecommon.HexToAddress(addr))
	}
	if err != nil {
		return nil, err
	}
	to := &ecommon.Address{}
	*to = ecommon.HexToAddress(token)
	res, err := c.client.CallContract(context.Background(), ethereum.CallMsg{
		From: ecommon.HexToAddress(addr),
		To:   to,
		Data: input,
	}, nil)
	if err != nil {
		return nil, err
	}
	value := big.NewInt(0)
	err = c.vaultABI.Unpack(&value, "balanceOf", res)
	if err != nil {
		return nil, err
	}
	return value, nil
}

// GetBalance gets balance
func (c *Client) GetBalances(addr string) (common.AccountCoins, error) {
	tokens, err := c.ethScanner.GetTokens()
	if err != nil {
		return nil, err
	}
	coins := common.AccountCoins{}
	for _, token := range tokens {
		balance, err := c.GetBalance(addr, token.Address)
		if err != nil {
			continue
		}
		coins = append(coins, common.AccountCoin{Amount: cosmos.NewUintFromBigInt(balance), Denom: "ETH." + token.Symbol + "-" + strings.ToUpper(token.Address)})
	}
	if err != nil {
		return nil, err
	}
	return coins, nil
}

// GetAccount gets account by address in eth client
func (c *Client) GetAccount(pk common.PubKey) (common.Account, error) {
	addr := c.GetAddress(pk)
	nonce, err := c.GetNonce(addr)
	if err != nil {
		return common.Account{}, err
	}
	balance, err := c.GetBalance(addr, strings.ToLower(ETHToken))
	if err != nil {
		return common.Account{}, err
	}
	if pk == c.pk {
		addr = c.vaultAddress.String()
	}
	coins, err := c.GetBalances(addr)
	if err != nil {
		return common.Account{}, err
	}
	coins = append(coins, common.AccountCoin{Amount: cosmos.NewUintFromBigInt(balance), Denom: "ETH.ETH-0X0000000000000000000000000000000000000000"})
	account := common.NewAccount(int64(nonce), 0, coins)
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
