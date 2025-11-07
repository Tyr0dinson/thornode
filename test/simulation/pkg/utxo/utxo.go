package utxo

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/hashicorp/go-multierror"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/btcsuite/btcd/btcec"
	btcecv2 "github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcjson"
	btcchaincfg "github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	btcwire "github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/btcsuite/btcutil/base58"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"

	dogeec "github.com/eager7/dogd/btcec"
	dogechaincfg "github.com/eager7/dogd/chaincfg"
	dogewire "github.com/eager7/dogd/wire"
	"github.com/eager7/dogutil"
	dogetxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/dogd-txscript"

	"github.com/gcash/bchd/bchec"
	bchchaincfg "github.com/gcash/bchd/chaincfg"
	bchwire "github.com/gcash/bchd/wire"
	"github.com/gcash/bchutil"
	bchtxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/bchd-txscript"

	ltcec "github.com/ltcsuite/ltcd/btcec"
	ltcchaincfg "github.com/ltcsuite/ltcd/chaincfg"
	ltcwire "github.com/ltcsuite/ltcd/wire"
	"github.com/ltcsuite/ltcutil"
	ltctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/ltcd-txscript"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/chain/zec/go/zec"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Client
////////////////////////////////////////////////////////////////////////////////////////

//go:generate go run utxo_generate.go

type Client struct {
	sync.Mutex
	chain common.Chain
	rpc   *rpc.Client

	keys    *thorclient.Keys
	privKey *btcec.PrivateKey
	pubKey  common.PubKey
	address common.Address

	// UTXO tracking for ZEC only (to prevent mempool conflicts)
	muSpentUtxos sync.Mutex          // Mutex for thread-safe access to spentUtxos
	spentUtxos   map[string]struct{} // Track spent UTXOs (ZEC only)
}

var _ LiteChainClient = &Client{}

func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(chain common.Chain, host string, keys *thorclient.Keys) (LiteChainClient, error) {
	// create rpc client
	retries := 5
	rpc, err := rpc.NewClient(host, "thorchain", "password", retries, zerolog.Nop())
	if err != nil {
		return nil, fmt.Errorf("fail to create rpc client: %w", err)
	}

	// extract the private key
	privateKey, err := keys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}
	privKey, _ := btcec.PrivKeyFromBytes(btcec.S256(), privateKey.Bytes())

	// derive the public key
	buf := privKey.PubKey().SerializeCompressed()
	pk := secp256k1.PubKey(buf)
	pubkey, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		return nil, fmt.Errorf("fail to create pubkey: %w", err)
	}

	// get pubkey address for the chain
	address, err := pubkey.GetAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
	}

	// import address to wallet, rescan if master account
	// ZEC doesn't support address importing
	if !chain.Equals(common.ZECChain) {
		if keys.GetSignerInfo().Name == "master" {
			err = rpc.ImportAddressRescan(address.String())
		} else {
			err = rpc.ImportAddress(address.String())
		}
		if err != nil {
			return nil, fmt.Errorf("fail to import address(%s): %w", address, err)
		}
	}

	client := &Client{
		chain:   chain,
		rpc:     rpc,
		keys:    keys,
		privKey: privKey,
		pubKey:  pubkey,
		address: address,
	}

	// Initialize UTXO tracking only for ZEC
	// (ZEC uses getaddressutxos which lacks mempool awareness)
	if chain.Equals(common.ZECChain) {
		client.spentUtxos = make(map[string]struct{})
	}

	return client, nil
}

