package txscript

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcutil/base58"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ripemd160"
)

// Network is the Zcash network type
type Network uint

const (
	NetworkMain    Network = 1
	NetworkRegtest Network = 2
	NetworkTest    Network = 3
)

// NetworkParams defines Zcash-specific network parameters
type NetworkParams struct {
	Name                        string  // human-readable identifier for the network
	Net                         Network // value used to identify the network
	P2PKHPrefix                 []byte  // prefixes for P2PKH transparent addresses ([2]byte)
	P2SHPrefix                  []byte  // prefixes for P2SH transparent addresses ([2]byte)
	ShieldedSourceMarkerAddress string  // used as a marker for txs that have shielded source
}

// ScriptClass represents the class of a script (standard types).
type ScriptClass byte

const (
	// NonStandardTy is a non-standard script.
	NonStandardTy ScriptClass = iota
	// PubKeyTy is a pay-to-pubkey script.
	PubKeyTy
	// PubKeyHashTy is a pay-to-pubkey-hash script.
	PubKeyHashTy
	// ScriptHashTy is a pay-to-script-hash script.
	ScriptHashTy
	// MultiSigTy is a multi-signature script.
	MultiSigTy
	// NullDataTy is a null data script (OP_RETURN).
	NullDataTy
)

// AddressType represents the type of Zcash transparent address
type addressType int

const (
	p2pkh addressType = iota
	p2sh

	OP_DUP                = 0x76
	OP_HASH160            = 0xa9
	OP_PUSH20             = 0x14
	OP_EQUALVERIFY        = 0x88
	OP_CHECKSIG           = 0xac
	OP_EQUAL              = 0x87
	OP_PUSH33             = 0x21
	OP_PUSH65             = 0x41
	OP_RETURN             = 0x6a
	p2shScriptSize        = 23
	p2pkhScriptSize       = 25
	compressedKeyLen      = 33
	uncompressedKeyLen    = 65
	compressedKeyPrefix1  = 0x02
	compressedKeyPrefix2  = 0x03
	uncompressedKeyPrefix = 0x04
)

// ExtractPkScriptAddrs returns addresses from Zcash scriptPubKey (transparent only).
// This matches the BCH/LTC/DOGE pattern for compatibility with UTXO client.
func ExtractPkScriptAddrs(pkScript []byte, networkParams *NetworkParams) (scriptClass ScriptClass, addresses []string, requireSigs int, err error) {
	switch {
	case isPayToPubKeyHash(pkScript):
		hash := extractHash160PKH(pkScript)
		if hash == nil {
			return NonStandardTy, nil, 0, errors.New("could not extract hash160 from P2PKH script")
		}
		addr, err := encodeTransparentAddress(hash, p2pkh, networkParams)
		if err != nil {
			return NonStandardTy, nil, 0, err
		}
		return PubKeyHashTy, []string{addr}, 1, nil

	case isPayToScriptHash(pkScript):
		hash := extractHash160SH(pkScript)
		if hash == nil {
			return NonStandardTy, nil, 0, errors.New("could not extract hash160 from P2SH script")
		}
		addr, err := encodeTransparentAddress(hash, p2sh, networkParams)
		if err != nil {
			return NonStandardTy, nil, 0, err
		}
		return ScriptHashTy, []string{addr}, 1, nil

	case isPayToPubKey(pkScript):
		pubkey, err := extractPubKey(pkScript)
		if err != nil {
			return NonStandardTy, nil, 0, errors.Wrap(err, "failed to extract Pub key from pkScript")
		}
		// P2PK scripts resolve to P2PKH addresses
		addr, err := encodeTransparentAddress(hash160(pubkey), p2pkh, networkParams)
		if err != nil {
			return NonStandardTy, nil, 0, err
		}
		return PubKeyTy, []string{addr}, 1, nil

	case isNullData(pkScript):
		return NullDataTy, nil, 0, nil

	default:
		// Could be multisig or other non-standard scripts
		return NonStandardTy, nil, 0, nil
	}
}

// isPayToPubKeyHash checks if a script is a P2PKH script
func isPayToPubKeyHash(script []byte) bool {
	return len(script) == p2pkhScriptSize &&
		script[0] == OP_DUP &&
		script[1] == OP_HASH160 &&
		script[2] == OP_PUSH20 &&
		script[23] == OP_EQUALVERIFY &&
		script[24] == OP_CHECKSIG
}

// isPayToScriptHash checks if a script is a P2SH script
func isPayToScriptHash(script []byte) bool {
	return len(script) == p2shScriptSize &&
		script[0] == OP_HASH160 &&
		script[1] == OP_PUSH20 &&
		script[22] == OP_EQUAL
}

