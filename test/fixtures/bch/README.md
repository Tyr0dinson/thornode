# BCHN capture fixtures

These fixtures are unedited JSON-RPC responses captured from Bitcoin Cash Node
29.0.0 on regtest. They cover the response formats used by Bifrost for relay
fees, average block fee rates, verbose blocks, and verbose transactions.

The linked capture set uses:

- block height: `102`
- block hash: `43159289e4b6d64cf809068056f82de496c117b9fb19fe5557fcf651bb74ff0e`
- transaction: `ace2c8eeb7c6a57d5ba18c5db686673dfe5c8cb1204cbf77abbf577e89955620`
- daemon: `/Bitcoin Cash Node:29.0.0(EB32.0)/`

The files can be reproduced from a BCHN regtest node after creating and mining
a transaction that pays one native base unit per byte:

```text
bitcoin-cli -regtest getnetworkinfo
bitcoin-cli -regtest getblockhash 102
bitcoin-cli -regtest getblockstats 43159289e4b6d64cf809068056f82de496c117b9fb19fe5557fcf651bb74ff0e
bitcoin-cli -regtest getblock 43159289e4b6d64cf809068056f82de496c117b9fb19fe5557fcf651bb74ff0e 2
bitcoin-cli -regtest getrawtransaction ace2c8eeb7c6a57d5ba18c5db686673dfe5c8cb1204cbf77abbf577e89955620 true
```

Keep the full JSON-RPC envelope, including `result`, `error`, and `id`, when
refreshing a fixture. The suite replaces the captured request ID with the ID of
the mock request before returning the response.
