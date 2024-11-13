package thorchain

import (
	"os"
	"path/filepath"
	"time"

	"github.com/CosmWasm/wasmd/x/wasm/ioutils"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"
)

type WasmManagerVCURSuite struct{}

var _ = Suite(&WasmManagerVCURSuite{})

func (s WasmManagerVCURSuite) TestStoreCode(c *C) {
	ctx, mgr := setupManagerForTest(c)
	wasm := s.loadWasm(c, "simple.wasm")

	msg := wasmtypes.MsgStoreCode{
		Sender:                "tthor1zf3gsk7edzwl9syyefvfhle37cjtql35h6k85m",
		WASMByteCode:          wasm,
		InstantiatePermission: nil,
	}
	_, err := mgr.WasmManager().StoreCode(ctx, &msg)
	c.Assert(err, IsNil)
}

func (s WasmManagerVCURSuite) TestMigrateCode(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockTime(time.Unix(1, 0))
	wasm := s.loadWasm(c, "simple.wasm")

	msg := wasmtypes.MsgStoreCode{
		Sender:                "tthor1zf3gsk7edzwl9syyefvfhle37cjtql35h6k85m",
		WASMByteCode:          wasm,
		InstantiatePermission: nil,
	}
	_, err := mgr.WasmManager().StoreCode(ctx, &msg)
	c.Assert(err, IsNil)

	wasm = s.loadWasm(c, "extended.wasm")

	msg = wasmtypes.MsgStoreCode{
		Sender:                "tthor1zf3gsk7edzwl9syyefvfhle37cjtql35h6k85m",
		WASMByteCode:          wasm,
		InstantiatePermission: nil,
	}
	_, err = mgr.WasmManager().StoreCode(ctx, &msg)
	c.Assert(err, IsNil)

	initMsg := wasmtypes.MsgInstantiateContract{
		Sender: "tthor1zf3gsk7edzwl9syyefvfhle37cjtql35h6k85m",
		Admin:  "tthor1zf3gsk7edzwl9syyefvfhle37cjtql35h6k85m",
		CodeID: 1,
		Label:  "label",
		Msg:    []byte(`{}`),
		Funds:  []types.Coin{},
	}
	_, err = mgr.WasmManager().InstantiateContract(ctx, &initMsg)
	c.Assert(err, IsNil)

	migrateMsg := wasmtypes.MsgMigrateContract{
		Sender:   "tthor1zf3gsk7edzwl9syyefvfhle37cjtql35h6k85m",
		Contract: "tthor14hj2tavq8fpesdwxxcu44rty3hh90vhujrvcmstl4zr3txmfvw9sw58u9f",
		CodeID:   2,
		Msg:      []byte(`{}`),
	}
	_, err = mgr.WasmManager().MigrateContract(ctx, &migrateMsg)
	c.Assert(err, IsNil)
}

func (s WasmManagerVCURSuite) loadWasm(c *C, file string) []byte {
	wasmPath := filepath.Join("../../test/fixtures/wasm", file)
	wasm, err := os.ReadFile(wasmPath)
	c.Assert(err, IsNil)
	wasm, err = ioutils.GzipIt(wasm)
	c.Assert(err, IsNil)
	return wasm
}
