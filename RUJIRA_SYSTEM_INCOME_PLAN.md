# Plan: Rujira Revenue as Explicit THORChain System Income

**Status:** Design proposal for review (no code written yet)
**Goal:** Allow Revenue Collector 4 to send RUNE that is counted as **Gross System Income** and flows through the existing system-income distribution (TCY, burn, dev fund, marketing, POL, incentive pendulum → LP/bond) in the **same block**, with explicit events so indexers stop inferring revenue from Reserve transfers.

Collector 4 address (mainnet): `thor1txum04wp8ykqudphxy9prtwsd9jpcm2kwdaxctxeeyr6g0r0we9qpfdktr`

---

## 0. Key design decisions (read first)

These facts shape everything below and correct assumptions in the original plan.

1. **There is no generic `AddSystemIncome()` in the keeper, and we will not add one.**
   "System income" is computed once per block, inline, in `calcBlockRewards`
   (`x/thorchain/manager_network_current.go:2176`):
   ```go
   systemIncome := blockReward.Add(totalLiquidityFees)        // notional figure
   systemIncome = common.SafeSub(systemIncome, totalRevShare)
   ```
   We add a **dedicated per-block accumulator** and merge it here — exactly the
   pattern REVSHARE uses (`processRevShare`, line 2042). We do **not** thread a
   `source string` through keeper APIs.

2. **Rujira funds must NOT pass through the Reserve, because of the emission
   curve.** This is the core reason for the whole change. The block reward is
   `totalReserve / emissionCurve / blocksPerYear` (`manager_network_current.go:2164`),
   and `totalReserve` is read fresh at EndBlock (`line 1840`). The Rujira deposit
   handler runs *during the block* (DeliverTx), **before** EndBlock — so if the
   funds landed in Reserve they would already be sitting there when `totalReserve`
   is snapshotted, **inflating the emission-curve block reward** and partially
   subjecting Rujira revenue to the very emission mechanics we want to bypass.
   → Hold Rujira RUNE in a **dedicated `RujiraIncomeName` module account** (exactly
   like `POLReserveName` / `TCYStakeName`), never Reserve. The emission snapshot
   excludes it. Reserve is touched only as a transient rail *after* the snapshot
   (see step 5).

   Note: all split *transfers* (dev fund, burn, TCY, marketing, POL, pool/bond)
   are hardcoded `SendFromModule(ReserveName, …)`. Rather than refactor every one
   to source from a second module, we move the Rujira balance into Reserve **after
   the emission snapshot**, then add it to the notional `systemIncome`; the
   existing payout machinery distributes it in full the same block. The funds are
   in Reserve for a fraction of one EndBlock and are never emitted via the curve.

3. **The module balance IS the accumulator.** Because funds live in a dedicated
   module that is drained each block, `GetRuneBalanceOfModule(ctx, RujiraIncomeName)`
   is the pending income. No KV accumulator, no overflow guard, no separate
   "clear" step — and fund/accounting consistency is automatic (you can only
   distribute RUNE that is physically present).

4. **Distribution is immediate (same block), not slow emission.** Rujira is added
   as a **separate term** to `systemIncome`, so it is distributed in full the
   block it is processed — it does not go through `totalReserve / emissionCurve`.

**Critical invariant (the one real correctness trap):**
`calcBlockRewards` and the split transfers are **not always reached** —
`UpdateNetwork` early-returns when `availablePoolsRune` is zero
(`manager_network_current.go:1856`) and on other guards. The Rujira→Reserve move
must run **only on the path where distribution actually runs.** On any
skipped/guarded block, do **not** move the funds — they stay in `RujiraIncomeName`
and roll forward to the next block. Never move-without-distributing (would strand
RUNE in Reserve); never distribute-twice. With the module-as-accumulator design
this is automatic: if you don't move + distribute, the balance simply remains.

**Alternative (only if "never touch Reserve, even transiently" is a hard
requirement):** pay the Rujira-funded portion of each split directly from
`RujiraIncomeName` to each destination. Much larger change — duplicates the split
math and touches every transfer. The transient-rail approach above already keeps
Rujira out of the emission curve, which is the actual concern, so this is not
recommended unless explicitly required.

---

## 1. Collector address — config, NOT a bare `const`

