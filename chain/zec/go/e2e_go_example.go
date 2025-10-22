// go run main.go
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"gitlab.com/mayachain/mayanode/chain/zec/go/zec"
)

const (
	testMnemonic   = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	rpcUser        = "mayachain"
	rpcPass        = "password"
	rpcURL         = "http://localhost:18232"
	defaultFeeZats = uint64(50_000) // simple flat fee for these regtest demos
	confirmMineN   = 3
	regtestNetwork = zec.NetworkRegtest
)

// ---------------------------
// Minimal zcashd RPC client
// ---------------------------

type ZcashRPC struct {
	BaseURL string
	User    string
	Pass    string
	Client  *http.Client
}

type rpcReq struct {
	Jsonrpc string        `json:"jsonrpc"`
	ID      string        `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcResp struct {
	Result interface{}   `json:"result"`
	Error  *rpcRespError `json:"error"`
	ID     string        `json:"id"`
}

type rpcRespError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func NewZcashRPC(base, user, pass string) *ZcashRPC {
	return &ZcashRPC{
		BaseURL: base,
		User:    user,
		Pass:    pass,
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (z *ZcashRPC) call(method string, params ...interface{}) (interface{}, error) {
	reqBody := rpcReq{Jsonrpc: "1.0", ID: "goffi", Method: method, Params: params}
	raw, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", z.BaseURL, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(z.User, z.Pass)

	resp, err := z.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out rpcResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("RPC error: code=%d msg=%s", out.Error.Code, out.Error.Message)
	}
	return out.Result, nil
}

func (z *ZcashRPC) GetBlockCount() (int, error) {
	r, err := z.call("getblockcount")
	if err != nil {
		return 0, err
	}
	switch v := r.(type) {
	case json.Number:
		i64, err := v.Int64()
		if err != nil {
			return 0, err
		}
		return int(i64), nil
	case float64:
		return int(v), nil
	default:
		return 0, errors.New("getblockcount: bad result type")
	}
}

func (z *ZcashRPC) Generate(n int) error {
	_, err := z.call("generate", int64(n))
	return err
}

func (z *ZcashRPC) SendRawTransaction(txHex string) (string, error) {
	r, err := z.call("sendrawtransaction", txHex)
	if err != nil {
		return "", err
	}
	s, ok := r.(string)
	if !ok {
		return "", errors.New("sendrawtransaction: bad result type")
	}
	return s, nil
}

// Return hex string (verbosity=0 MUST be an integer; bool causes
// "JSON value is not an integer as expected" on zcashd).
func (z *ZcashRPC) GetRawTransactionHex(txid string) (string, error) {
	r, err := z.call("getrawtransaction", txid, 0)
	if err != nil {
		return "", err
	}
	s, ok := r.(string)
	if !ok {
		return "", errors.New("getrawtransaction(0): bad result type")
	}
	return s, nil
}

// Return verbose JSON (map) (verbosity=1)
func (z *ZcashRPC) GetRawTransactionVerbose(txid string) (map[string]interface{}, error) {
	r, err := z.call("getrawtransaction", txid, 1)
	if err != nil {
		return nil, err
	}
	m, ok := r.(map[string]interface{})
	if !ok {
		return nil, errors.New("getrawtransaction(1): bad result type")
	}
	return m, nil
}

type ZcashUTXO struct {
	Txid   string
	Vout   uint32
	Script string
	Value  uint64
	Height uint32
}

// zcashd (regtest) utility call (no wallet): getaddressutxos
func (z *ZcashRPC) GetAddressUTXOs(address string) ([]ZcashUTXO, error) {
	r, err := z.call("getaddressutxos", map[string]interface{}{"addresses": []string{address}})
	if err != nil {
		return nil, err
	}
	arr, ok := r.([]interface{})
	if !ok {
		return nil, errors.New("getaddressutxos: bad result type")
	}
	utxos := make([]ZcashUTXO, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]interface{})
		if !ok {
			return nil, errors.New("getaddressutxos: invalid type assertion")
		}
		txid, ok := m["txid"].(string)
		if !ok {
			return nil, errors.New("getaddressutxos: txid not a string")
		}
		script, ok := m["script"].(string)
		if !ok {
			return nil, errors.New("getaddressutxos: script not a string")
		}

		// Numbers may be float64 (default) or json.Number (when UseNumber is enabled).
		toUint32 := func(val interface{}, field string) (uint32, error) {
			switch n := val.(type) {
			case json.Number:
				i64, err := n.Int64()
				if err != nil {
					return 0, fmt.Errorf("getaddressutxos: %s: %w", field, err)
				}
				if i64 < 0 || i64 > math.MaxUint32 {
					return 0, fmt.Errorf("getaddressutxos: %s out of range", field)
				}
				return uint32(i64), nil
			case float64:
				if n < 0 || n > math.MaxUint32 || n != math.Trunc(n) {
					return 0, fmt.Errorf("getaddressutxos: %s invalid float", field)
				}
				return uint32(n), nil
			default:
				return 0, fmt.Errorf("getaddressutxos: %s not a number", field)
			}
		}

		toUint64 := func(val interface{}, field string) (uint64, error) {
			switch n := val.(type) {
			case json.Number:
				i64, err := n.Int64()
				if err != nil {
					return 0, fmt.Errorf("getaddressutxos: %s: %w", field, err)
				}
				if i64 < 0 {
					return 0, fmt.Errorf("getaddressutxos: %s negative", field)
				}
				return uint64(i64), nil
			case float64:
				if n < 0 || n != math.Trunc(n) {
					return 0, fmt.Errorf("getaddressutxos: %s invalid float", field)
				}
				// Note: zcash amounts fit in int64; guard against overflow into uint64
				if n > float64(^uint64(0)) {
					return 0, fmt.Errorf("getaddressutxos: %s out of range", field)
				}
				return uint64(n), nil
			default:
				return 0, fmt.Errorf("getaddressutxos: %s not a number", field)
			}
		}

		vout, err := toUint32(m["outputIndex"], "outputIndex")
		if err != nil {
			return nil, err
		}
		height, err := toUint32(m["height"], "height")
		if err != nil {
			return nil, err
		}
		value, err := toUint64(m["satoshis"], "satoshis")
		if err != nil {
			return nil, err
		}

		utxos = append(utxos, ZcashUTXO{
			Txid:   txid,
			Vout:   vout,
			Script: script,
			Value:  value,
			Height: height,
		})
	}
	return utxos, nil
}

// ---------------------------
// Signing helper
// ---------------------------

func signSighashes(sighashes [][]byte, priv *btcec.PrivateKey) ([][]byte, error) {
	sigs := make([][]byte, len(sighashes))
	for i, sh := range sighashes {
		s := ecdsa.Sign(priv, sh).Serialize() // DER (no sighash flag)
		fmt.Printf("   Signed sighash %d: %s\n", i, hex.EncodeToString(s))
		sigs[i] = s
	}
	return sigs, nil
}

// ---------------------------
// Block tx fetcher (robust)
// ---------------------------

// For heights [1..height], return [][]txBytes grouped per block.
// Uses getblock (verbosity=1) → txids, then getrawtransaction(0) for each.
//
//nolint:forcetypeassert
func fetchBlockTxsUpTo(rpc *ZcashRPC, height uint32) ([][][]byte, error) {
	blocks := make([][][]byte, 0, height)
	for h := uint32(1); h <= height; h++ {
		hashAny, err := rpc.call("getblockhash", int64(h))
		if err != nil {
			return nil, fmt.Errorf("getblockhash(%d): %w", h, err)
		}
		hash, ok := hashAny.(string)
		if !ok {
			return nil, errors.New("getblockhash: invalid type assertion")
		}

		blkAny, err := rpc.call("getblock", hash, 1)
		if err != nil {
			return nil, fmt.Errorf("getblock(%s): %w", hash, err)
		}
		blk, ok := blkAny.(map[string]interface{})
		if !ok {
			return nil, errors.New("getblock: unexpected block shape")
		}
		txAny, ok := blk["tx"]
		if !ok {
			return nil, errors.New("getblock: missing tx field")
		}
		txids, ok := txAny.([]interface{})
		if !ok {
			return nil, errors.New("getblock: tx field not list")
		}

		btxs := make([][]byte, 0, len(txids))
		for _, it := range txids {
			txid := it.(string)
			hexStr, err := rpc.GetRawTransactionHex(txid)
			if err != nil {
				return nil, fmt.Errorf("getrawtransaction(%s): %w", txid, err)
			}
			tb, err := hex.DecodeString(hexStr)
			if err != nil {
				return nil, fmt.Errorf("decode tx %s: %w", txid, err)
			}
			btxs = append(btxs, tb)
		}
		blocks = append(blocks, btxs)
	}
	return blocks, nil
}

// ---------------------------
// Main
// ---------------------------

func main() {
	fmt.Println("=== Real Go FFI Test: T->Sapling->T & T->Orchard->T ===")
	fmt.Println("Using REAL zec library FFI bindings!")
	fmt.Println("")

	// Init Rust zec lib (loads proving params once).
	if err := zec.InitZec(); err != nil {
		log.Fatalf("InitZec failed: %v", err)
	}
	fmt.Println("✅ Rust library initialized")

	rpc := NewZcashRPC(rpcURL, rpcUser, rpcPass)

	// Connectivity check
	tip, err := rpc.GetBlockCount()
	if err != nil {
		log.Fatalf("zcashd connect failed: %v", err)
	}
	fmt.Printf("✅ Connected to zcashd, current height: %d\n", tip)

	// Derive our account (ZIP32 from test mnemonic)
	acct, err := zec.DeriveAccountFromMnemonic(testMnemonic, regtestNetwork)
	if err != nil {
		log.Fatalf("DeriveAccount failed: %v", err)
	}
	fmt.Printf("✅ Derived account:\n  Transparent: %s\n  Sapling:     %s\n  Orchard:     %s\n\n",
		acct.Transparent, acct.Sapling, acct.Orchard)

	// Get a UTXO to spend (try our T-addr first, fallback to known regtest miner addr)
	candidates := []string{acct.Transparent, "tmV1zYhR2xisn6VWdCNKHpeD4S7L1U1nPH6"}
	var utxos []ZcashUTXO
	var spendAddr string
	for _, a := range candidates {
		if u, err := rpc.GetAddressUTXOs(a); err == nil && len(u) > 0 {
			utxos, spendAddr = u, a
			break
		}
	}
	if len(utxos) == 0 {
		fmt.Println("⚠️  No UTXOs found on candidate addresses; check -mineraddress.")
		return
	}
	utxo := utxos[0]
	fmt.Printf("✅ Found UTXO: %d satoshis in %s\n   Using address: %s\n\n",
		utxo.Value, utxo.Txid, spendAddr)

	// ========== Test 1: T -> Sapling -> T ==========
	fmt.Println("=== Test 1: Complete T -> Sapling -> T Flow ===")
	if !runSaplingFlow(rpc, &acct, utxo, uint32(tip)) {
		fmt.Println("❌ Sapling flow failed")
	} else {
		fmt.Println("✅ Sapling flow OK")
	}
	fmt.Println("")

	// Refresh UTXOs for Orchard test
	if u, err := rpc.GetAddressUTXOs(spendAddr); err == nil && len(u) > 0 {
		utxo = u[0]
	} else {
		fmt.Println("⚠️  No UTXOs available for Orchard test")
		return
	}

	// ========== Test 2: T -> Orchard -> T ==========
	fmt.Println("=== Test 2: Complete T -> Orchard -> T Flow ===")
	if !runOrchardFlow(rpc, &acct, utxo, uint32(tip)) {
		fmt.Println("❌ Orchard flow failed")
	} else {
		fmt.Println("✅ Orchard flow OK")
	}
	fmt.Println("")

	fmt.Println("🎉 Done.")
}

// ---------------------------
// Sapling flow
// ---------------------------

func runSaplingFlow(rpc *ZcashRPC, acct *zec.ZecAccount, utxo ZcashUTXO, currentHeight uint32) bool {
	fmt.Println("Phase 1: T -> Sapling (shield)")

	// Build T->Sapling PTX
	if utxo.Value <= defaultFeeZats {
		fmt.Println("❌ insufficient UTXO to cover fee")
		return false
	}
	sendAmt := utxo.Value - defaultFeeZats
	ptx := zec.PartialTx{
		Height: currentHeight,
		Inputs: []zec.Utxo{{
			Txid:   utxo.Txid,
			Vout:   utxo.Vout,
			Script: utxo.Script,
			Value:  utxo.Value,
			Height: utxo.Height,
		}},
		Outputs: []zec.Output{{
			Address: acct.Sapling,
			Amount:  sendAmt,
			Memo:    "Go FFI Sapling Test",
		}},
		Fee:          defaultFeeZats,
		ExpiryHeight: 0,
	}
	bptx, err := zec.BuildPtx(acct.PublicKey, ptx, regtestNetwork)
	if err != nil {
		fmt.Printf("❌ BuildPtx T->Sapling: %v\n", err)
		return false
	}
	fmt.Printf("   Built T->Sapling PTX with %d sighashes\n", len(bptx.Sighashes))

	// Sign transparent inputs
	priv, _ := btcec.PrivKeyFromBytes(acct.PrivateKey)
	sigs, err := signSighashes(bptx.Sighashes, priv)
	if err != nil {
		fmt.Printf("❌ sign: %v\n", err)
		return false
	}
	var raw []byte
	raw, err = zec.ApplySignatures(acct.PublicKey, bptx, sigs, regtestNetwork)
	if err != nil {
		fmt.Printf("❌ ApplySignatures: %v\n", err)
		return false
	}

	txid, err := rpc.SendRawTransaction(hex.EncodeToString(raw))
	if err != nil {
		fmt.Printf("❌ Broadcast shield: %v\n", err)
		return false
	}
	fmt.Printf("✅ Broadcast T->Sapling tx: %s\n", txid)

	// Mine a few blocks and fetch the *transaction height* directly
	if err = rpc.Generate(confirmMineN); err != nil {
		fmt.Printf("❌ generate: %v\n", err)
		return false
	}
	v, err := rpc.GetRawTransactionVerbose(txid)
	if err != nil {
		fmt.Printf("❌ getrawtransaction(1): %v\n", err)
		return false
	}
	var anchorHeight uint32
	switch hv := v["height"].(type) {
	case json.Number:
		i64, err2 := hv.Int64()
		if err2 != nil {
			fmt.Printf("❌ height parse: %v\n", err2)
			return false
		}
		anchorHeight = uint32(i64)
	case float64:
		anchorHeight = uint32(hv)
	default:
		fmt.Println("❌ unexpected height type")
		return false
	}
	fmt.Printf("✅ T->Sapling transaction mined at height %d\n", anchorHeight)

	fmt.Println("Phase 2: Sapling -> T (unshield)")

	// Decrypt our Sapling note from the shielding tx bytes
	txHex, err := rpc.GetRawTransactionHex(txid)
	if err != nil {
		fmt.Printf("❌ getrawtransaction(0): %v\n", err)
		return false
	}
	txBytes, _ := hex.DecodeString(txHex)

	notes, err := zec.DecryptNotesInTx(testMnemonic, regtestNetwork, txBytes, anchorHeight)
	if err != nil || len(notes) == 0 {
		fmt.Printf("❌ DecryptNotesInTx: %v (len=%d)\n", err, len(notes))
		return false
	}
	n := notes[0]
	fmt.Printf("✅ Decrypted Sapling note: %d zats\n", n.Value)

	// Build witness up to the tx height
	blocks, err := fetchBlockTxsUpTo(rpc, anchorHeight)
	if err != nil {
		fmt.Printf("❌ fetch blocks: %v\n", err)
		return false
	}
	wp, err := zec.BuildWitnessFromBlocks(
		testMnemonic,
		regtestNetwork,
		n.Diversifier,
		n.Value,
		n.Rseed,
		n.Zip212,
		blocks,
		zec.BranchIdNu5,
		nil, // let Rust verify/compute the anchor
	)
	if err != nil {
		fmt.Printf("❌ BuildWitnessFromBlocks: %v\n", err)
		return false
	}

	// Spend back to T
	spend := zec.SpendableNote{
		Diversifier: n.Diversifier,
		Value:       n.Value,
		Rseed:       n.Rseed,
		Zip212:      n.Zip212,
		Witness:     wp.WitnessBytes,
	}
	targetHeight := anchorHeight + 50
	outBytes, err := zec.SendFromSaplingToTransparent(
		testMnemonic,
		regtestNetwork,
		[]zec.SpendableNote{spend},
		acct.Transparent,
		wp.AnchorBytes,
		targetHeight,
		nil,
	)
	if err != nil {
		fmt.Printf("❌ SendFromSaplingToTransparent: %v\n", err)
		return false
	}
	unshieldTxid, err := rpc.SendRawTransaction(hex.EncodeToString(outBytes))
	if err != nil {
		fmt.Printf("❌ Broadcast Sapling->T: %v\n", err)
		return false
	}
	if err := rpc.Generate(confirmMineN); err != nil {
		fmt.Printf("❌ generate: %v\n", err)
		return false
	}
	fmt.Printf("✅ Sapling->T transaction confirmed: %s\n", unshieldTxid)
	return true
}

// ---------------------------
// Orchard flow
// ---------------------------

//nolint:govet // shadow: we intentionally reuse err in this function
//nolint:forcetypeassert
func runOrchardFlow(rpc *ZcashRPC, acct *zec.ZecAccount, utxo ZcashUTXO, currentHeight uint32) bool {
	fmt.Println("Phase 1: T -> Orchard (shield)")

	// Build T->Orchard PTX
	if utxo.Value <= defaultFeeZats {
		fmt.Println("❌ insufficient UTXO to cover fee")
		return false
	}
	sendAmt := utxo.Value - defaultFeeZats
	ptx := zec.PartialTx{
		Height: currentHeight,
		Inputs: []zec.Utxo{{
			Txid:   utxo.Txid,
			Vout:   utxo.Vout,
			Script: utxo.Script,
			Value:  utxo.Value,
			Height: utxo.Height,
		}},
		Outputs: []zec.Output{{
			Address: acct.Orchard,
			Amount:  sendAmt,
			Memo:    "Go FFI Orchard Test",
		}},
		Fee:          defaultFeeZats,
		ExpiryHeight: 0,
	}
	bptx, err := zec.BuildPtx(acct.PublicKey, ptx, regtestNetwork)
	if err != nil {
		fmt.Printf("❌ BuildPtx T->Orchard: %v\n", err)
		return false
	}
	fmt.Printf("   Built T->Orchard PTX with %d sighashes\n", len(bptx.Sighashes))

	// Sign + broadcast
	priv, _ := btcec.PrivKeyFromBytes(acct.PrivateKey)
	sigs, err := signSighashes(bptx.Sighashes, priv)
	if err != nil {
		fmt.Printf("❌ sign: %v\n", err)
		return false
	}
	raw, err := zec.ApplySignatures(acct.PublicKey, bptx, sigs, regtestNetwork)
	if err != nil {
		fmt.Printf("❌ ApplySignatures: %v\n", err)
		return false
	}
	txid, err := rpc.SendRawTransaction(hex.EncodeToString(raw))
	if err != nil {
		fmt.Printf("❌ Broadcast shield: %v\n", err)
		return false
	}
	fmt.Printf("✅ Broadcast T->Orchard tx: %s\n", txid)

	// Mine & fetch tx height
	if err := rpc.Generate(confirmMineN); err != nil {
		fmt.Printf("❌ generate: %v\n", err)
		return false
	}
	v, err := rpc.GetRawTransactionVerbose(txid)
	if err != nil {
		fmt.Printf("❌ getrawtransaction(1): %v\n", err)
		return false
	}
	var anchorHeight uint32
	switch hv := v["height"].(type) {
	case json.Number:
		i64, err := hv.Int64()
		if err != nil {
			fmt.Printf("❌ height parse: %v\n", err)
			return false
		}
		anchorHeight = uint32(i64)
	case float64:
		anchorHeight = uint32(hv)
	default:
		fmt.Println("❌ unexpected height type")
		return false
	}
	fmt.Printf("✅ T->Orchard transaction mined at height %d\n", anchorHeight)

	fmt.Println("Phase 2: Orchard -> T (unshield)")

	// Decrypt Orchard note
	txHex, err := rpc.GetRawTransactionHex(txid)
	if err != nil {
		fmt.Printf("❌ getrawtransaction(0): %v\n", err)
		return false
	}
	txBytes, _ := hex.DecodeString(txHex)

	notes, err := zec.DecryptOrchardNotesInTx(testMnemonic, regtestNetwork, txBytes, anchorHeight)
	if err != nil || len(notes) == 0 {
		fmt.Printf("❌ DecryptOrchardNotesInTx: %v (len=%d)\n", err, len(notes))
		return false
	}
	n := notes[0]
	fmt.Printf("✅ Decrypted Orchard note: %d zats\n", n.Value)

	// Build witness up to tx height
	blocks, err := fetchBlockTxsUpTo(rpc, anchorHeight)
	if err != nil {
		fmt.Printf("❌ fetch blocks: %v\n", err)
		return false
	}
	wp, err := zec.BuildOrchardWitnessFromBlocks(
		testMnemonic,
		regtestNetwork,
		n.Diversifier,
		n.Value,
		n.Rseed,
		n.Rho,
		n.Cmx,
		blocks,
		zec.BranchIdNu5,
		nil,
	)
	if err != nil {
		fmt.Printf("❌ BuildOrchardWitnessFromBlocks: %v\n", err)
		return false
	}

	// Spend back to T
	spend := zec.OrchardSpendableNote{
		Diversifier: n.Diversifier,
		Value:       n.Value,
		Rseed:       n.Rseed,
		Rho:         n.Rho,
		Witness:     wp.WitnessBytes,
		// optional fields kept for completeness (ignored by many FFI versions):
		Cmx:       n.Cmx,
		AddrBytes: n.AddrBytes,
	}
	targetHeight := anchorHeight + 50
	outBytes, err := zec.SendFromOrchardToTransparent(
		testMnemonic,
		regtestNetwork,
		[]zec.OrchardSpendableNote{spend},
		acct.Transparent,
		wp.AnchorBytes,
		targetHeight,
		nil,
	)
	if err != nil {
		fmt.Printf("❌ SendFromOrchardToTransparent: %v\n", err)
		return false
	}
	unshieldTxid, err := rpc.SendRawTransaction(hex.EncodeToString(outBytes))
	if err != nil {
		fmt.Printf("❌ Broadcast Orchard->T: %v\n", err)
		return false
	}
	if err := rpc.Generate(confirmMineN); err != nil {
		fmt.Printf("❌ generate: %v\n", err)
		return false
	}
	fmt.Printf("✅ Orchard->T transaction confirmed: %s\n", unshieldTxid)
	return true
}