////////////////////////////////////////////////////////////////////////////////////////
// GetAccount
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	// default to the client key address
	var err error
	addr := c.address
	if pk != nil {
		addr, err = pk.GetAddress(c.chain)
		if err != nil {
			return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
		}
	}

	var totalAmt btcutil.Amount

	// ZEC uses getaddressutxos instead of listunspent
	if c.chain.Equals(common.ZECChain) {
		zecUtxos, err := c.rpc.GetAddressUtxos(addr.String())
		if err != nil {
			return nil, fmt.Errorf("fail to get ZEC UTXOs: %w", err)
		}

		// sum balance from satoshis
		totalSatoshis := int64(0)
		for _, item := range zecUtxos {
			totalSatoshis += item.Value
		}
		totalAmt = btcutil.Amount(totalSatoshis)
	} else {
		// get unspent utxos for BTC, LTC, BCH, DOGE
		utxos, err := c.rpc.ListUnspent(addr.String())
		if err != nil {
			return nil, fmt.Errorf("fail to get UTXOs: %w", err)
		}

		// sum balance
		total := 0.0
		for _, item := range utxos {
			total += item.Amount
		}
		totalAmt, err = btcutil.NewAmount(total)
		if err != nil {
			return nil, fmt.Errorf("fail to convert total amount: %w", err)
		}
	}

	// create account
	coin := common.NewCoin(c.chain.GetGasAsset(), cosmos.NewUint(uint64(totalAmt)))
	a := common.NewAccount(0, 0, common.NewCoins(coin), false)

	return &a, nil
}

////////////////////////////////////////////////////////////////////////////////////////
// SignTx
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) SignContractTx(SimContractTx) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	// Special handling for ZEC using the Rust library
	if c.chain.Equals(common.ZECChain) {
		return c.signZECTx(tx)
	}

	sourceScript, err := c.getSourceScript(tx)
	if err != nil {
		return nil, fmt.Errorf("fail to get source pay to address script: %w", err)
	}

	// build transaction
	redeemTx, amounts, err := c.buildTx(tx, sourceScript)
	if err != nil {
		return nil, err
	}

	// create the list of signing requests
	signings := []struct{ idx, amount int64 }{}
	totalAmount := int64(0)
	for idx, txIn := range redeemTx.TxIn {
		key := fmt.Sprintf("%s-%d", txIn.PreviousOutPoint.Hash, txIn.PreviousOutPoint.Index)
		outputAmount := amounts[key]
		totalAmount += outputAmount
		signings = append(signings, struct{ idx, amount int64 }{int64(idx), outputAmount})
	}

	// convert the wire tx to the chain specific tx for signing
	var stx interface{}
	switch c.chain {
	case common.DOGEChain:
		stx = wireToDOGE(redeemTx)
	case common.BCHChain:
		stx = wireToBCH(redeemTx)
	case common.LTCChain:
		stx = wireToLTC(redeemTx)
	case common.BTCChain:
		stx = wireToBTC(redeemTx)
	default:
		log.Fatal().Msg("unsupported chain")
	}

	// sign the tx
	wg := &sync.WaitGroup{}
	wg.Add(len(signings))
	mu := &sync.Mutex{}
	var utxoErr error
	for _, signing := range signings {
		go func(i int, amount int64) {
			defer wg.Done()

			var err error
			switch c.chain {
			case common.DOGEChain:
				err = c.signUTXODOGE(stx.(*dogewire.MsgTx), amount, sourceScript, i)
			case common.BCHChain:
				err = c.signUTXOBCH(stx.(*bchwire.MsgTx), amount, sourceScript, i)
			case common.LTCChain:
				err = c.signUTXOLTC(stx.(*ltcwire.MsgTx), amount, sourceScript, i)
			case common.BTCChain:
				err = c.signUTXOBTC(stx.(*btcwire.MsgTx), amount, sourceScript, i)
			default:
				log.Fatal().Msg("unsupported chain")
			}

			if err != nil {
				mu.Lock()
				utxoErr = multierror.Append(utxoErr, err)
				mu.Unlock()
			}
		}(int(signing.idx), signing.amount)
	}
	wg.Wait()
	if utxoErr != nil {
		return nil, fmt.Errorf("fail to sign the message: %w", utxoErr)
	}

	// convert back to wire tx
	switch c.chain {
	case common.DOGEChain:
		redeemTx = dogeToWire(stx.(*dogewire.MsgTx))
	case common.BCHChain:
		redeemTx = bchToWire(stx.(*bchwire.MsgTx))
	case common.LTCChain:
		redeemTx = ltcToWire(stx.(*ltcwire.MsgTx))
	case common.BTCChain:
		redeemTx = btcToWire(stx.(*btcwire.MsgTx))
	default:
		log.Fatal().Msg("unsupported chain")
	}

	// calculate the final transaction size
	var signedTx bytes.Buffer
	if err = redeemTx.Serialize(&signedTx); err != nil {
		return nil, fmt.Errorf("fail to serialize tx to bytes: %w", err)
	}

	return signedTx.Bytes(), nil
}

