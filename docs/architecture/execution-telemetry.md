# Execution telemetry

Tracing is disabled by default. Prometheus metrics also require the Cosmos SDK
`telemetry.enabled` setting.

```yaml
thor:
  telemetry:
    trace_managers: false
    trace_handlers: false
    trace_contracts: false
    trace_payloads: false
```

`trace_payloads` requires `trace_contracts` and debug logging. It logs the top-level call,
payload content or base64, full size, SHA-256 hash, truncation state, contract address,
code ID when available, result, and runtime. Payload content is never a Prometheus label.
Payload logging does not require `telemetry.enabled`. Payloads contain user-supplied call
arguments, so review log access and retention before enabling it.

Payload content is capped at 16 KiB per entry. Larger payloads are truncated and base64
encoded, while the full size and hash remain available. Call names over 256 bytes are
reported as `unknown`.

## Code structure

`thorchain.Tracer` owns the Prometheus collectors and payload logging. Managers, handlers,
and the Wasm manager share it.

Shared THORChain and scheduler label values live in `common/telemetry`.

`scheduler/keeper.TracingWasmKeeper` executes and traces scheduled Wasm calls. The scheduler
keeper still owns scheduling, cached state, events, errors, and panic recovery.

## Consensus safety

Telemetry never changes application state or consensus results. Contract tracing adds no
store reads, so it does not change SDK gas. When tracing is off, wrappers call the manager,
handler, or Wasm keeper directly.

Recording is best-effort. Telemetry panics are contained and cannot replace the original
result, error, or panic.

Tracing uses CPU, memory, and log I/O. Leave enough capacity for consensus, especially with
payload logging enabled.

## Prometheus metrics

- `thornode_execution_manager_runs_total`
- `thornode_execution_manager_duration_seconds`
- `thornode_execution_handler_runs_total`
- `thornode_execution_handler_duration_seconds`
- `thornode_execution_contract_runs_total`
- `thornode_execution_contract_duration_seconds`
- `thornode_execution_contract_payload_bytes`

`result` is `success`, `error`, or `panic`. Calls without an error return can only report
`success` or `panic`: gas begin/end, slasher begin, observer end, and validator end.

Handler `mode="execution"` covers normal calls; `mode="simulation"` covers Cosmos SDK
transaction and THORChain quote simulations.

Contract metrics include `contract` and `code_id`. The Wasm manager uses code IDs already
available during execution. Scheduled calls use `code_id=""` to avoid an extra store read.
Local contract series last until restart; each is then recreated on its next call.

> **Cardinality:** `contract_runs_total` and `contract_duration_seconds` use `operation`,
> `contract`, `code_id`, and `result`; `contract_payload_bytes` omits `result`. Each
> histogram label combination exports 21 series (18 buckets, `+Inf`, `_sum`, and `_count`).
> For one operation, contract, and code ID with `R` results, all three metrics export
> `22R + 21` series. More contracts and code IDs raise memory, scrape, storage, and query
> costs. Restart clears local series, not stored samples; monitor growth and set retention.

Failed instantiation uses `contract="unassigned"`; other missing addresses use
`contract="unknown"`.

Metrics are cumulative, so a running process retains executions between 10-second scrapes.
Use queries or recording rules for one-minute windows:

```promql
sum by (manager, operation, result) (
  increase(thornode_execution_manager_runs_total[1m])
)
```

```promql
sum by (msg_type, path, mode, result) (
  increase(thornode_execution_handler_runs_total[1m])
)
```

```promql
sum by (operation, contract, code_id, result) (
  increase(thornode_execution_contract_runs_total[1m])
)
```

For a one-minute contract p95 runtime:

```promql
histogram_quantile(
  0.95,
  sum by (le, operation, contract, code_id) (
    rate(thornode_execution_contract_duration_seconds_bucket[1m])
  )
)
```
