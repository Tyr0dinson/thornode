package solana

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/bifrost/metrics"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/bifrost/tss"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/config"
	"gitlab.com/thorchain/thornode/constants"
	mem "gitlab.com/thorchain/thornode/x/thorchain/memo"
	tssp "gitlab.com/thorchain/tss/go-tss/tss"
)

// SOLClient is a client for the Solana blockchain
type SOLClient struct {
	logger                  zerolog.Logger
	cfg                     config.BifrostChainConfiguration
	localPubKey             common.PubKey
	kw                      *evm.KeySignWrapper
	solScanner              *SOLScanner
	bridge                  thorclient.ThorchainBridge
	blockScanner            *blockscanner.BlockScanner
	pubkeyMgr               pubkeymanager.PubKeyValidator
	poolMgr                 thorclient.PoolManager
	tssKeySigner            *tss.KeySign
	tssKeyManager           tss.ThorchainKeyManager
	wg                      *sync.WaitGroup
	stopchan                chan struct{}
	globalSolvencyQueue     chan stypes.Solvency
	signerCacheManager      *signercache.CacheManager
	lastSolvencyCheckHeight int64
}

// NewSOLClient creates a new instance of a SOLClient
func NewSOLClient(
	thorKeys *thorclient.Keys,
	cfg config.BifrostChainConfiguration,
	server *tssp.TssServer,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	poolMgr thorclient.PoolManager,
) (*SOLClient, error) {
	// check required arguments
	if thorKeys == nil {
		return nil, fmt.Errorf("failed to create EVM client, thor keys empty")
	}
	if bridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}
	if pubkeyMgr == nil {
		return nil, errors.New("pubkey manager is nil")
	}
	if poolMgr == nil {
		return nil, errors.New("pool manager is nil")
	}

	// create keys
	tssKm, err := tss.NewKeySign(server, bridge)
	if err != nil {
		return nil, fmt.Errorf("failed to create tss signer: %w", err)
	}
	priv, err := thorKeys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get private key: %w", err)
	}
	temp, err := codec.ToTmPubKeyInterface(priv.PubKey())
	if err != nil {
		return nil, fmt.Errorf("failed to get tm pub key: %w", err)
	}
	pk, err := common.NewPubKeyFromCrypto(temp)
	if err != nil {
		return nil, fmt.Errorf("failed to get pub key: %w", err)
	}

	// create rpc clients
	rpcClient := rpc.NewSolRPC(cfg.RPCHost, cfg.BlockScanner.HTTPRequestTimeout)

	c := &SOLClient{
		logger:      log.With().Str("module", "sol").Stringer("chain", cfg.ChainID).Logger(),
		cfg:         cfg,
		localPubKey: pk,
		// kw:           keysignWrapper,
		bridge:        bridge,
		pubkeyMgr:     pubkeyMgr,
		poolMgr:       poolMgr,
		tssKeySigner:  tssKm,
		tssKeyManager: tssKm,
		wg:            &sync.WaitGroup{},
		stopchan:      make(chan struct{}),
	}

	// initialize storage
	var path string // if not set later, will in memory storage
	if len(c.cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", c.cfg.BlockScanner.DBPath, c.cfg.BlockScanner.ChainID)
	}
	storage, err := blockscanner.NewBlockScannerStorage(path, c.cfg.ScannerLevelDB)
	if err != nil {
		return c, fmt.Errorf("fail to create blockscanner storage: %w", err)
	}
	signerCacheManager, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
	if err != nil {
		return nil, fmt.Errorf("fail to create signer cache manager")
	}
	c.signerCacheManager = signerCacheManager

	// create block scanner
	c.solScanner, err = NewSOLScanner(
		c.cfg.BlockScanner,
		storage,
		bridge,
		m,
		rpcClient,
		pubkeyMgr,
		c.ReportSolvency,
		signerCacheManager,
	)
	if err != nil {
		return c, fmt.Errorf("fail to create evm block scanner: %w", err)
	}

	// initialize block scanner
	c.blockScanner, err = blockscanner.NewBlockScanner(
		c.cfg.BlockScanner, storage, m, c.bridge, c.solScanner,
	)
	if err != nil {
		return c, fmt.Errorf("fail to create block scanner: %w", err)
	}

	return c, nil
}

// Start starts the chain client with the given queues.
func (c *SOLClient) Start(globalTxsQueue chan stypes.TxIn, globalErrataQueue chan stypes.ErrataBlock, globalSolvencyQueue chan stypes.Solvency) {
	// c.solScanner.globalErrataQueue = globalErrataQueue
	c.globalSolvencyQueue = globalSolvencyQueue
	c.tssKeySigner.Start()
	c.blockScanner.Start(globalTxsQueue)
	c.wg.Add(1)
	go runners.SolvencyCheckRunner(c.GetChain(), c, c.bridge, c.stopchan, c.wg, constants.ThorchainBlockTime)
}

