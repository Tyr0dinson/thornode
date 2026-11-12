# TRON signer-cache recovery

TRON recovery requires an expired signer-cache entry and
`SkipSignerCacheTRON > 0`. Set `BIFROST_CHAINS_TRON_SOLIDITY_API_HOST`
(`bifrost.chains.tron.solidity_api_host`) to the solidity HTTP API base URL.
Node-launcher sets it to `http://tron-daemon:8091`; the ordinary API remains on
port 8090 for scanning, transaction construction and broadcast. JSON-RPC keeps
its separate endpoint.

The setting is optional and defaults to empty. When it is missing, or the
endpoint is unavailable, failed-payment recovery stays blocked. Bifrost never
falls back to a latest-head receipt. The solidity endpoint must serve
`/walletsolidity/gettransactioninfobyid`. A matching receipt at a positive block
height must explicitly report `result: FAILED` and a recognized execution failure.
Successful execution, missing history, empty responses, unknown results and RPC
errors keep the payment blocked. A single empty lookup does not prove absence.
The scanner continues to use latest-head receipts for gas accounting.

Local signing and broadcasting are serialized by the cross-vault payment cache
identity, including saved signed retries. Locks exist only while a caller holds
or waits for them; unrelated payments do not share a lock. Existing UTXO vault
locks still cover consolidation and ordinary outbounds. Local lock acquisition
order does not establish network-wide keysign order; conflicting cross-vault
attempts may still require timeout and retry under existing scheduling rules.

The failed cache entry remains until an accepted replacement is recorded. A
checkpoint matching the proven failed transaction is retired locally without an
automatic observation; THORChain still owns the owed outbound and its rescheduling.
A distinct checkpoint keeps its exact bytes, including after partial signature
exposure. This applies to user outbounds and scheduled internal vault transfers.
TRON has no consolidation scheduler; UTXO consolidation decisions are unchanged.

Saved signed payloads bypass `SignTx`. Broadcast retry therefore also checks
whether a duplicate is positively proven failed before treating it as accepted.
Such failures retire the local attempt without renewing its cache or emitting its
sign-time observation as success.

If an accepted transaction conflicts with a different cached transaction, the
previous transaction must be positively proven failed before replacing its record.
Otherwise Bifrost preserves the cached identity without expiry and reports
`reconciliation required`. This safety hold survives restart and requires operator
reconciliation of both transaction IDs. A temporary RPC failure during this rare
conflict can therefore require manual recovery. Automatically expiring this hold
could permit a third payment while either earlier transaction has already paid.
A routine recovery does not repeat the lookup: it reuses the failure `SignTx`
proved under the same payment lock, because a solidified failure is final. Only
saved signed retries, which skip `SignTx`, query the previous transaction again.
