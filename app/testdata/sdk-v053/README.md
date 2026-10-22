# Previous-application upgrade fixture

`application.db.hex` is every key/value pair of a GoLevelDB application
database committed at height 7 by THORNode commit
`757199a809e069932aeaff1628a6483d5fdde416` (release 3.20.0, SDK 0.53.0,
Store v1), one hex `key=value` line each, in key order. The test writes the
pairs into a fresh GoLevelDB and places the contract's stored code, which is
`test/fixtures/wasm/counter.wasm` byte for byte, under `data/wasm/state/wasm/`.
The fixture is plain text so the whole previous-application state can be read
in review.

`generate_test.go` loads that commit's Wasm gas-fee export using the **old**
application, restores the export's stripped validator versions to **3.20.0**,
adds a deterministic funded account (number 123, sequence 456), schedules
`4.0.0` at height 8, and commits height 7. It preserves the module-version map
written by the old InitChainer (auth 5, Wasm 4). The account key is public test
data derived from `sdk-upgrade-existing-account`.

To regenerate from the upgraded checkout (requires the old dependencies and
native Wasm library):

```sh
fixture_dir="$PWD/app/testdata/sdk-v053"
baseline_dir=$(mktemp -d)
git archive 757199a809e069932aeaff1628a6483d5fdde416 | tar -x -C "$baseline_dir"
cp "$fixture_dir/generate_test.go" "$baseline_dir/app/sdk_fixture_generate_test.go"
(cd "$baseline_dir" && SDK_UPGRADE_FIXTURE="$baseline_dir/fixture-home" \
  go test -tags mocknet ./app -run '^TestGenerateSDKUpgradeFixture$' -count=1)
cp "$baseline_dir/fixture-home/application.db.hex" "$fixture_dir/"
```

Regeneration is byte-for-byte reproducible.

Run `go test -tags mocknet ./app -run '^TestSDKScheduledUpgradeFromOldDatabase$'`
to open the fixture using the new application, execute the registered upgrade
through FinalizeBlock, commit, restart, and verify migrations, identities,
validator versions, a signed bank send and a signed contract execution.
The regular mocknet unit-test CI job includes this test.