// Stop stops the chain client.
func (c *SOLClient) Stop() {
	c.tssKeySigner.Stop()
	c.blockScanner.Stop()
	close(c.stopchan)
	c.wg.Wait()
}

// IsBlockScannerHealthy returns true if the block scanner is healthy.
func (c *SOLClient) IsBlockScannerHealthy() bool {
	return c.blockScanner.IsHealthy()
}

// --------------------------------- config ---------------------------------

// GetConfig returns the chain configuration.
func (c *SOLClient) GetConfig() config.BifrostChainConfiguration {
	return c.cfg
}

// GetChain returns the chain.
func (c *SOLClient) GetChain() common.Chain {
	return c.cfg.ChainID
}

// --------------------------------- status ---------------------------------

// GetHeight returns the current height of the chain.
func (c *SOLClient) GetHeight() (int64, error) {
	return c.solScanner.GetHeight()
}

// GetBlockScannerHeight returns blockscanner height
func (c *SOLClient) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
}

func (c *SOLClient) GetLatestTxForVault(vault string) (string, string, error) {
	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(stypes.InboundCacheKey(vault, c.GetChain().String()))
	if err != nil {
		return "", "", err
	}
	lastBroadCasted, err := c.signerCacheManager.GetLatestRecordedTx(stypes.BroadcastCacheKey(vault, c.GetChain().String()))
	return lastObserved, lastBroadCasted, err
}

// --------------------------------- addresses ---------------------------------

// GetAddress returns the address for the given public key.
func (c *SOLClient) GetAddress(poolPubKey common.PubKey) string {
	addr, err := poolPubKey.GetAddress(c.cfg.ChainID)
	if err != nil {
		c.logger.Error().Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get pool address")
		return ""
	}
	return addr.String()
}

// GetAccount returns the account for the given public key.
func (c *SOLClient) GetAccount(pk common.PubKey, height *big.Int) (common.Account, error) {
	// TODO: Implement?
	return common.Account{}, nil
}

// GetAccountByAddress returns the account for the given address.
func (c *SOLClient) GetAccountByAddress(address string, height *big.Int) (common.Account, error) {
	// TODO: Implement?
	return common.Account{}, nil
}

// --------------------------------- balances ---------------------------------

// GetBalance returns the balance of the provided address.
func (c *SOLClient) GetBalance(addr, token string, height *big.Int) (*big.Int, error) {
	// TODO: Implement
	return big.NewInt(0), nil
}

// GetBalances returns the balances of the provided address.
func (c *SOLClient) GetBalances(addr string, height *big.Int) (common.Coins, error) {
	// TODO: Implement
	return common.Coins{}, nil
}

// --------------------------------- solvency ---------------------------------

// ReportSolvency reports solvency once per configured solvency blocks.
func (c *SOLClient) ReportSolvency(height int64) error {

	// TODO: Implement

	if !c.ShouldReportSolvency(height) {
		return nil
	}
	c.lastSolvencyCheckHeight = height
	return nil
}

// ShouldReportSolvency returns true if the given height is a solvency report height.
func (c *SOLClient) ShouldReportSolvency(height int64) bool {
	return height%c.cfg.SolvencyBlocks == 0
}

// --------------------------------- sign ---------------------------------

// SignTx returns the signed transaction.
func (c *SOLClient) SignTx(tx stypes.TxOutItem, height int64) ([]byte, []byte, *stypes.TxInItem, error) {
	if tx.ToAddress.IsEmpty() {
		return nil, nil, nil, fmt.Errorf("to address is empty")
	}
	if tx.VaultPubKey.IsEmpty() {
		return nil, nil, nil, fmt.Errorf("vault public key is empty")
	}

	if len(tx.Memo) == 0 {
		return nil, nil, nil, fmt.Errorf("can't sign tx when it doesn't have memo")
	}

	solTx, err := c.CreateTx(tx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to create tx: %w", err)
	}

	checkPointBytes, err := solTx.Serialize()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to serialize checkpoint bytes: %w", err)
	}

	signedTx, err := c.signTransaction(solTx, tx)
	if err != nil {
		return nil, checkPointBytes, nil, fmt.Errorf("fail to sign tx: %w", err)
	}

	fmt.Println("SIGNED TX")
	prettyPrint(signedTx)

	signedBytes, err := signedTx.Serialize()
	if err != nil {
		return nil, checkPointBytes, nil, fmt.Errorf("fail to serialize signed tx: %w", err)
	}

	return signedBytes, nil, nil, nil
}