////////////////////////////////////////////////////////////////////////////////////////
// BroadcastTx
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) BroadcastTx(payload []byte) (string, error) {
	// For ZEC, the payload is already a properly formatted transaction from ApplySignatures
	if c.chain.Equals(common.ZECChain) {
		// ZEC transactions are already in the proper format from the ZEC Rust library
		hexTx := hex.EncodeToString(payload)
		var txid string
		err := c.rpc.Call(&txid, "sendrawtransaction", hexTx, true)
		return txid, err
	}

	redeemTx := btcwire.NewMsgTx(btcwire.TxVersion)
	buf := bytes.NewBuffer(payload)
	if err := redeemTx.Deserialize(buf); err != nil {
		return "", fmt.Errorf("fail to deserialize payload: %w", err)
	}

	var maxFee any
	switch c.chain {
	case common.DOGEChain, common.BCHChain:
		maxFee = true // "allowHighFees" - boolean for these chains
	case common.LTCChain, common.BTCChain:
		maxFee = 10_000_000 // numeric maxfeerate for Bitcoin/Litecoin
	}

	// broadcast tx
	return c.rpc.SendRawTransaction(redeemTx, maxFee)
}

////////////////////////////////////////////////////////////////////////////////////////
// Internal
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) getSourceScript(tx SimTx) ([]byte, error) {
	switch c.chain {
	case common.DOGEChain:
		var addr dogutil.Address
		addr, err := dogutil.DecodeAddress(c.address.String(), c.getChainCfgDOGE())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", c.address, err)
		}
		return dogetxscript.PayToAddrScript(addr)
	case common.BCHChain:
		var addr bchutil.Address
		addr, err := bchutil.DecodeAddress(c.address.String(), c.getChainCfgBCH())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", c.address, err)
		}
		return bchtxscript.PayToAddrScript(addr)
	case common.LTCChain:
		var addr ltcutil.Address
		addr, err := ltcutil.DecodeAddress(c.address.String(), c.getChainCfgLTC())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", c.address, err)
		}
		return ltctxscript.PayToAddrScript(addr)
	case common.BTCChain:
		var addr btcutil.Address
		addr, err := btcutil.DecodeAddress(c.address.String(), c.getChainCfgBTC())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", c.address, err)
		}
		return btctxscript.PayToAddrScript(addr)
	case common.ZECChain:
		// ZEC uses Base58Check with different version bytes, decode manually
		decoded := base58.Decode(c.address.String())
		if len(decoded) < 26 {
			return nil, fmt.Errorf("invalid ZEC address length: %d", len(decoded))
		}

		// Extract the public key hash (20 bytes) after version bytes (2 bytes)
		// Last 4 bytes are checksum
		pubKeyHash := decoded[2:22]

		// Create P2PKH script manually: OP_DUP OP_HASH160 <pubKeyHash> OP_EQUALVERIFY OP_CHECKSIG
		builder := btctxscript.NewScriptBuilder()
		builder.AddOp(btctxscript.OP_DUP)
		builder.AddOp(btctxscript.OP_HASH160)
		builder.AddData(pubKeyHash)
		builder.AddOp(btctxscript.OP_EQUALVERIFY)
		builder.AddOp(btctxscript.OP_CHECKSIG)
		return builder.Script()
	default:
		log.Fatal().Msg("unsupported chain")
		return nil, nil
	}
}

