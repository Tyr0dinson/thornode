# TSS DKLS

## Summary

This branch adds DKLS as the ECDSA signing engine behind Mimir `EnableDKLS`.
GG20 remains the default. Existing GG20 vaults keep signing with GG20 because
the local keyshare state carries the signing engine. New ECDSA vaults created
after `EnableDKLS=1` use DKLS.

The THORChain protocol/session APIs remain the same. Bifrost chooses the engine
internally, so keygen/keysign request flow, MsgTssPool, and MsgTssKeysignFail
stay compatible.

## Build And Shipping

DKLS is built from the `dkls23-rs` engine through Go wrappers. Those Go
wrappers now live directly inside the THORChain codebase at:

```text
dkls/go-wrappers/go-dkls
```

The wrapper package is normal in-repo Go code owned by THORChain. It exposes
the native DKLS keygen/keysign session API to Bifrost. The remaining external
runtime artifact is the platform native shared library.

The upstream Rust source is pinned in `dkls/SOURCE`:

```text
repository: https://github.com/silence-laboratories/silent-shard-dkls23-ll.git
commit:     e22cea6efb7650b928ee869251e01f3d35b68b55
package:    dkls23-ll
sl-crypto:  f366497
```

`dkls/build-libgodkls.sh` fetches that repository/ref and runs:

```text
cargo build --manifest-path "$SRC_DIR/Cargo.toml" --release -p dkls23-ll
```

The current upstream does not yet emit the `libgodkls` C ABI consumed by
`dkls/go-wrappers/go-dkls/include/go-dkls.h`. The checked-in native libraries
therefore still come from `dkls/go-wrappers/DKLS_VERSION` commit
`c9ef1f8ee76286c2fdbe6630b8fc9fcacb99cb76`, with THORChain's abort-message
wrapper export applied.

The Docker image copies the native DKLS library into the runtime image:

```text
dkls/go-wrappers/includes/linux/${TARGETARCH}/libgodkls.so -> /usr/lib/libgodkls.so
```

This avoids manual node setup for DKLS native libraries.

Required shipping pieces:

```text
1. DKLS Go wrapper code maintained in the THORChain repo under dkls/go-wrappers/go-dkls.
2. libgodkls native artifacts under dkls/go-wrappers/includes.
3. libgodkls.so copied into the Bifrost/thornode Docker runtime image.
4. Runtime linker path/image layout must let Bifrost load libgodkls.so without operator setup.
5. Platform support is limited by available native libraries.
```

Checked-in native library sizes:

```text
linux/amd64 libgodkls.so      1,851,664 bytes
linux/arm64 libgodkls.so      1,684,776 bytes
darwin/arm64 libgodkls.dylib  1,510,640 bytes
```

## Integration

DKLS is integrated under Bifrost TSS as a second ECDSA engine. The key external
behavior is unchanged: keygen still produces normal THORChain secp256k1 vault
pubkeys, and keysign still returns the existing `R`, `S`, and `RecoveryID`
shape.

Keygen integration:

```text
1. Bifrost resolves the requested ECDSA signing engine.
2. GG20 or DKLS runs behind the same keygen ceremony flow.
3. DKLS serializes its keyshare bytes into LocalData.
4. LocalData stores signing_engine=dkls for DKLS vaults.
5. Missing signing_engine defaults to gg20 for old vault compatibility.
```

Keysign integration:

```text
1. Bifrost loads local state for the vault pubkey.
2. signing_engine selects DKLS or GG20.
3. DKLS signatures are adapted into the existing keysign.Response format.
4. Ed25519/EDDSA paths are unchanged.
```

Blame integration:

```text
1. DKLS sender/message failures map into existing types.Blame.
2. Native identifiable aborts map DKLS party index back to THORChain pubkey.
3. Keygen blame is posted through MsgTssPool.
4. Keysign blame is posted through MsgTssKeysignFail.
5. Empty-blame fallbacks are forced so failures remain slashable.
```

## Engine Flip And Compatibility

`EnableDKLS` controls new ECDSA vault creation:

```text
EnableDKLS absent/0 -> new ECDSA vaults use GG20
EnableDKLS=1        -> new ECDSA vaults use DKLS
```

Existing vaults are backward compatible because their local keyshare state
selects the engine:

```text
old local state without signing_engine -> GG20
local state with signing_engine=gg20   -> GG20
local state with signing_engine=dkls   -> DKLS
```

Operationally this means a GG20 vault can retire and migrate funds after
`EnableDKLS=1`; the retiring GG20 vault still signs with GG20, while the next
vault is DKLS.

## Plug And Play Notes

DKLS is plug and play at the THORChain ceremony/API boundary:

```text
same keygen request flow
same keysign request flow
same P2P session plumbing
same vault pubkey format
same MsgTssPool and MsgTssKeysignFail posting
same chain signer APIs consuming R/S/RecoveryID
```

Compromises:

```text
1. LocalData must record signing_engine for new DKLS shares.
2. Docker images must ship the native DKLS shared library.
3. DKLS message IDs include the engine suffix to avoid session collision.
4. Blame adapters are required to translate DKLS abort/sender evidence into THORChain blame.
```

## Files Changed

```text
dkls/go-wrappers/
```

Added THORChain-owned DKLS Go wrapper code, native `libgodkls` artifacts,
source/build provenance, and wrapper documentation. This is the bulk of the
new DKLS binding surface used by Bifrost.