// isPayToPubKey checks if a script is a P2PK script
func isPayToPubKey(script []byte) bool {
	scriptLen := len(script)
	if scriptLen != 35 && scriptLen != 67 {
		return false
	}
	return (script[0] == OP_PUSH33 || script[0] == OP_PUSH65) &&
		script[scriptLen-1] == OP_CHECKSIG
}

// isNullData checks if a script is a null data (OP_RETURN) script
func isNullData(script []byte) bool {
	return len(script) > 0 && script[0] == OP_RETURN
}

// extractHash160PKH extracts the 20-byte hash from a P2PKH script
func extractHash160PKH(script []byte) []byte {
	// P2PKH: OP_DUP OP_HASH160 <0x14> <20-byte-hash> OP_EQUALVERIFY OP_CHECKSIG
	// Hex:   76     a9        14     <20-byte-hash> 88             ac
	if len(script) == 25 && script[2] == 0x14 {
		return script[3 : 3+20]
	}
	return nil
}

// extractHash160SH extracts the 20-byte hash from a P2SH script
func extractHash160SH(script []byte) []byte {
	// P2SH: OP_HASH160 <0x14> <20-byte-hash> OP_EQUAL
	// Hex:  a9         14     <20-byte-hash> 87
	if len(script) == 23 && script[1] == 0x14 {
		return script[2 : 2+20]
	}
	return nil
}

// encodeTransparentAddress encodes a hash to a base58-encoded Zcash transparent address
// with the appropriate 2-byte version prefix for P2PKH/P2SH addresses.
// This is Zcash-specific and differs from Bitcoin's 1-byte prefix.
func encodeTransparentAddress(hash []byte, addrType addressType, networkParams *NetworkParams) (string, error) {
	var prefixBytes []byte
	switch addrType {
	case p2pkh:
		prefixBytes = networkParams.P2PKHPrefix
	case p2sh:
		prefixBytes = networkParams.P2SHPrefix
	default:
		return "", errors.New("unknown address type for prefix")
	}

	if len(prefixBytes) != 2 {
		return "", fmt.Errorf("invalid prefix length for Zcash transparent address: expected 2, got %d for network %s", len(prefixBytes), networkParams.Name)
	}

	data := make([]byte, len(prefixBytes))
	copy(data, prefixBytes)
	data = append(data, hash...)
	checksum := calculateChecksum(data)
	return base58.Encode(append(data, checksum...)), nil
}

// calculateChecksum calculates the checksum for an address payload (double SHA256 hash, first 4 bytes)
func calculateChecksum(data []byte) []byte {
	firstHash := sha256.Sum256(data)
	secondHash := sha256.Sum256(firstHash[:])
	return secondHash[:4]
}

// hash160 computes SHA-256 followed by RIPEMD-160
func hash160(data []byte) []byte {
	shaHash := sha256.Sum256(data)
	hasher := ripemd160.New()
	_, err := hasher.Write(shaHash[:])
	if err != nil {
		return nil
	}
	return hasher.Sum(nil)
}

// extractPubKey extracts the public key from a pay-to-pubkey script
func extractPubKey(script []byte) ([]byte, error) {
	scriptLen := len(script)

	if scriptLen < 2 {
		return nil, errors.New("script too short")
	}

	pubKey := script[:scriptLen-1] // All bytes except last OP_CHECKSIG byte (0xac)
	pubKeyLen := len(pubKey)

	switch {
	case pubKeyLen == compressedKeyLen && (pubKey[0] == compressedKeyPrefix1 || pubKey[0] == compressedKeyPrefix2): // Compressed key
		return pubKey, nil
	case pubKeyLen == uncompressedKeyLen && pubKey[0] == uncompressedKeyPrefix: // Uncompressed key
		return pubKey, nil
	default:
		return nil, errors.New("invalid public key format")
	}
}

// GetAddressesFromScriptPubKeyHex is a helper function for UTXO ownership verification.
// It handles both pre-parsed addresses from RPC and hex-encoded scriptPubKey.
func GetAddressesFromScriptPubKeyHex(scriptPubKeyHex string, rpcAddresses []string, networkParams *NetworkParams) ([]string, error) {
	// If RPC provided addresses, return them directly
	if len(rpcAddresses) > 0 {
		return rpcAddresses, nil
	}

	if len(scriptPubKeyHex) == 0 {
		return nil, fmt.Errorf("scriptPubKey hex is empty and no pre-parsed addresses available")
	}

	buf, err := hex.DecodeString(scriptPubKeyHex)
	if err != nil {
		return nil, fmt.Errorf("fail to decode scriptPubKey hex '%s': %w", scriptPubKeyHex, err)
	}

	_, addresses, _, err := ExtractPkScriptAddrs(buf, networkParams)
	if err != nil {
		return nil, fmt.Errorf("fail to ExtractPkScriptAddrs from scriptPubKey hex '%s': %w", scriptPubKeyHex, err)
	}

	return addresses, nil
}