func (c *Client) getUtxoToSpend(total float64) ([]btcjson.ListUnspentResult, error) {
	var utxos []btcjson.ListUnspentResult
	var err error

	// ZEC uses getaddressutxos instead of listunspent
	if c.chain.Equals(common.ZECChain) {
		zecUtxos, err := c.rpc.GetAddressUtxos(c.address.String())
		if err != nil {
			return nil, fmt.Errorf("fail to get ZEC UTXOs: %w", err)
		}

		// Filter out already-spent UTXOs (ZEC only needs this because getaddressutxos lacks mempool awareness)
		var filteredUtxos []rpc.Utxo
		c.muSpentUtxos.Lock()
		for _, item := range zecUtxos {
			utxoKey := fmt.Sprintf("%s:%d", item.Txid, item.Vout)
			if _, alreadySpent := c.spentUtxos[utxoKey]; alreadySpent {
				continue // Skip spent UTXO
			}
			filteredUtxos = append(filteredUtxos, item)
		}
		c.muSpentUtxos.Unlock()

		// get current height to calculate confirmations
		currentHeight, err := c.rpc.GetBlockCount()
		if err != nil {
			return nil, fmt.Errorf("fail to get current block height: %w", err)
		}

		// convert filtered ZEC UTXOs to ListUnspentResult format
		utxos = make([]btcjson.ListUnspentResult, 0, len(filteredUtxos))
		for _, zecUtxo := range filteredUtxos {
			// convert satoshis to BTC
			amountBTC := btcutil.Amount(zecUtxo.Value).ToBTC()

			// calculate confirmations (currentHeight - utxoHeight + 1)
			confirmations := currentHeight - zecUtxo.Height + 1

			utxos = append(utxos, btcjson.ListUnspentResult{
				TxID:          zecUtxo.Txid,
				Vout:          zecUtxo.Vout,
				Address:       c.address.String(),
				ScriptPubKey:  zecUtxo.Script,
				Amount:        amountBTC,
				Confirmations: confirmations,
			})
		}
	} else {
		// get unspent utxos for BTC, LTC, BCH, DOGE
		utxos, err = c.rpc.ListUnspent(c.address.String())
		if err != nil {
			return nil, fmt.Errorf("fail to get UTXOs: %w", err)
		}
	}

	// spend UTXOs older to younger
	sort.SliceStable(utxos, func(i, j int) bool {
		if utxos[i].Confirmations > utxos[j].Confirmations {
			return true
		} else if utxos[i].Confirmations < utxos[j].Confirmations {
			return false
		}
		return utxos[i].TxID < utxos[j].TxID
	})

	// collect UTXOs to spend
	var result []btcjson.ListUnspentResult
	var toSpend float64
	for _, item := range utxos {
		result = append(result, item)
		toSpend += item.Amount
		if toSpend >= total {
			break
		}
	}

	// error if there is insufficient balance
	if toSpend < total {
		return nil, fmt.Errorf("insufficient balance: %f < %f", toSpend, total)
	}

	return result, nil
}

func (c *Client) getGasSats() float64 {
	switch c.chain {
	case common.DOGEChain:
		return 0.1
	case common.BCHChain:
		return 0.0001
	case common.LTCChain:
		return 0.0001
	case common.BTCChain, common.ZECChain:
		return 0.0001
	default:
		log.Fatal().Msg("unsupported chain")
		return 0.0
	}
}

func (c *Client) getPaymentAmount(tx SimTx) float64 {
	amtToPay1e8 := tx.Coin.Amount.Uint64()
	amtToPay := btcutil.Amount(int64(amtToPay1e8)).ToBTC()
	amtToPay += c.getGasSats()
	return amtToPay
}