// SignTransaction takes a rpc.Transaction and signs with the pubkey
func (c *SOLClient) signTransaction(solTx rpc.Transaction, txOut stypes.TxOutItem) (rpc.Transaction, error) {
	// TODO: Get pubkey
	// cpk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeAccPub, txOut.VaultPubKey.String())
	// if err != nil {
	// 	return rpc.Transaction{}, fmt.Errorf("unable to GetPubKeyFromBech32 from cosmos: %w", err)
	// }
	message, err := solTx.Serialize()
	if err != nil {
		return solTx, fmt.Errorf("failed to serialize transaction: %w", err)
	}

	c.logger.Info().Str("txout", jsonString(txOut)).Msg("signing message")

	sig, _, err := c.tssKeySigner.RemoteSign(message, txOut.VaultPubKey.String())
	if err != nil {
		return rpc.Transaction{}, err
	}

	fmt.Println("SIG")
	prettyPrint(sig)

	// TODO: Verify signature
	// if !cpk.VerifySignature(message, sig) {
	// 	return rpc.Transaction{}, fmt.Errorf("unable to verify signature with secpPubKey")
	// }

	solTx.Transaction.Signatures = append(solTx.Transaction.Signatures, common.Base58Encode(sig))

	return solTx, nil
}

// CreateTx takes a TxOutItem and returns a solana transaction
func (c *SOLClient) CreateTx(tx stypes.TxOutItem) (rpc.Transaction, error) {

	solTx := rpc.Transaction{}

	memo, err := mem.ParseMemo(common.LatestVersion, tx.Memo)
	if err != nil {
		return solTx, fmt.Errorf("fail to parse memo(%s):%w", tx.Memo, err)
	}

	if memo.IsInbound() {
		return solTx, fmt.Errorf("inbound memo should not be used for outbound tx")
	}

	if len(tx.Coins) != 1 {
		return solTx, fmt.Errorf("only support one coin per transaction")
	}

	solValue, err := convertToLamports(tx.Coins[0].Amount)
	if solValue.Int64() == 0 {
		return solTx, fmt.Errorf("sol value is zero")
	}

	// Create instruction
	systemProgramID := "11111111111111111111111111111111"
	data := common.Base58Encode(createTransferInstructionData(solValue))

	instruction := rpc.Instruction{
		Accounts:       []int{0, 1},
		Data:           data,
		ProgramIdIndex: 2,
	}

	recentBlockhash, err := c.solScanner.GetLatestBlockHash()
	if err != nil {
		return solTx, fmt.Errorf("fail to get latest block hash: %w", err)
	}

	message := rpc.Message{
		AccountKeys: []string{
			tx.VaultPubKey.String(),
			tx.ToAddress.String(),
			systemProgramID,
		},
		Header: rpc.Header{
			NumReadonlySignedAccounts:   0,
			NumReadonlyUnsignedAccounts: 1,
			NumRequiredSignatures:       1,
		},
		Instructions:    []rpc.Instruction{instruction},
		RecentBlockhash: recentBlockhash,
	}

	txnData := rpc.TxnData{
		Message:    message,
		Signatures: []string{},
	}

	return rpc.Transaction{
		Meta:        rpc.Meta{},
		Transaction: txnData,
		Version:     rpc.Version{Value: "0"},
	}, nil
}

// --------------------------------- sign ---------------------------------

func convertToLamports(amount cosmos.Uint) (*big.Int, error) {
	amountLamports := new(big.Int).Mul(amount.BigInt(), big.NewInt(1e9))
	return amountLamports, nil
}

// Create transfer instruction data
func createTransferInstructionData(amountLamports *big.Int) []byte {
	var data []byte
	// System program transfer instruction
	transferInstruction := uint32(2)
	transferInstructionBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(transferInstructionBytes, transferInstruction)
	data = append(data, transferInstructionBytes...)

	// Append amount
	amountBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(amountBytes, amountLamports.Uint64())
	data = append(data, amountBytes...)

	return data
}

// --------------------------------- broadcast ---------------------------------

// BroadcastTx broadcasts the transaction and returns the transaction hash.
func (c *SOLClient) BroadcastTx(txOutItem stypes.TxOutItem, hexTx []byte) (string, error) {
	// TODO: Implement
	return "", nil
}

// --------------------------------- observe ---------------------------------

// OnObservedTxIn is called when a new observed tx is received.
func (c *SOLClient) OnObservedTxIn(txIn stypes.TxInItem, blockHeight int64) {
	// TODO: Implement
}

// GetConfirmationCount returns the confirmation count for the given tx.
func (c *SOLClient) GetConfirmationCount(txIn stypes.TxIn) int64 {
	// TODO: Implement
	return 0
}

// ConfirmationCountReady returns true if the confirmation count is ready.
func (c *SOLClient) ConfirmationCountReady(txIn stypes.TxIn) bool {
	// TODO: Implement
	return true
}

// Helpers

func base58Decode(input string) ([32]byte, error) {
	var output [32]byte
	decoded, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		return output, err
	}
	copy(output[:], decoded)
	return output, nil
}