```text
bifrost/tss/go-tss/tss/dkls.go
bifrost/tss/go-tss/tss/dkls_test.go
bifrost/tss/go-tss/tss/keygen.go
bifrost/tss/go-tss/tss/keysign.go
bifrost/tss/keygen.go
bifrost/tss/keygen_test.go
bifrost/tss/keysign_error.go
bifrost/p2p/messages/
bifrost/p2p/storage/localstate_mgr.go
```

Added the DKLS keygen/keysign engine, protocol messages, result broadcast,
blame conversion, and tests. Local keyshare state now records
`signing_engine`, defaulting missing values to GG20 for old vaults.

```text
constants/mimir_strings.go
constants/constants_chainnet.go
bifrost/signer/sign.go
x/thorchain/types/tss_round_normalizer.go
x/thorchain/types/msg_tss_sign_fail.go
x/thorchain/handler_tss.go
```

Added the `EnableDKLS` switch, TSS round normalization for DKLS aborts, and
small Thornode/Bifrost integration changes so DKLS failures remain compatible
with existing TSS messages and slash/jail handling.

```text
build/docker/
build/scripts/genesis.sh
ci/Dockerfile.regtest
```

Added Docker image/runtime support for `libgodkls`, DKLS chainnet timing and
blame scripts, generated compose files, and local chainnet helpers used to
validate DKLS keygen, keysign, blame, and migration behavior.

```text
docs/bifrost/dkls.md
docs/bifrost/tss.md
```

Added DKLS operating documentation and updated the main TSS doc to describe
DKLS as an optional ECDSA engine for new vaults.

## Benchmarks

Scripts:

```text
build/docker/dkls-chainnet-generate-compose.sh
build/docker/dkls-chainnet-timing-run.sh
build/docker/dkls-chainnet-multihost-deploy.sh
build/docker/dkls-chainnet-blame.sh
```

Results:

```text
build/docker/dkls-chainnet-results/
```

### Multihost Results

```text
run                             topology             GG20 keygen   GG20 keysign   DKLS keygen                    DKLS keysign
multihost-7-20260518T034700Z    7 nodes / 7 hosts    6.18-7.04s    n/a            1.91-2.13s                    0.616s
multihost-7                     7 nodes / 7 hosts    n/a           1.019s         1.262-1.275s                  0.599s
multihost-14-20260518T115810Z   14 nodes / 14 hosts  9.467s        n/a            3.820-4.061s first result     0.636-0.752s first result
                                                                                   3.912-4.149s all observed     0.713-0.821s all observed
multihost-21-20260518T180730Z   21 nodes / 21 hosts  240.381s*     4.662s         5.834s first result           0.922s first result
multihost-21-gg20-redo          21 nodes / 21 hosts  14.362s       pending        pending                       pending
multihost-28-20260519T002201Z   28 nodes / 28 hosts  19.480s       6.532s         8.603s first result           1.085s first result
```

The 14-node DKLS values are from the observed 12-party DKLS vault produced
during the 14-host run. GG20 initial keygen in that run produced a 13-member
vault.

The original 21-node GG20 keygen value is marked with `*` because the rerun
produced a clean 20-member GG20 vault in 14.362s. The 21-node DKLS values are
from the observed 19-party DKLS vault produced during the 21-host run.

The 28-node values are from the accepted 27-member GG20 vault and 26-member
DKLS vault. Keyshare sizes measured on node28 were 109,265 bytes for GG20
and 2,192,598 bytes for DKLS.

### Keyshare Sizes

```text
run                            engine  vault members  keyshare size
multihost-21-gg20-redo         GG20    20             83,104 bytes
multihost-21-gg20-redo         DKLS    pending        pending
multihost-28-20260519T002201Z  GG20    27             109,265 bytes
multihost-28-20260519T002201Z  DKLS    26             2,192,598 bytes
```

## Blame Testing

The blame runner is:

```text
build/docker/dkls-chainnet-blame.sh
build/docker/docker-compose.dkls-abort.yml
```

The blame cluster bootstraps directly into DKLS, then injects controlled
Bifrost-side DKLS failures.

Covered fault modes:

```text
join
exit
corrupt
malformed
auth
result
error
```

The tests deliberately corrupt or break a participant during the ceremony, then
validate that another node reports blame and that the blame lands on-chain by
checking slash points or jail release height.

### Keygen Cases

Covered keygen blame paths:

```text
default_join    -> fallback blame for the joining/new node
default_exit    -> fallback blame for the exiting/new node path
corrupt         -> blame the corrupting sender
malformed       -> blame the malformed message sender
auth            -> blame the bad-auth sender
native_error    -> blame native DKLS identifiable-abort party
```

Expected result: `MsgTssPool` carries non-empty blame and Thornode applies
slash/jail to the blamed node.

### Keysign Cases

Covered keysign blame paths:

```text
default_exit    -> fallback blame for the leader/member selected by the run
corrupt         -> blame the corrupting signer
malformed       -> blame the malformed message sender
auth            -> blame the bad-auth sender
result          -> blame the bad-result broadcaster
native_error    -> blame native DKLS identifiable-abort party
```

Expected result: Bifrost posts `MsgTssKeysignFail` with non-empty blame, and
Thornode applies slash/jail to the blamed node.

## Blame Validation Notes

The important checks were:

```text
1. Blame data is not empty.
2. Blame identifies the corrupting or malformed sender when available.
3. Native DKLS identifiable aborts map the DKLS party index back to the THORChain node pubkey.
4. Keygen fallback blame uses the joining/new node when no key was generated.
5. Keysign fallback blame uses the selected leader/member path when DKLS cannot identify a better party.
6. Keygen blame is posted through MsgTssPool.
7. Keysign blame is posted through MsgTssKeysignFail.
8. On-chain state changes: slash points or jail release height increase for the blamed node.
```