func (c *Client) buildTx(tx SimTx, sourceScript []byte) (
	*btcwire.MsgTx, map[string]int64, error,
) {
	txes, err := c.getUtxoToSpend(c.getPaymentAmount(tx))
	if err != nil {
		return nil, nil, fmt.Errorf("fail to get unspent UTXO: %w", err)
	}

	// Use standard version for now, ZEC version will be handled during serialization
	redeemTx := wire.NewMsgTx(wire.TxVersion)
	totalAmt := int64(0)
	individualAmounts := make(map[string]int64, len(txes))
	for _, item := range txes {
		var txID *chainhash.Hash
		txID, err = chainhash.NewHashFromStr(item.TxID)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to parse txID(%s): %w", item.TxID, err)
		}
		// double check that the utxo is still valid
		outputPoint := wire.NewOutPoint(txID, item.Vout)
		sourceTxIn := wire.NewTxIn(outputPoint, nil, nil)
		redeemTx.AddTxIn(sourceTxIn)
		var amt btcutil.Amount
		amt, err = btcutil.NewAmount(item.Amount)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to parse amount(%f): %w", item.Amount, err)
		}
		individualAmounts[fmt.Sprintf("%s-%d", txID, item.Vout)] = int64(amt)
		totalAmt += int64(amt)
	}

	var buf []byte
	switch c.chain {
	case common.DOGEChain:
		var outputAddr dogutil.Address
		outputAddr, err = dogutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgDOGE())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		buf, err = dogetxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get pay to address script: %w", err)
		}
	case common.BCHChain:
		var outputAddr bchutil.Address
		outputAddr, err = bchutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgBCH())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		buf, err = bchtxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get pay to address script: %w", err)
		}
	case common.LTCChain:
		var outputAddr ltcutil.Address
		outputAddr, err = ltcutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgLTC())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		buf, err = ltctxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get pay to address script: %w", err)
		}
	case common.BTCChain:
		var outputAddr btcutil.Address
		outputAddr, err = btcutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgBTC())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		buf, err = btctxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get pay to address script: %w", err)
		}
	case common.ZECChain:
		// ZEC uses Base58Check with different version bytes, decode manually
		decoded := base58.Decode(tx.ToAddress.String())
		if len(decoded) < 26 {
			return nil, nil, fmt.Errorf("invalid ZEC destination address length: %d", len(decoded))
		}

		// Extract the public key hash (20 bytes) after version bytes (2 bytes)
		// Last 4 bytes are checksum
		pubKeyHash := decoded[2:22]

		// Create P2PKH script manually: OP_DUP OP_HASH160 <pubKeyHash> OP_EQUALVERIFY OP_CHECKSIG
		builder := btctxscript.NewScriptBuilder()
		builder.AddOp(btctxscript.OP_DUP)
		builder.AddOp(btctxscript.OP_HASH160)
		builder.AddData(pubKeyHash)
		builder.AddOp(btctxscript.OP_EQUALVERIFY)
		builder.AddOp(btctxscript.OP_CHECKSIG)
		buf, err = builder.Script()
		if err != nil {
			return nil, nil, fmt.Errorf("fail to build pay to address script: %w", err)
		}
	default:
		log.Fatal().Msg("unsupported chain")
	}

	// pay to customer
	redeemTxOut := wire.NewTxOut(int64(tx.Coin.Amount.Uint64()), buf)
	redeemTx.AddTxOut(redeemTxOut)

	// add output to pay the balance back ourselves
	balance := totalAmt - redeemTxOut.Value - int64(c.getGasSats()*common.One)
	if balance > 0 {
		redeemTx.AddTxOut(wire.NewTxOut(balance, sourceScript))
	}

	// memo
	var nullDataScript []byte
	switch c.chain {
	case common.DOGEChain:
		nullDataScript, err = dogetxscript.NullDataScript([]byte(tx.Memo))
	case common.BCHChain:
		nullDataScript, err = bchtxscript.NullDataScript([]byte(tx.Memo))
	case common.LTCChain:
		nullDataScript, err = ltctxscript.NullDataScript([]byte(tx.Memo))
	case common.BTCChain, common.ZECChain:
		nullDataScript, err = btctxscript.NullDataScript([]byte(tx.Memo))
	default:
		log.Fatal().Msg("unsupported chain")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("fail to generate null data script: %w", err)
	}
	redeemTx.AddTxOut(wire.NewTxOut(0, nullDataScript))

	return redeemTx, individualAmounts, nil
}

// ------------------------------ chain config ------------------------------

func (c *Client) getChainCfgBTC() *btcchaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &btcchaincfg.RegressionNetParams
	case common.MainNet:
		return &btcchaincfg.MainNetParams
	case common.StageNet:
		return &btcchaincfg.MainNetParams
	default:
		log.Fatal().Msg("unsupported network")
		return nil
	}
}

