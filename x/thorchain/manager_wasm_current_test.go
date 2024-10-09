package thorchain

import (
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	. "gopkg.in/check.v1"
)

type WasmManagerVCURSuite struct{}

var _ = Suite(&WasmManagerVCURSuite{})

func (s WasmManagerVCURSuite) TestStoreCode(c *C) {
	ctx, mgr := setupManagerForTest(c)
	addr := GetRandomBech32Addr()
	msg := wasmtypes.MsgStoreCode{
		Sender:                addr.String(),
		WASMByteCode:          []byte{},
		InstantiatePermission: nil,
	}
	_, err := mgr.WasmManager().StoreCode(ctx, &msg)
	c.Assert(err, NotNil)
}
