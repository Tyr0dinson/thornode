package thorchain

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CosmWasm/wasmd/x/wasm/ioutils"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type CovCCWasmManagerSuite struct{}

var _ = Suite(&CovCCWasmManagerSuite{})

func covCCLoadWasmRaw(c *C, file string) []byte {
	raw, err := os.ReadFile(filepath.Join("../../test/fixtures/wasm", file))
	c.Assert(err, IsNil)
	return raw
}

func covCCLoadWasm(c *C, file string) []byte {
	wasm, err := ioutils.GzipIt(covCCLoadWasmRaw(c, file))
	c.Assert(err, IsNil)
	return wasm
}

func covCCChecksumKey(checksum []byte) string {
	return strings.TrimRight(base32.StdEncoding.EncodeToString(checksum), "=")
}

func covCCWasmSetup(c *C) (cosmos.Context, *Mgrs) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockTime(time.Unix(1, 0))
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyWasmPermissionless, 1)
	return ctx, mgr
}

// covCCInstantiate stores the given wasm file and instantiates one contract.
func covCCInstantiate(c *C, ctx cosmos.Context, mgr *Mgrs, file string, admin cosmos.AccAddress) (uint64, cosmos.AccAddress, []byte) {
	codeID, checksum, err := mgr.WasmManager().StoreCode(ctx, GetRandomBech32Addr(), covCCLoadWasm(c, file))
	c.Assert(err, IsNil)
	addr, _, err := mgr.WasmManager().InstantiateContract(ctx, codeID, admin, admin, []byte(`{}`), "label", sdk.Coins{})
	c.Assert(err, IsNil)
	return codeID, addr, checksum
}

func (s *CovCCWasmManagerSuite) TestStoreCodeErrors(c *C) {
	ctx, mgr := setupManagerForTest(c)
	wm := mgr.WasmManager()
	creator := GetRandomBech32Addr()
	wasm := covCCLoadWasm(c, "simple.wasm")

	// not permissionless + random actor -> unauthorized by permissions
	_, _, err := wm.StoreCode(ctx, creator, wasm)
	c.Assert(err, NotNil)

	// permissionless, but the deployer is halted
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyWasmPermissionless, 1)
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf(constants.MimirTemplateWasmHaltDeployer, creator.String()), 1)
	_, _, err = wm.StoreCode(ctx, creator, wasm)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*unauthorized.*")

	// checksum halted -> created then rejected
	sum := sha256.Sum256(covCCLoadWasmRaw(c, "simple.wasm"))
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf(constants.MimirTemplateWasmHaltChecksum, covCCChecksumKey(sum[:])), 1)
	_, _, err = wm.StoreCode(ctx, GetRandomBech32Addr(), wasm)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*checksum halted.*")

	// invalid wasm bytes
	_, _, err = wm.StoreCode(ctx, GetRandomBech32Addr(), []byte("not wasm"))
	c.Assert(err, NotNil)

	// global halt
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyWasmHaltGlobal, 1)
	_, _, err = wm.StoreCode(ctx, GetRandomBech32Addr(), covCCLoadWasm(c, "counter.wasm"))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*wasm halted.*")
}

func (s *CovCCWasmManagerSuite) TestGlobalHaltAllEntrypoints(c *C) {
	ctx, mgr := covCCWasmSetup(c)
	wm := mgr.WasmManager()
	admin := GetRandomBech32Addr()
	codeID, addr, _ := covCCInstantiate(c, ctx, mgr, "counter.wasm", admin)

	// halt height in the future is not active yet
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyWasmHaltGlobal, ctx.BlockHeight()+10)
	_, err := wm.ExecuteContract(ctx, addr, admin, []byte(`{"increment":{}}`), sdk.Coins{})
	c.Assert(err, IsNil)

	mgr.Keeper().SetMimir(ctx, constants.MimirKeyWasmHaltGlobal, 1)
	checkHalt := func(err error) {
		c.Assert(err, NotNil)
		c.Assert(err.Error(), Matches, ".*wasm halted.*")
	}
	_, _, err = wm.InstantiateContract(ctx, codeID, admin, admin, []byte(`{}`), "l", sdk.Coins{})
	checkHalt(err)
	_, _, err = wm.InstantiateContract2(ctx, codeID, admin, admin, []byte(`{}`), "l", sdk.Coins{}, []byte("salt"), false)
	checkHalt(err)
	_, err = wm.ExecuteContract(ctx, addr, admin, []byte(`{"increment":{}}`), sdk.Coins{})
	checkHalt(err)
	_, err = wm.MigrateContract(ctx, addr, admin, codeID, []byte(`{}`))
	checkHalt(err)
	_, err = wm.SudoContract(ctx, addr, admin, []byte(`{}`))
	checkHalt(err)
	_, err = wm.UpdateAdmin(ctx, addr, admin, GetRandomBech32Addr())
	checkHalt(err)
	_, err = wm.ClearAdmin(ctx, addr, admin)
	checkHalt(err)
}

