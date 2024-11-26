package wasmpermissions

import (
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/blang/semver"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

var wasmPermissionsV138 WasmPermissions

type WasmPermissions struct {
	Permissions map[string]WasmPermission
}

type WasmPermission struct {
	Origin    string          `json:"origin"`
	Deployers map[string]bool `json:"deployers"`
}

func (w WasmPermissions) Permit(actor sdk.AccAddress, checksum []byte) error {
	hexChecksum := hex.EncodeToString(checksum)
	if permission, exists := w.Permissions[hexChecksum]; exists && permission.Deployers[actor.String()] {
		return nil
	}
	return errors.New("unauthorized")
}

func init() {
	if err := json.Unmarshal(WasmPermissionsRawV138, &wasmPermissionsV138); err != nil {
		panic(err)
	}
}

func GetWasmPermissions(version semver.Version) WasmPermissions {
	switch {
	case version.GTE(semver.MustParse("2.138.0")):
		return wasmPermissionsV138
	default:
		return WasmPermissions{}
	}
}