Follow the `DevFundAddress` pattern (per-network constant) **and**
make it overridable by Mimir so it can be rotated without a chain upgrade.

Per-network defaults (`constants/constants_mocknet.go`, `_stagenet.go`,
`_chainnet.go`, and the mainnet default in `constant_values.go`):
```go
// constants/constants_chainnet.go (mainnet)
RujiraRevenueCollector: "thor1txum04wp8ykqudphxy9prtwsd9jpcm2kwdaxctxeeyr6g0r0we9qpfdktr",
// constants/constants_mocknet.go (regtest) — use an addr that exists in regtest
RujiraRevenueCollector: "tthor1...regtest_addr...",
```
Add `RujiraRevenueCollector` to the `constants` name enum (`constant_values.go`)
and regenerate the stringer (`constantname_string.go` is generated).

Lookup helper, with Mimir override:
```go
func getRujiraRevenueCollector(ctx cosmos.Context, k keeper.Keeper) (common.Address, error) {
    // Mimir override (string mimir) if set, else network constant
    if addr := k.GetConstants().GetStringValue(constants.RujiraRevenueCollector); addr != "" {
        return common.NewAddress(addr)
    }
    return common.NoAddress, fmt.Errorf("rujira revenue collector not configured")
}
```
**Decided:** per-network constant **+ Mimir override** (the form shown above), so
the collector can be rotated without a chain upgrade.

---

## 2. New memo: `rujira-income`

**Decided** canonical memo string: `rujira-income` (type const `TxRujiraIncome`).

`x/thorchain/memo/memo.go`:
```go
// add to the TxType const block
TxRujiraIncome

// stringToTxTypeMap
"rujira-income": TxRujiraIncome,

// txToStringMap
TxRujiraIncome: "rujira-income",

// IsInbound() — add TxRujiraIncome to the inbound case list
// HasOutbound() — add TxRujiraIncome to the `return false` list (like TxReserve)
```

New parser `x/thorchain/memo/memo_rujira_income.go` (mirrors `memo_reserve.go`,
no args):
```go
package thorchain

type RujiraIncomeMemo struct {
    MemoBase
}

func (p *parser) ParseRujiraIncomeMemo() (RujiraIncomeMemo, error) {
    return RujiraIncomeMemo{
        MemoBase: MemoBase{TxType: TxRujiraIncome},
    }, nil
}
```
Wire `TxRujiraIncome` into the parser dispatch (`memo_parser.go`, same place the
other `Parse*Memo` are called).

---

## 2b. Register the dedicated `RujiraIncomeName` module account

