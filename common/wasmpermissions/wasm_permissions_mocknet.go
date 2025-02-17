//go:build mocknet
// +build mocknet

package wasmpermissions

import (
	_ "embed"
)

var WasmPermissionsRaw = WasmPermissions{}