func (s *CovCCWasmManagerSuite) TestInstantiateAuthorization(c *C) {
	ctx, mgr := covCCWasmSetup(c)
	wm := mgr.WasmManager()
	admin := GetRandomBech32Addr()
	codeID, contractAddr, _ := covCCInstantiate(c, ctx, mgr, "counter.wasm", admin)

	// drop permissionless: random actor can no longer instantiate
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyWasmPermissionless, 0)
	random := GetRandomBech32Addr()
	_, _, err := wm.InstantiateContract(ctx, codeID, random, random, []byte(`{}`), "l", sdk.Coins{})
	c.Assert(err, NotNil)
	_, _, err = wm.InstantiateContract2(ctx, codeID, random, random, []byte(`{}`), "l", sdk.Coins{}, []byte("salt"), false)
	c.Assert(err, NotNil)

	// a contract (has history) can instantiate without explicit permission
	addr2, _, err := wm.InstantiateContract(ctx, codeID, contractAddr, nil, []byte(`{}`), "l2", sdk.Coins{})
	c.Assert(err, IsNil)
	c.Assert(wm.HasContract(ctx, addr2), Equals, true)

	// halted deployer cannot instantiate even with permissionless
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyWasmPermissionless, 1)
	halted := GetRandomBech32Addr()
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf(constants.MimirTemplateWasmHaltDeployer, halted.String()), 1)
	_, _, err = wm.InstantiateContract(ctx, codeID, halted, halted, []byte(`{}`), "l", sdk.Coins{})
	c.Assert(err, NotNil)

	// unknown code id fails while pinning
	_, _, err = wm.InstantiateContract(ctx, 999, admin, admin, []byte(`{}`), "l", sdk.Coins{})
	c.Assert(err, NotNil)
	_, _, err = wm.InstantiateContract2(ctx, 999, admin, admin, []byte(`{}`), "l", sdk.Coins{}, []byte("salt"), false)
	c.Assert(err, NotNil)

	// HasContract on a non-contract
	c.Assert(wm.HasContract(ctx, GetRandomBech32Addr()), Equals, false)
}

func (s *CovCCWasmManagerSuite) TestInstantiateContract2(c *C) {
	ctx, mgr := covCCWasmSetup(c)
	wm := mgr.WasmManager()
	admin := GetRandomBech32Addr()
	codeID, _, err := wm.StoreCode(ctx, GetRandomBech32Addr(), covCCLoadWasm(c, "counter.wasm"))
	c.Assert(err, IsNil)

	addr, _, err := wm.InstantiateContract2(ctx, codeID, admin, admin, []byte(`{}`), "l", sdk.Coins{}, []byte("salt"), false)
	c.Assert(err, IsNil)
	c.Assert(wm.HasContract(ctx, addr), Equals, true)

	// same salt + creator collides
	_, _, err = wm.InstantiateContract2(ctx, codeID, admin, admin, []byte(`{}`), "l", sdk.Coins{}, []byte("salt"), false)
	c.Assert(err, NotNil)
}

