package rpc

import (
	"encoding/json"
)

// TxVerbose represents verbose transaction data for Zcash, including ZEC-specific fields.
// This type handles fields that don't exist in btcjson.TxRawResult but are returned by Zcash RPC.
type TxVerbose struct {
	// Fields commonly found in btcjson.TxRawResult
	Txid          string `json:"txid"`
	Hash          string `json:"hash"` // Often same as Txid for non-segwit
	Version       int32  `json:"version"`
	Size          int32  `json:"size"`
	Vsize         *int32 `json:"vsize,omitempty"`  // Can be null in ZEC
	Weight        *int32 `json:"weight,omitempty"` // Can be null in ZEC
	LockTime      uint32 `json:"locktime"`
	Vin           []Vin  `json:"vin"`
	Vout          []Vout `json:"vout"`
	BlockHash     string `json:"blockhash,omitempty"`
	Confirmations uint32 `json:"confirmations,omitempty"`
	Time          int64  `json:"time,omitempty"`
	BlockTime     int64  `json:"blocktime,omitempty"`
	Hex           string `json:"hex"` // The raw tx hex

	// Zcash specific fields (match JSON keys from zcashd)
	Vjoinsplit      []interface{} `json:"vjoinsplit,omitempty"`
	ValueBalance    float64       `json:"valueBalance,omitempty"`
	ValueBalanceZat int64         `json:"valueBalanceZat,omitempty"`
	VShieldedSpend  []interface{} `json:"vShieldedSpend,omitempty"`
	VShieldedOutput []interface{} `json:"vShieldedOutput,omitempty"`
	AuthDigest      string        `json:"authdigest,omitempty"`
	Overwintered    bool          `json:"overwintered,omitempty"`
	VersionGroupID  string        `json:"versiongroupid,omitempty"`
	ExpiryHeight    int64         `json:"expiryheight,omitempty"`

	// Raw Orchard bundle
	Orchard json.RawMessage `json:"orchard,omitempty"`
}

// HasSprout reports presence of Sprout components (vJoinSplit)
func (t *TxVerbose) HasSprout() bool {
	return len(t.Vjoinsplit) > 0
}

// HasSapling reports presence of Sapling components.
func (t *TxVerbose) HasSapling() bool {
	// Arrays are the strongest signal; value balance is a fallback
	if len(t.VShieldedSpend) > 0 || len(t.VShieldedOutput) > 0 {
		return true
	}
	if t.ValueBalanceZat != 0 || t.ValueBalance != 0 {
		// Non-zero value balance indicates shielded funds moved (Sprout or Sapling).
		// Treat as shielded so downstream logic handles it.
		return true
	}
	return false
}

// HasOrchard reports presence of Orchard components by peeking into the raw bundle.
func (t *TxVerbose) HasOrchard() bool {
	if len(t.Orchard) == 0 || string(t.Orchard) == "null" {
		return false
	}
	// Look for an "actions" array; we don't need full decoding
	var lite struct {
		Actions []json.RawMessage `json:"actions"`
	}
	if err := json.Unmarshal(t.Orchard, &lite); err != nil {
		return false
	}
	return len(lite.Actions) > 0
}

// IsShielded reports if the transaction has any shielded components (Sprout, Sapling, or Orchard).
func (t *TxVerbose) IsShielded() bool {
	return t.HasSprout() || t.HasSapling() || t.HasOrchard()
}

// Vin represents a transaction input for Zcash
type Vin struct {
	Coinbase  string     `json:"coinbase,omitempty"`
	Txid      string     `json:"txid,omitempty"`
	Vout      uint32     `json:"vout,omitempty"`
	ScriptSig *ScriptSig `json:"scriptSig,omitempty"`
	Sequence  uint32     `json:"sequence"`
}

// IsCoinBase returns a bool to show if a Vin is a Coinbase one or not.
func (v *Vin) IsCoinBase() bool {
	return len(v.Coinbase) > 0
}

// Vout represents a transaction output for Zcash
type Vout struct {
	Value        float64      `json:"value"`
	N            uint32       `json:"n"`
	ScriptPubKey ScriptPubKey `json:"scriptPubKey"`
}

// ScriptPubKey represents the script public key for Zcash
type ScriptPubKey struct {
	Asm       string   `json:"asm"`
	Hex       string   `json:"hex"`
	ReqSigs   *int32   `json:"reqSigs,omitempty"`
	Type      string   `json:"type"`
	Addresses []string `json:"addresses,omitempty"`
}

// ScriptSig represents the script signature for Zcash
type ScriptSig struct {
	Asm string `json:"asm"`
	Hex string `json:"hex"`
}
