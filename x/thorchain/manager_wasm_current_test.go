package thorchain

import (
	"os"
	"path/filepath"

	"github.com/CosmWasm/wasmd/x/wasm/ioutils"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	. "gopkg.in/check.v1"
)

type WasmManagerVCURSuite struct{}

var _ = Suite(&WasmManagerVCURSuite{})

func (s WasmManagerVCURSuite) TestStoreCode(c *C) {
	ctx, mgr := setupManagerForTest(c)
	wasmPath := filepath.Join("../../test/fixtures/wasm", "simple.wasm")
	wasm, err := os.ReadFile(wasmPath)
	c.Assert(err, IsNil)
	wasm, err = ioutils.GzipIt(wasm)
	c.Assert(err, IsNil)

	msg := wasmtypes.MsgStoreCode{
		Sender:                "tthor1zf3gsk7edzwl9syyefvfhle37cjtql35h6k85m",
		WASMByteCode:          wasm,
		InstantiatePermission: nil,
	}
	_, err = mgr.WasmManager().StoreCode(ctx, &msg)
	c.Assert(err, IsNil)
}
