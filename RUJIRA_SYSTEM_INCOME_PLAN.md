# Plan: Add `app_layer` Module to System Income

## Context

THORChain's per-block reward calculation (`UpdateNetwork` → `calcBlockRewards`)
currently treats system income as two sources:

```
systemIncome = blockReward + totalLiquidityFees − totalRevShare
```

This plan adds a **third source**: a dedicated, unblocked cosmos module account
`app_layer`. External "app layer" sources deposit RUNE into it; the protocol
drains its full balance every block and folds it into system income:

```
systemIncome = blockReward + totalLiquidityFees + appLayerFees − totalRevShare
```

The drained RUNE is then distributed through the **same Incentive Pendulum split**
as all other income (LPs, bonders, dev fund, burn, TCY, marketing, POL).

> **First consumer: Rujira.** Rujira's Revenue Collector
> (`thor1txum04wp8ykqudphxy9prtwsd9jpcm2kwdaxctxeeyr6g0r0we9qpfdktr`) is the
> motivating use case — it sends RUNE to the `app_layer` module so its revenue is
> counted as explicit, indexable system income instead of being inferred from
> Reserve transfers. `app_layer` is intentionally generic so other sources can use
> the same path.

---

## Approach

Mirror exactly how `totalLiquidityFees` (swap fees) is already handled: **read a
module balance in `UpdateNetwork`, drain it to Reserve, then pass it as a separate
parameter to `calcBlockRewards`.** No custom KV accumulator is needed — **the
module account IS the accumulator**, and draining it keeps fund balances and
accounting consistent automatically (you can only distribute RUNE that is present).

Reserve is the rail that funds all the existing split transfers (dev fund, burn,
TCY, marketing, POL, pool/bond are all `SendFromModule(ReserveName, …)`); draining
`app_layer` into Reserve lets that machinery distribute the funds unchanged, in the
same block.

### Two ordering constraints (both must hold)

1. **Read `totalReserve` BEFORE the drain.** `blockReward = totalReserve /
   emissionCurve / blocksPerYear`. If `app_layer` RUNE were already in Reserve when
   `totalReserve` is snapshotted (line 1840), it would inflate the emission-curve
   block reward. The drain must happen after that read.

2. **Drain only AFTER the early-return guards — on the distribution path.**
   `UpdateNetwork` early-returns at `if totalReserve.IsZero()` (line 1843) and
   `if availablePoolsRune.IsZero()` (line 1856). If the drain runs *before* those
   guards, a guarded block moves the funds into Reserve and then returns **without
   distributing** — stranding the RUNE in Reserve (subject to the emission curve)
   and losing it from `app_layer` accounting (the module is now empty). Place the
   drain **after line 1856** (e.g. alongside the `totalLiquidityFees` read, ~1862).
   On a guarded block the funds then stay in `app_layer` and roll forward to the
   next healthy block. No double-count, no stranding.

The safe window for the drain is therefore **between line 1856 and the
`calcBlockRewards` call (~1892)**.

### Access model

The `app_layer` module is **unblocked** (like `TreasuryName` / `LendingName`), so
any account can `MsgSend` RUNE to it directly — no memo or custom handler. Allowing
any sender is harmless: contributing RUNE only adds to system income that is then
distributed; it is not an attack vector. (If a future requirement needs to restrict
contributions to a specific collector, gate it with a dedicated memo + handler —
out of scope for this plan.)

---

## Files to change

### 1. `x/thorchain/types/keys.go`
Add after `POLReserveName`:
```go
// AppLayerName the name of the account used to receive app layer income
AppLayerName = "app_layer"
```

### 2. `x/thorchain/alias.go`
Add to the `const` block:
```go
AppLayerName = types.AppLayerName
```

### 3. `x/thorchain/keeper/v1/alias.go`
Add (alongside `ReserveName`, `POLReserveName`, …):
```go
AppLayerName = types.AppLayerName
```

### 4. `app/app.go`
- In `maccPerms` (line ~117), add empty perms (no Minter/Burner, like Reserve):
  ```go
  thorchain.AppLayerName: {},
  ```
- In `BlockedAddresses()` (line ~935, next to the `LendingName` / `TreasuryName`
  deletes at 942–943), unblock the module so it can receive external RUNE:
  ```go
  delete(modAccAddrs, authtypes.NewModuleAddress(thorchaintypes.AppLayerName).String())
  ```
- `app/app_test.go` asserts `maccPerms` matches — update accordingly.

### 5. `x/thorchain/genesis.go` (~line 382)
Log the new module address at init (consistent with other modules):
```go
appLayerAddr, _ := keeper.GetModuleAddress(AppLayerName)
ctx.Logger().Info("AppLayer Module", "address", appLayerAddr.String())
```

