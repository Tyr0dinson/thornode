package features

import (
	"fmt"
	rand "math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const (
	wasmGasFixturePath = "../../test/fixtures/wasm/counter.wasm"
	wasmGasPerTx       = int64(1_000)
	wasmGasPerBlock    = int64(50_000)
)

// WasmGasLimits exercises the gas caps against a real VM. The actor is
// optional because it temporarily changes economic Mimirs and intentionally
// submits a failing contract execution.
func WasmGasLimits(rng *rand.Rand) *Actor {
	a := &wasmGasLimitsActor{
		Actor: *NewActor("Feature-WasmGasLimits", rng),
		rng:   rng,
	}
	a.Timeout = 5 * time.Minute
	a.Ops = append(a.Ops,
		a.acquireUser,
		a.captureMimirs,
		a.enablePermissionless,
		a.waitPermissionless,
		a.storeCode,
		a.waitStoreCode,
		a.instantiate,
		a.waitInstantiate,
		a.enableGasLimits,
		a.waitGasLimits,
		a.executeCapped,
		a.waitCappedExecution,
		a.restoreMimirs,
		a.waitRestoredMimirs,
		a.releaseUser,
	)
	return &a.Actor
}

type wasmGasLimitsActor struct {
	Actor

	rng       *rand.Rand
	user      *User
	original  map[string]int64
	codeID    uint64
	contract  string
	storeTx   common.TxID
	instTx    common.TxID
	executeTx common.TxID
}

type cosmosTxResponse struct {
	TxResponse struct {
		Code   uint32        `json:"code"`
		RawLog string        `json:"raw_log"`
		Events []cosmosEvent `json:"events"`
	} `json:"tx_response"`
}

type cosmosEvent struct {
	Type       string            `json:"type"`
	Attributes []cosmosAttribute `json:"attributes"`
}

type cosmosAttribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (a *wasmGasLimitsActor) acquireUser(config *OpConfig) OpResult {
	for _, user := range config.Users {
		if user.Acquire() {
			a.user = user
			a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())
			a.Log().Info().Msg("starting WASM gas-limit checks with a real contract")
			return OpResult{Continue: true}
		}
	}
	return OpResult{}
}

func (a *wasmGasLimitsActor) captureMimirs(_ *OpConfig) OpResult {
	mimirs, err := thornode.GetMimirs()
	if err != nil {
		return OpResult{Error: err}
	}
	a.original = map[string]int64{}
	for _, key := range []string{"WASMPERMISSIONLESS", constants.WasmMaxGasPerTx.String(), constants.WasmMaxGasPerBlock.String()} {
		a.original[key] = mimirs[strings.ToUpper(key)]
	}
	a.Log().Info().Interface("mimirs", a.original).Msg("captured WASM Mimirs for restoration after the test")
	return OpResult{Continue: true}
}

func (a *wasmGasLimitsActor) enablePermissionless(config *OpConfig) OpResult {
	return a.broadcastMimirs(config, map[string]int64{"WASMPERMISSIONLESS": 1})
}

func (a *wasmGasLimitsActor) waitPermissionless(_ *OpConfig) OpResult {
	result := waitForMimirs(map[string]int64{"WASMPERMISSIONLESS": 1})
	if result.Continue {
		a.Log().Info().Msg("permissionless WASM enabled; deploying the counter contract")
	}
	return result
}

func (a *wasmGasLimitsActor) storeCode(_ *OpConfig) OpResult {
	code, err := os.ReadFile(wasmGasFixturePath)
	if err != nil {
		return OpResult{Finish: true, Error: err}
	}
	sender := a.user.Address(common.THORChain).String()
	a.storeTx, err = a.user.Thorchain.Broadcast(&wasmtypes.MsgStoreCode{Sender: sender, WASMByteCode: code})
	return OpResult{Continue: err == nil, Error: err}
}

func (a *wasmGasLimitsActor) waitStoreCode(_ *OpConfig) OpResult {
	tx, err := getCosmosTx(a.storeTx)
	if err != nil {
		return OpResult{Error: err}
	}
	if tx.TxResponse.Code != 0 {
		return OpResult{Finish: true, Error: fmt.Errorf("store code failed: %s", tx.TxResponse.RawLog)}
	}
	value := txEventAttribute(tx, "store_code", "code_id")
	a.codeID, err = strconv.ParseUint(value, 10, 64)
	if err == nil {
		a.Log().Info().Uint64("code_id", a.codeID).Stringer("txid", a.storeTx).Msg("stored WASM counter code")
	}
	return OpResult{Continue: err == nil, Error: err}
}

func (a *wasmGasLimitsActor) instantiate(_ *OpConfig) OpResult {
	sender := a.user.Address(common.THORChain).String()
	var err error
	a.instTx, err = a.user.Thorchain.Broadcast(&wasmtypes.MsgInstantiateContract{
		Sender: sender,
		CodeID: a.codeID,
		Label:  "simulation-wasm-gas-counter",
		Msg:    wasmtypes.RawContractMessage(`{}`),
		Funds:  sdk.Coins{},
	})
	return OpResult{Continue: err == nil, Error: err}
}

