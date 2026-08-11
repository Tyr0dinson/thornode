# Bitcoin ABC fixture provenance

These responses were captured from the official Bitcoin ABC 0.33.8 x86_64
release on regtest with eCash denomination enabled (the release default).

Bitcoin ABC 0.33.8 does not expose Bitcoin Core's count-based
`-limitancestorcount` or `-limitdescendantcount` policy settings. Its tagged
[initialization](https://github.com/Bitcoin-ABC/bitcoin-abc/blob/v0.33.8/src/init.cpp)
and [policy](https://github.com/Bitcoin-ABC/bitcoin-abc/blob/v0.33.8/src/policy/policy.h)
sources contain no corresponding option or default. Bifrost therefore uses
`max_mempool_ancestors: 0` for XEC, which disables its own client-side count
guard.

The daemon was started with:

```text
bitcoind -regtest -avalanche=0 -server=1 -txindex=1
```

In this release, omitting `sendrawtransaction`'s `maxfeerate` argument applies
Bitcoin ABC's built-in `100,000 XEC/kB` safety limit; passing zero disables it.

A wallet was created, 101 blocks were mined, and a transaction paying
`100.00 XEC` plus an OP_RETURN memo was mined in block 102. The transaction
pays the daemon-selected relay fee of `2.64 XEC`, reported by
`getblockstats` as `0.01 XEC` per byte.

The fixtures are unmodified JSON-RPC results wrapped in the response envelope
expected by the UTXO mock server. Transaction and block identifiers are pinned
so the raw input, inbound transaction, verbose block, and block statistics can
be checked together.