Funds must live outside Reserve (see decision #2). Add a dedicated module account,
mirroring `POLReserveName`.

- Name const: `x/thorchain/types/keys.go` (e.g.
  `RujiraIncomeName = "rujira_income"`) + alias in `x/thorchain/alias.go`.
- Permissions: `app/app.go` `maccPerms` (`app.go:117`) — empty perms `{}` like
  Reserve/POLReserve (no Minter/Burner):
  ```go
  thorchain.RujiraIncomeName: {},
  ```
- `app/app_test.go:101` asserts `maccPerms` matches; update accordingly.

## 3. Route funds to the Rujira module (NOT Reserve)

`x/thorchain/handler_deposit.go:174` — add a branch routing the new memo to
`RujiraIncomeName`, **not** Reserve. **This alone moves the deposited RUNE into the
Rujira module**; the handler needs no manual `SendFromAccountToModule`.
```go
case TxRujiraIncome:
    targetModule = RujiraIncomeName
```
The existing guard at line 186 already rejects non-RUNE/TCY coins for non-Asgard
modules, so a non-RUNE Rujira deposit is blocked here too (defence in depth; the
handler also checks asset explicitly).

---

## 4. Message + handler — reuse `MsgReserveContributor` (no new proto)

No new proto Msg. The new memo maps to the existing `MsgReserveContributor`; the
existing `ReserveContributorHandler` branches on the memo to apply Rujira-specific
validation and emit the `system_income` event. (Trade-off: the reserve handler
becomes dual-purpose, in exchange for avoiding a `.proto` edit + regen.)

### 4a. Memo → Msg (`x/thorchain/handler.go:252`, in `processOneTxIn`)
Reuse the same `MsgReserveContributor` the `ReserveMemo` case builds:
```go
case ReserveMemo, RujiraIncomeMemo:
    res := NewReserveContributor(tx.Tx.FromAddress, tx.Tx.Coins.GetCoin(common.RuneAsset()).Amount)
    newMsg = NewMsgReserveContributor(tx.Tx, res, signer)
```
No new entry in `getInternalHandlerMapping` — it already routes
`MsgReserveContributor` to `ReserveContributorHandler`.

### 4b. Branch in `ReserveContributorHandler` (`handler_reserve_contrib.go`)
Detect the Rujira variant from the memo. **Rujira-specific validation lives here**
(sender == collector, asset == RUNE); a non-collector or non-RUNE deposit returns
an error, which reverts the whole MsgDeposit (the deposit handler's fund routing
in step 3 runs in the same cached context, so the funds are not committed).
```go
func (h ReserveContributorHandler) handle(ctx cosmos.Context, msg MsgReserveContributor) error {
    memo, err := ParseMemo(ctx, ... , msg.Tx.Memo) // or pass type through
    if err == nil && memo.GetType() == TxRujiraIncome {
        return h.handleRujiraIncome(ctx, msg)
    }
    // ...existing reserve path: emit NewEventReserve...
}

func (h ReserveContributorHandler) handleRujiraIncome(ctx cosmos.Context, msg MsgReserveContributor) error {
    coin := msg.Tx.Coins.GetCoin(common.RuneAsset())
    if coin.IsEmpty() || !coin.Asset.IsRune() {
        return fmt.Errorf("rujira income must be THOR.RUNE")
    }
    // Sender must be the configured collector.
    // NOTE: guards DATA INTEGRITY / grief, not fund safety — a rogue sender would
    // merely be donating their own RUNE into the same-block distribution.
    collector, err := getRujiraRevenueCollector(ctx, h.mgr.Keeper())
    if err != nil {
        return err
    }
    if !msg.Tx.FromAddress.Equals(collector) {
        return fmt.Errorf("unauthorized rujira income sender: %s", msg.Tx.FromAddress)
    }
    // Funds already in the RujiraIncome module (step 3); the module balance is the
    // accumulator (decision #3). Nothing to credit — just emit the canonical event.
    evt := NewEventSystemIncome("rujira", msg.Tx.FromAddress, common.RuneAsset(), coin.Amount, msg.Tx.ID)
    if emitErr := h.mgr.EventMgr().EmitEvent(ctx, evt); emitErr != nil {
        ctx.Logger().Error("fail to emit system_income event", "error", emitErr)
    }
    return nil
}
```
> **Alternative placement:** the collector check could instead live in the deposit
> routing (step 3) to reject *before* funds move. Cleaner failure, but scatters
> business logic into `handler_deposit.go`. Recommend keeping it in the handler;
> the revert-on-error behaviour makes the ordering safe either way.

---

## 5. Merge into Gross System Income at end-block — ORDER IS CRITICAL

`x/thorchain/manager_network_current.go`, `UpdateNetwork`. The sequence must be:

1. **Snapshot Reserve first** (existing line 1840) — Rujira module excluded, so the
   emission-curve block reward is NOT inflated:
   ```go
   totalReserve := vm.k.GetRuneBalanceOfModule(ctx, ReserveName)   // unchanged
   ```
2. **Read the Rujira module balance** (after the snapshot), near the
   `totalLiquidityFees` read (~1862):
   ```go
   rujiraIncome := vm.k.GetRuneBalanceOfModule(ctx, RujiraIncomeName)
   ```
3. **Fold into `systemIncome` as a separate term** — pass `rujiraIncome` into
   `calcBlockRewards` (new param next to `totalLiquidityFees`), line 2176:
   ```go
   systemIncome := blockReward.Add(totalLiquidityFees).Add(rujiraIncome)
   systemIncome = common.SafeSub(systemIncome, totalRevShare)
   ```
   (`blockReward` is still derived purely from `totalReserve`, untouched by Rujira.)
4. **Move Rujira → Reserve only on the distribution path**, so the existing
   `SendFromModule(ReserveName, …)` splits have the cash. Do this after the
   emission snapshot and once past the `availablePoolsRune == 0` guard (line 1856):
   ```go
   if !rujiraIncome.IsZero() {
       if err = vm.k.SendFromModuleToModule(ctx, RujiraIncomeName, ReserveName,
           common.NewCoins(common.NewCoin(common.RuneNative, rujiraIncome))); err != nil {
           return fmt.Errorf("fail to move rujira income to reserve: %w", err)
       }
   }
   ```
   On any early-return/guard path, **do not move** — the funds stay in
   `RujiraIncomeName` and roll forward to the next block automatically. No double
   count (we only add `rujiraIncome` to `systemIncome` on the path that also moves
   + distributes it).

Add `rujira` to the existing pendulum log line (~2251) for observability.

> **Subtle ordering note for the reviewer:** the move in step 4 must be gated on
> exactly the same condition as the distribution. Read `rujiraIncome` once; ensure
> there is no path that adds it to `systemIncome` without also moving the funds
> (would overdraw Reserve) or moves without adding (would mis-distribute).

---

## 6. Event — `system_income` acceptance event

Emitted in the handler (step 4b) when a Rujira deposit is accepted. This is the
**canonical signal for indexers** — they derive Rujira Gross System Income from
this event, not from Reserve transfers.

New event type, mirror `EventRevShare` (`types/type_event.go:289` +
`type_events.pb.go` regen + `.proto`). **Heads-up: events are protobuf-generated**
— this requires editing the proto and `make proto-gen`, not just a hand-written
struct.
```
event: system_income
attrs: source=rujira, collector=<addr>, asset=THOR.RUNE, amount=<uint>, txid=<hash>
```
(No aggregate `system_income_split` event — the per-tx gross above is sufficient.
A split event would also be misleading since the bps deductions apply to the
*pooled* system income, not to Rujira alone.)

---

## 7. Indexer (separate repo — out of scope for thornode)

Midgard/the indexer is not in this repo. The thornode deliverable is "emit the
canonical `system_income` event." Track separately:
- Derive Rujira Gross System Income from `system_income` events with
  `source=rujira`.
- **Remove** any logic that counts Collector-4 → Reserve transfers as gross
  income, to avoid double counting once the event path is live.

---

## 8. Test plan

**Handler (`handler_reserve_contrib_test.go` — Rujira branch):**
- valid collector + valid memo + THOR.RUNE → `system_income` event emitted
- wrong sender → rejected (deposit reverts)
- non-RUNE asset → rejected
- zero amount → rejected
- funds land in the **`RujiraIncomeName` module** (assert balance delta), and
  **Reserve is unchanged** at deposit time
- reserve memo still emits the existing `reserve` event (no regression from the
  dual-purpose handler branch)

**Emission isolation (the key regression test):**
- a Rujira deposit in block N does **not** change the emission-curve block reward
  computed in block N's EndBlock (assert `blockReward` identical with/without the
  deposit) — proves the funds bypass the emission curve