### 6. `x/thorchain/manager_network_current.go` — `UpdateNetwork`
**Drain the module after the guards** (after line 1856, before `calcBlockRewards`;
NOT before the guards — see ordering constraint #2):
```go
// Drain app layer fees into Reserve each block (after the totalReserve snapshot
// and after the early-return guards, so guarded blocks roll the funds forward
// instead of stranding them in Reserve).
appLayerFees := vm.k.GetRuneBalanceOfModule(ctx, AppLayerName)
if !appLayerFees.IsZero() {
    coin := common.NewCoin(common.RuneNative, appLayerFees)
    if err = vm.k.SendFromModuleToModule(ctx, AppLayerName, ReserveName, common.NewCoins(coin)); err != nil {
        return fmt.Errorf("fail to transfer app layer fees to reserve: %w", err)
    }
}
```
Pass `appLayerFees` to `calcBlockRewards` (new param after `totalLiquidityFees`),
and to the `NewEventRewards` call (see #9).

### 7. `x/thorchain/manager_network_current.go` — `calcBlockRewards` (~line 2137)
- Add `appLayerFees cosmos.Uint` parameter (after `totalLiquidityFees`).
- Change the income line (line 2176):
  ```go
  systemIncome := blockReward.Add(totalLiquidityFees).Add(appLayerFees)
  systemIncome = common.SafeSub(systemIncome, totalRevShare)
  ```
- Add `"app_layer_fees", appLayerFees,` to the existing
  `ctx.Logger().Info("incentive pendulum", …)` attributes (~line 2251).
- Thread the **gross** system income back for the event (#11): either return
  `blockReward` (and let `UpdateNetwork` recompute
  `gross = blockReward + totalLiquidityFees + appLayerFees − totalRevShare`), or
  return the gross value directly as an added return value.

### 8. `proto/thorchain/v1/types/type_events.proto`
Add field 8 to `EventRewards`:
```proto
string app_layer_fees = 8 [
  (gogoproto.customtype) = "cosmossdk.io/math.Uint",
  (gogoproto.nullable) = false
];
```
Add a new `EventSystemIncome` message:
```proto
message EventSystemIncome {
  string block_reward    = 1 [(gogoproto.customtype) = "cosmossdk.io/math.Uint", (gogoproto.nullable) = false];
  string liquidity_fees  = 2 [(gogoproto.customtype) = "cosmossdk.io/math.Uint", (gogoproto.nullable) = false];
  string app_layer_fees  = 3 [(gogoproto.customtype) = "cosmossdk.io/math.Uint", (gogoproto.nullable) = false];
  string rev_share       = 4 [(gogoproto.customtype) = "cosmossdk.io/math.Uint", (gogoproto.nullable) = false];
  string system_income   = 5 [(gogoproto.customtype) = "cosmossdk.io/math.Uint", (gogoproto.nullable) = false];
}
```
Then run `make proto-gen` to regenerate `type_events.pb.go`.

### 9. `x/thorchain/types/type_event.go`
- `NewEventRewards` (line 444): add `appLayerFees cosmos.Uint` param, set
  `AppLayerFees: appLayerFees`, and add
  `cosmos.NewAttribute("app_layer_fees", m.AppLayerFees.String())` to
  `EventRewards.Events()`.
- Add `NewEventSystemIncome` constructor + `Type()` / `Events()`:
  - `Type()` → `"system_income"`.
  - `Events()` attributes: `block_reward`, `liquidity_fees`, `app_layer_fees`,
    `rev_share`, `system_income`.
  - `system_income` is the **gross** (= sum of the inputs minus rev_share), so
    indexers can reconcile it against the component attributes.

### 10. `x/thorchain/alias.go`
Add aliases for the new event:
```go
NewEventSystemIncome = types.NewEventSystemIncome
EventSystemIncome    = types.EventSystemIncome
```

### 11. `x/thorchain/manager_network_current.go` — `UpdateNetwork`
After `calcBlockRewards` (and the drain), emit the system income event before the
rewards event (line ~2014):
```go
systemIncomeEvt := NewEventSystemIncome(blockReward, totalLiquidityFees, appLayerFees, totalRevShare, grossSystemIncome)
if err = eventMgr.EmitEvent(ctx, systemIncomeEvt); err != nil {
    return fmt.Errorf("fail to emit system income event: %w", err)
}
```
Update the existing `NewEventRewards(...)` call (line 2014) to pass `appLayerFees`.

---

## Test plan

**Compilation / existing suites**
- `x/thorchain/manager_network_current_test.go`: every `calcBlockRewards` call site
  (~18) gets `cosmos.ZeroUint()` added for the new `appLayerFees` arg (after
  `totalLiquidityFees`).
- `x/thorchain/types/type_event_test.go`: update the `NewEventRewards` call;
  add a `NewEventSystemIncome` test (attribute values + `Type()`).

**New behaviour**
- **Income inclusion:** with a funded `app_layer` module, `systemIncome` includes
  `appLayerFees`; total distributed to pools/bond increases by the drained amount.
- **Emission isolation (key regression):** an `app_layer` balance present at the
  `totalReserve` snapshot does **not** change `blockReward` (assert identical
  block reward with/without the balance) — proves the drain happens after the
  snapshot.
- **Guard roll-forward:** on an `availablePoolsRune == 0` (or `totalReserve == 0`)
  block, the `app_layer` balance is **not** drained — it persists in the module and
  is distributed on the next healthy block. (Guards against the strand-in-Reserve
  bug.)
- **Drain completeness:** after a healthy block, the `app_layer` module is emptied.
- **Events:** `system_income` event emitted with correct `block_reward`,
  `liquidity_fees`, `app_layer_fees`, `rev_share`, `system_income`; the `rewards`
  event carries a non-zero `app_layer_fees` attribute.

---

## Verification
1. `go build ./...` — compiles.
2. `go test ./x/thorchain/... -run TestBlockRewards` — pendulum tests pass with the
   new zero-value arg.
3. Mocknet manual check: fund the `app_layer` module address with RUNE; on the next
   block confirm:
   - a `system_income` event with correct component + gross values,
   - a non-zero `app_layer_fees` attribute on the `rewards` event,
   - pool/bond distribution increased by the drained amount,
   - and (guarded-block case) if no available pools, the funds remain in `app_layer`
     and are not stranded in Reserve.