func (c *Client) getChainCfgLTC() *ltcchaincfg.Params {
	cn := common.CurrentChainNetwork
	switch cn {
	case common.MockNet:
		return &ltcchaincfg.RegressionNetParams
	case common.MainNet:
		return &ltcchaincfg.MainNetParams
	case common.StageNet:
		return &ltcchaincfg.MainNetParams
	}
	return nil
}

func (c *Client) getChainCfgBCH() *bchchaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &bchchaincfg.RegressionNetParams
	case common.MainNet:
		return &bchchaincfg.MainNetParams
	case common.StageNet:
		return &bchchaincfg.MainNetParams
	default:
		log.Fatal().Msg("unsupported network")
		return nil
	}
}

func (c *Client) getChainCfgDOGE() *dogechaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &dogechaincfg.RegressionNetParams
	case common.MainNet:
		return &dogechaincfg.MainNetParams
	case common.StageNet:
		return &dogechaincfg.MainNetParams
	default:
		log.Fatal().Msg("unsupported network")
		return nil
	}
}

func (c *Client) getChainCfgZEC() *btcchaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &btcchaincfg.RegressionNetParams
	case common.MainNet:
		return &btcchaincfg.MainNetParams
	case common.StageNet:
		return &btcchaincfg.MainNetParams
	default:
		log.Fatal().Msg("unsupported network")
		return nil
	}
}

// ------------------------------ signing ------------------------------

func (c *Client) signUTXODOGE(redeemTx *dogewire.MsgTx, amount int64, sourceScript []byte, idx int) error {
	signable := dogetxscript.NewPrivateKeySignable((*dogeec.PrivateKey)(c.privKey))
	sig, err := dogetxscript.RawTxInSignature(redeemTx, idx, sourceScript, dogetxscript.SigHashAll, signable)
	if err != nil {
		return fmt.Errorf("fail to get witness: %w", err)
	}

	pkData := signable.GetPubKey().SerializeCompressed()
	sigscript, err := dogetxscript.NewScriptBuilder().AddData(sig).AddData(pkData).Script()
	if err != nil {
		return fmt.Errorf("fail to build signature script: %w", err)
	}
	redeemTx.TxIn[idx].SignatureScript = sigscript
	flag := dogetxscript.StandardVerifyFlags
	engine, err := dogetxscript.NewEngine(sourceScript, redeemTx, idx, flag, nil, nil, amount)
	if err != nil {
		return fmt.Errorf("fail to create engine: %w", err)
	}
	if err = engine.Execute(); err != nil {
		return fmt.Errorf("fail to execute the script: %w", err)
	}
	return nil
}

func (c *Client) signUTXOBCH(redeemTx *bchwire.MsgTx, amount int64, sourceScript []byte, idx int) error {
	signable := bchtxscript.NewPrivateKeySignable((*bchec.PrivateKey)(c.privKey))
	sig, err := bchtxscript.RawTxInECDSASignature(redeemTx, idx, sourceScript, bchtxscript.SigHashAll, signable, amount)
	if err != nil {
		return fmt.Errorf("fail to get witness: %w", err)
	}

	pkData := signable.GetPubKey().SerializeCompressed()
	sigscript, err := bchtxscript.NewScriptBuilder().AddData(sig).AddData(pkData).Script()
	if err != nil {
		return fmt.Errorf("fail to build signature script: %w", err)
	}
	redeemTx.TxIn[idx].SignatureScript = sigscript
	flag := bchtxscript.StandardVerifyFlags
	engine, err := bchtxscript.NewEngine(sourceScript, redeemTx, idx, flag, nil, nil, amount)
	if err != nil {
		return fmt.Errorf("fail to create engine: %w", err)
	}
	if err = engine.Execute(); err != nil {
		return fmt.Errorf("fail to execute the script: %w", err)
	}
	return nil
}

