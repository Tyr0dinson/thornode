package wasmgas

import (
	"fmt"
	"math"

	storetypes "cosmossdk.io/store/types"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// maxWasmSDKGas is the largest SDK gas value that wasmd can convert to
// CosmWasm VM gas without overflowing uint64.
const maxWasmSDKGas = math.MaxUint64 / wasmtypes.DefaultGasMultiplier

// Meter delegates gas accounting to its parent and can be activated once to
// enforce Wasm transaction and block limits. Keeping the same meter for the
// whole transaction makes nested and internally-created Wasm calls share one
// budget instead of resetting the allowance for every call.
type Meter struct {
	parent storetypes.GasMeter
	limit  storetypes.Gas
	active bool
}

// NewMeter returns an inactive Wasm meter backed by parent.
func NewMeter(parent storetypes.GasMeter) *Meter {
	return &Meter{
		parent: parent,
		limit:  math.MaxUint64,
	}
}

// NewInfiniteMeter returns an inactive Wasm meter with an infinite parent.
func NewInfiniteMeter() *Meter {
	return NewMeter(storetypes.NewInfiniteGasMeter())
}

// Activate enables the configured limits on the context's meter. Repeated
// calls are intentionally no-ops so nested executions cannot reset the budget.
// The block limit is measured against the shared SDK block meter; therefore it
// also conservatively includes gas consumed by non-Wasm transactions.
func Activate(ctx sdk.Context, maxGasPerTx, maxGasPerBlock int64) sdk.Context {
	if maxGasPerTx <= 0 && maxGasPerBlock <= 0 {
		return ctx
	}

	meter, ok := ctx.GasMeter().(*Meter)
	if !ok {
		meter = NewMeter(ctx.GasMeter())
		ctx = ctx.WithGasMeter(meter)
	}

	var blockGasConsumed storetypes.Gas
	if blockMeter := ctx.BlockGasMeter(); blockMeter != nil {
		blockGasConsumed = blockMeter.GasConsumed()
	}
	meter.activate(maxGasPerTx, maxGasPerBlock, blockGasConsumed)
	return ctx
}

func (m *Meter) activate(maxGasPerTx, maxGasPerBlock int64, blockGasConsumed storetypes.Gas) {
	if m.active {
		return
	}
	m.active = true
	m.limit = m.parent.Limit()

	if maxGasPerTx > 0 {
		txLimit := saturatingAdd(m.parent.GasConsumed(), safeWasmGas(maxGasPerTx))
		m.limit = min(m.limit, txLimit)
	}

	if maxGasPerBlock > 0 {
		blockLimit := storetypes.Gas(0)
		blockMax := safeWasmGas(maxGasPerBlock)
		if blockGasConsumed < blockMax {
			blockLimit = blockMax - blockGasConsumed
		}
		m.limit = min(m.limit, blockLimit)
	}
}

// safeWasmGas clamps a positive Mimir value before wasmd multiplies the SDK
// allowance by DefaultGasMultiplier. Without this bound an oversized runtime
// configuration deterministically panics every attempted Wasm execution.
func safeWasmGas(configured int64) storetypes.Gas {
	return min(storetypes.Gas(configured), maxWasmSDKGas) // #nosec G115 -- callers guard configured > 0
}

func saturatingAdd(a, b storetypes.Gas) storetypes.Gas {
	if math.MaxUint64-a < b {
		return math.MaxUint64
	}
	return a + b
}

func (m *Meter) GasConsumed() storetypes.Gas {
	return m.parent.GasConsumed()
}

func (m *Meter) GasConsumedToLimit() storetypes.Gas {
	consumed := m.parent.GasConsumedToLimit()
	if m.active && consumed > m.limit {
		return m.limit
	}
	return consumed
}

func (m *Meter) GasRemaining() storetypes.Gas {
	remaining := m.parent.GasRemaining()
	if !m.active {
		return remaining
	}

	consumed := m.parent.GasConsumed()
	if consumed >= m.limit {
		return 0
	}
	return min(remaining, m.limit-consumed)
}

func (m *Meter) Limit() storetypes.Gas {
	if !m.active {
		return m.parent.Limit()
	}
	return min(m.parent.Limit(), m.limit)
}

func (m *Meter) ConsumeGas(amount storetypes.Gas, descriptor string) {
	m.parent.ConsumeGas(amount, descriptor)
	if m.active && m.parent.GasConsumed() > m.limit {
		panic(storetypes.ErrorOutOfGas{Descriptor: descriptor})
	}
}

func (m *Meter) RefundGas(amount storetypes.Gas, descriptor string) {
	m.parent.RefundGas(amount, descriptor)
}

func (m *Meter) IsPastLimit() bool {
	return m.parent.IsPastLimit() || m.active && m.parent.GasConsumed() > m.limit
}

func (m *Meter) IsOutOfGas() bool {
	return m.parent.IsOutOfGas() || m.active && m.parent.GasConsumed() >= m.limit
}

func (m *Meter) String() string {
	return fmt.Sprintf("WasmGasMeter{active: %t, limit: %d, consumed: %d}", m.active, m.Limit(), m.GasConsumed())
}