func (a *wasmGasLimitsActor) waitInstantiate(_ *OpConfig) OpResult {
	tx, err := getCosmosTx(a.instTx)
	if err != nil {
		return OpResult{Error: err}
	}
	if tx.TxResponse.Code != 0 {
		return OpResult{Finish: true, Error: fmt.Errorf("instantiate failed: %s", tx.TxResponse.RawLog)}
	}
	a.contract = txEventAttribute(tx, "instantiate", "_contract_address")
	if a.contract == "" {
		return OpResult{Finish: true, Error: fmt.Errorf("instantiate response has no contract address")}
	}
	a.Log().Info().Str("contract", a.contract).Stringer("txid", a.instTx).Msg("instantiated WASM counter contract")
	return OpResult{Continue: true}
}

func (a *wasmGasLimitsActor) enableGasLimits(config *OpConfig) OpResult {
	return a.broadcastMimirs(config, map[string]int64{
		constants.WasmMaxGasPerTx.String():    wasmGasPerTx,
		constants.WasmMaxGasPerBlock.String(): wasmGasPerBlock,
	})
}

func (a *wasmGasLimitsActor) waitGasLimits(_ *OpConfig) OpResult {
	result := waitForMimirs(map[string]int64{
		constants.WasmMaxGasPerTx.String():    wasmGasPerTx,
		constants.WasmMaxGasPerBlock.String(): wasmGasPerBlock,
	})
	if result.Continue {
		a.Log().Info().
			Int64("per_tx", wasmGasPerTx).
			Int64("per_block", wasmGasPerBlock).
			Msg("WASM gas caps active; checking that direct execution fails with out of gas")
	}
	return result
}

func (a *wasmGasLimitsActor) executeCapped(_ *OpConfig) OpResult {
	sender := a.user.Address(common.THORChain).String()
	var err error
	a.executeTx, err = a.user.Thorchain.Broadcast(&wasmtypes.MsgExecuteContract{
		Sender:   sender,
		Contract: a.contract,
		Msg:      wasmtypes.RawContractMessage(`{"increment":{}}`),
		Funds:    sdk.Coins{},
	})
	return OpResult{Continue: err == nil, Error: err}
}

func (a *wasmGasLimitsActor) waitCappedExecution(_ *OpConfig) OpResult {
	tx, err := getCosmosTx(a.executeTx)
	if err != nil {
		return OpResult{Error: err}
	}
	if tx.TxResponse.Code == 0 || !strings.Contains(strings.ToLower(tx.TxResponse.RawLog), "out of gas") {
		return OpResult{Finish: true, Error: fmt.Errorf("expected wasm out of gas, got code %d: %s", tx.TxResponse.Code, tx.TxResponse.RawLog)}
	}
	a.Log().Info().Uint32("code", tx.TxResponse.Code).Stringer("txid", a.executeTx).
		Msg("direct WASM execution was capped with the expected out-of-gas error")
	return OpResult{Continue: true}
}

func (a *wasmGasLimitsActor) restoreMimirs(config *OpConfig) OpResult {
	return a.broadcastMimirs(config, a.original)
}

func (a *wasmGasLimitsActor) waitRestoredMimirs(_ *OpConfig) OpResult {
	result := waitForMimirs(a.original)
	if result.Continue {
		a.Log().Info().Msg("restored the original WASM Mimirs")
	}
	return result
}

func (a *wasmGasLimitsActor) releaseUser(_ *OpConfig) OpResult {
	a.Log().Info().
		Uint64("code_id", a.codeID).
		Str("contract", a.contract).
		Msg("WASM gas-limit simulation checks succeeded")
	a.user.Release()
	return OpResult{Finish: true}
}

func (a *wasmGasLimitsActor) broadcastMimirs(config *OpConfig, values map[string]int64) OpResult {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := values[key]
		actor := NewMultiNodeMimirActor(key, value, a.rng)
		actor.InitRoot()
		if err := actor.Execute(config); err != nil {
			return OpResult{Error: err}
		}
	}
	return OpResult{Continue: true}
}

func waitForMimirs(expected map[string]int64) OpResult {
	mimirs, err := thornode.GetMimirs()
	if err != nil {
		return OpResult{Error: err}
	}
	for key, value := range expected {
		if mimirs[strings.ToUpper(key)] != value {
			return OpResult{}
		}
	}
	return OpResult{Continue: true}
}

func getCosmosTx(txID common.TxID) (cosmosTxResponse, error) {
	var response cosmosTxResponse
	err := thornode.Get(fmt.Sprintf("%s/cosmos/tx/v1beta1/txs/%s", thornode.BaseURL(), txID), &response)
	return response, err
}

func txEventAttribute(tx cosmosTxResponse, eventType, key string) string {
	for _, event := range tx.TxResponse.Events {
		if event.Type != eventType {
			continue
		}
		for _, attribute := range event.Attributes {
			if attribute.Key == key {
				return attribute.Value
			}
		}
	}
	return ""
}
