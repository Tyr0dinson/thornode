# DKLS

This directory contains the DKLS Go bindings and native library artifacts used
by Bifrost.

The Go wrapper source under `go-dkls` is maintained as normal THORChain
in-repo code. It is not pulled from an external Go wrapper dependency at build
time.

The original DKLS Rust source is pinned in `../SOURCE` and can be fetched and
built with `../build-libgodkls.sh`.

The current bundled native library still comes from the commit recorded in
`DKLS_VERSION`, with THORChain's abort-message wrapper export applied. The
upstream Rust source does not yet expose the `libgodkls` C ABI consumed by
`go-dkls/include/go-dkls.h`, so a thin Rust FFI crate is still required before
the checked-in native library artifacts can be replaced by source-built output.

Bundled artifacts:

- `includes/darwin/arm64/libgodkls.dylib`
- `includes/linux/amd64/libgodkls.so`
- `includes/linux/arm64/libgodkls.so`

Current SHA-256:

```text
9f86963b17a7f3d62ae89a140c48eac81f1105c8e90751935ca0d98cbb7b81e0  includes/linux/amd64/libgodkls.so
b641716a7735b2ac1c454d40ba205e61e2a57c44ebe31e75e869b28a400f2828  includes/linux/arm64/libgodkls.so
dd982b32dc4edd2084295607bce402ec83ccc0c6ccc2907c099ea24f0509d65c  includes/darwin/arm64/libgodkls.dylib
```