**Network manager (`manager_network_*_test.go`):**
- rujira income is added to `systemIncome` *before* tcy/burn/dev/pendulum splits
- after a normal distribution block: `RujiraIncomeName` drained to zero, Reserve
  net-neutral on the rujira amount (in then fully out)
- `RujiraIncomeName` balance **persists** (rolls forward) on an
  `availablePoolsRune == 0` guarded block (no funds lost, no double count, no
  premature move to Reserve)
- `system_income` event emitted with correct attrs

**Double-count guard:** assert the rujira amount is added to `systemIncome` only
on the path that also moves it to Reserve and distributes it (never added without
moved, never moved without added).

---

## 9. Decisions (locked)

All design questions resolved — no open items.

1. **Collector address:** per-network constant + Mimir override (step 1).
2. **Memo string:** `rujira-income` (step 2).
3. **Distribution timing:** immediate, same-block (decision #4, step 5).
4. **Reserve rail:** the transient Rujira→Reserve rail is accepted — it keeps
   Rujira out of the emission curve, which is the actual requirement (decision #2,
   step 5.4). Funds never enter Reserve before the emission snapshot.
5. **Message:** reuse `MsgReserveContributor`, no dedicated `MsgRujiraIncome`
   proto; the handler branches on memo (step 4).
6. **Events:** per-tx `system_income` event only; no aggregate split event
   (step 6).
