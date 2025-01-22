//go:build !mocknet
// +build !mocknet

package wasmpermissions

import (
	_ "embed"
)

//go:embed wasm_permissions_mainnet_latest.json
var WasmPermissionsRawV138 []byte