func (s *CovCCWasmManagerSuite) TestExecuteContract(c *C) {
	ctx, mgr := covCCWasmSetup(c)
	wm := mgr.WasmManager()
	admin := GetRandomBech32Addr()
	_, addr, checksum := covCCInstantiate(c, ctx, mgr, "counter.wasm", admin)

	_, err := wm.ExecuteContract(ctx, addr, admin, []byte(`{"increment":{}}`), sdk.Coins{})
	c.Assert(err, IsNil)

	// unknown contract
	_, err = wm.ExecuteContract(ctx, GetRandomBech32Addr(), admin, []byte(`{}`), sdk.Coins{})
	c.Assert(err, NotNil)
	c.Assert(wasmtypes.ErrNotFound.Is(err), Equals, true)

	// contract halt
	addrStr := addr.String()
	contractKey := fmt.Sprintf(constants.MimirTemplateWasmHaltContract, addrStr[len(addrStr)-6:])
	mgr.Keeper().SetMimir(ctx, contractKey, 1)
	_, err = wm.ExecuteContract(ctx, addr, admin, []byte(`{"increment":{}}`), sdk.Coins{})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*contract halted.*")
	mgr.Keeper().SetMimir(ctx, contractKey, 0)

	// checksum halt
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf(constants.MimirTemplateWasmHaltChecksum, covCCChecksumKey(checksum)), 1)
	_, err = wm.ExecuteContract(ctx, addr, admin, []byte(`{"increment":{}}`), sdk.Coins{})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*checksum halted.*")
}

func (s *CovCCWasmManagerSuite) TestMigrateContractErrors(c *C) {
	ctx, mgr := covCCWasmSetup(c)
	wm := mgr.WasmManager()
	admin := GetRandomBech32Addr()
	codeID, addr, _ := covCCInstantiate(c, ctx, mgr, "simple.wasm", admin)

	// unknown contract
	_, err := wm.MigrateContract(ctx, GetRandomBech32Addr(), admin, codeID, []byte(`{}`))
	c.Assert(err, NotNil)

	// non admin
	_, err = wm.MigrateContract(ctx, addr, GetRandomBech32Addr(), codeID, []byte(`{}`))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*unauthorized.*")

	// admin with halted deployer
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf(constants.MimirTemplateWasmHaltDeployer, admin.String()), 1)
	_, err = wm.MigrateContract(ctx, addr, admin, codeID, []byte(`{}`))
	c.Assert(err, NotNil)
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf(constants.MimirTemplateWasmHaltDeployer, admin.String()), 0)

	// unknown target code -> pin fails
	_, err = wm.MigrateContract(ctx, addr, admin, 999, []byte(`{}`))
	c.Assert(err, NotNil)
}

func (s *CovCCWasmManagerSuite) TestSudoAndAdmin(c *C) {
	ctx, mgr := covCCWasmSetup(c)
	wm := mgr.WasmManager()
	admin := GetRandomBech32Addr()
	_, addr, _ := covCCInstantiate(c, ctx, mgr, "extended.wasm", admin)

	// sudo by non admin
	_, err := wm.SudoContract(ctx, addr, GetRandomBech32Addr(), []byte(`{}`))
	c.Assert(err, NotNil)
	// sudo on unknown contract
	_, err = wm.SudoContract(ctx, GetRandomBech32Addr(), admin, []byte(`{}`))
	c.Assert(err, NotNil)
	// sudo by admin
	_, err = wm.SudoContract(ctx, addr, admin, []byte(`{}`))
	c.Assert(err, IsNil)

	// update admin by non admin / unknown contract
	newAdmin := GetRandomBech32Addr()
	_, err = wm.UpdateAdmin(ctx, addr, GetRandomBech32Addr(), newAdmin)
	c.Assert(err, NotNil)
	_, err = wm.UpdateAdmin(ctx, GetRandomBech32Addr(), admin, newAdmin)
	c.Assert(err, NotNil)
	_, err = wm.UpdateAdmin(ctx, addr, admin, newAdmin)
	c.Assert(err, IsNil)
	info := mgr.wasmKeeper.GetContractInfo(ctx, addr)
	c.Assert(info.Admin, Equals, newAdmin.String())

	// clear admin: old admin no longer authorized
	_, err = wm.ClearAdmin(ctx, addr, admin)
	c.Assert(err, NotNil)
	_, err = wm.ClearAdmin(ctx, GetRandomBech32Addr(), newAdmin)
	c.Assert(err, NotNil)
	_, err = wm.ClearAdmin(ctx, addr, newAdmin)
	c.Assert(err, IsNil)
	info = mgr.wasmKeeper.GetContractInfo(ctx, addr)
	c.Assert(info.Admin, Equals, "")
}