func (c *Client) signUTXOLTC(redeemTx *ltcwire.MsgTx, amount int64, sourceScript []byte, idx int) error {
	sigHashes := ltctxscript.NewTxSigHashes(redeemTx)
	signable := ltctxscript.NewPrivateKeySignable((*ltcec.PrivateKey)(c.privKey))
	witness, err := ltctxscript.WitnessSignature(redeemTx, sigHashes, idx, amount, sourceScript, ltctxscript.SigHashAll, signable, true)
	if err != nil {
		return fmt.Errorf("fail to get witness: %w", err)
	}

	redeemTx.TxIn[idx].Witness = witness
	flag := ltctxscript.StandardVerifyFlags
	engine, err := ltctxscript.NewEngine(sourceScript, redeemTx, idx, flag, nil, nil, amount)
	if err != nil {
		return fmt.Errorf("fail to create engine: %w", err)
	}
	if err = engine.Execute(); err != nil {
		return fmt.Errorf("fail to execute the script: %w", err)
	}
	return nil
}

func (c *Client) signUTXOBTC(redeemTx *btcwire.MsgTx, amount int64, sourceScript []byte, idx int) error {
	sigHashes := btctxscript.NewTxSigHashes(redeemTx)
	signable := btctxscript.NewPrivateKeySignable(c.privKey)
	witness, err := btctxscript.WitnessSignature(redeemTx, sigHashes, idx, amount, sourceScript, btctxscript.SigHashAll, signable, true)
	if err != nil {
		return fmt.Errorf("fail to get witness: %w", err)
	}

	redeemTx.TxIn[idx].Witness = witness
	flag := btctxscript.StandardVerifyFlags
	engine, err := btctxscript.NewEngine(sourceScript, redeemTx, idx, flag, nil, nil, amount)
	if err != nil {
		return fmt.Errorf("fail to create engine: %w", err)
	}
	if err = engine.Execute(); err != nil {
		return fmt.Errorf("fail to execute the script: %w", err)
	}
	return nil
}

