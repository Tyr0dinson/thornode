package utxo

import (
	zectxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/zec-txscript"
)

// GetAddressesFromScriptPubKeyZEC is a helper function for UTXO ownership verification.
// It handles both pre-parsed addresses from RPC and hex-encoded scriptPubKey.
// This is a thin wrapper around the zec-txscript package.
func GetAddressesFromScriptPubKeyZEC(scriptPubKeyHex string, rpcAddresses []string, networkParams *NetworkParams) ([]string, error) {
	return zectxscript.GetAddressesFromScriptPubKeyHex(scriptPubKeyHex, rpcAddresses, networkParams)
}