// signZECTx handles ZEC transaction building and signing using the Rust library
func (c *Client) signZECTx(tx SimTx) ([]byte, error) {
	// Get the current block height
	blockCount, err := c.rpc.GetBlockCount()
	if err != nil {
		return nil, fmt.Errorf("fail to get block count: %w", err)
	}
	currentHeight := uint32(blockCount)

	// First, estimate the fee with a minimal transaction (1 input, 2 outputs)
	paymentAmountBTC := c.getPaymentAmount(tx)
	paymentAmountSats := int64(paymentAmountBTC * 100000000)
	estimatedFee := zec.CalculateFeeWithMemo(1, 2, tx.Memo)

	// Get UTXOs to spend - include estimated fee in the target amount (in BTC)
	targetAmountBTC := paymentAmountBTC + float64(estimatedFee)/100000000
	utxos, err := c.getUtxoToSpend(targetAmountBTC)
	if err != nil {
		return nil, fmt.Errorf("fail to get UTXOs to spend: %w", err)
	}

	// Convert UTXOs to ZEC format and calculate total input
	zecInputs := make([]zec.Utxo, len(utxos))
	totalInput := uint64(0)
	for i, utxo := range utxos {
		// Use the script directly from the UTXO
		scriptHex := utxo.ScriptPubKey

		// Calculate the block height from confirmations
		var height uint32
		if utxo.Confirmations > 0 {
			// For confirmed transactions, calculate the height
			blockCount, _ := c.rpc.GetBlockCount()
			height = uint32(blockCount - utxo.Confirmations + 1)
		}

		zecInputs[i] = zec.Utxo{
			Txid:   utxo.TxID,
			Vout:   utxo.Vout,
			Script: scriptHex,
			Value:  uint64(utxo.Amount * 100000000), // Convert BTC to zatoshis
			Height: height,
		}
		totalInput += uint64(utxo.Amount * 100000000)
	}

	// Now calculate the actual fee based on the real number of inputs
	fee := zec.CalculateFeeWithMemo(uint64(len(utxos)), 2, tx.Memo)

	// Check if we have enough inputs for payment + fee
	requiredAmount := uint64(paymentAmountSats) + fee
	if totalInput < requiredAmount {
		// Try to get more UTXOs
		targetAmountBTC = paymentAmountBTC + float64(fee)/100000000
		utxos, err = c.getUtxoToSpend(targetAmountBTC)
		if err != nil {
			return nil, fmt.Errorf("fail to get sufficient UTXOs: %w", err)
		}

		// Re-convert UTXOs to ZEC format
		zecInputs = make([]zec.Utxo, len(utxos))
		totalInput = 0
		for i, utxo := range utxos {
			scriptHex := utxo.ScriptPubKey
			var height uint32
			if utxo.Confirmations > 0 {
				blockCount, _ := c.rpc.GetBlockCount()
				height = uint32(blockCount - utxo.Confirmations + 1)
			}
			zecInputs[i] = zec.Utxo{
				Txid:   utxo.TxID,
				Vout:   utxo.Vout,
				Script: scriptHex,
				Value:  uint64(utxo.Amount * 100000000),
				Height: height,
			}
			totalInput += uint64(utxo.Amount * 100000000)
		}

		// Recalculate fee with actual number of inputs
		fee = zec.CalculateFeeWithMemo(uint64(len(utxos)), 2, tx.Memo)
		requiredAmount = uint64(paymentAmountSats) + fee

		if totalInput < requiredAmount {
			return nil, fmt.Errorf("insufficient funds: have %d zatoshis, need %d zatoshis", totalInput, requiredAmount)
		}
	}

	// Calculate change amount
	changeAmount := totalInput - uint64(paymentAmountSats) - fee

	// Build outputs - send to destination and change back to source
	outputs := []zec.Output{
		{
			Address: tx.ToAddress.String(),
			Amount:  uint64(paymentAmountSats),
			Memo:    tx.Memo,
		},
	}

	// Add change output if needed
	if changeAmount > 0 {
		outputs = append(outputs, zec.Output{
			Address: c.address.String(),
			Amount:  changeAmount,
			Memo:    "",
		})
	}

	// Get the vault public key (using the compressed public key)
	vaultPubKey := c.privKey.PubKey().SerializeCompressed()

	// Determine network
	var network zec.Network
	switch common.CurrentChainNetwork {
	case common.MockNet:
		network = zec.NetworkRegtest
	case common.MainNet:
		network = zec.NetworkMain
	case common.StageNet:
		network = zec.NetworkTest
	default:
		return nil, fmt.Errorf("unsupported network: %s", common.CurrentChainNetwork)
	}

	// Build the partial transaction
	ptx := zec.PartialTx{
		Height:       currentHeight,
		Inputs:       zecInputs,
		Outputs:      outputs,
		Fee:          fee,
		ExpiryHeight: 0, // No expiry
	}

	// Build the transaction
	bptx, err := zec.BuildPtx(vaultPubKey, ptx, network)
	if err != nil {
		return nil, fmt.Errorf("fail to build ZEC transaction: %w", err)
	}

	// Sign the sighashes - need to create a v2 private key from the same bytes
	privateKeyBytes := c.privKey.Serialize()
	privKeyV2, _ := btcecv2.PrivKeyFromBytes(privateKeyBytes)

	signatures := make([][]byte, len(bptx.Sighashes))
	for i, sighash := range bptx.Sighashes {
		sig := ecdsa.Sign(privKeyV2, sighash).Serialize()
		signatures[i] = sig
	}

	// Apply signatures to get the final transaction
	rawTx, err := zec.ApplySignatures(vaultPubKey, bptx, signatures, network)
	if err != nil {
		return nil, fmt.Errorf("fail to apply signatures: %w", err)
	}

	// Mark all used UTXOs as spent (ZEC only needs this because getaddressutxos lacks mempool awareness)
	c.muSpentUtxos.Lock()
	for _, utxo := range zecInputs {
		utxoKey := fmt.Sprintf("%s:%d", utxo.Txid, utxo.Vout)
		c.spentUtxos[utxoKey] = struct{}{}
	}
	c.muSpentUtxos.Unlock()

	return rawTx, nil
}
