# Previous-application upgrade fixture

`home.zip` contains a GoLevelDB application database committed at height 7 by
THORNode commit `757199a809e069932aeaff1628a6483d5fdde416` (release 3.20.0,
SDK 0.53.0, Store v1), plus the existing contract's portable Wasm bytecode.
The original source, go.mod, go.sum and gas-fee genesis are unchanged.
Native compiled Wasm caches and lock/log files are excluded.

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
python3 - "$baseline_dir/fixture-home" "$fixture_dir/home.zip" <<'PY'
import pathlib, sys, zipfile
root = pathlib.Path(sys.argv[1])
with zipfile.ZipFile(sys.argv[2], 'w', zipfile.ZIP_DEFLATED) as archive:
    for path in sorted(root.rglob('*')):
        relative = path.relative_to(root).as_posix()
        if (path.is_file() and '/cache/' not in relative
                and path.name not in ('LOCK', 'LOG', 'exclusive.lock')):
            info = zipfile.ZipInfo(relative, (2026, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o100644 << 16
            archive.writestr(info, path.read_bytes())
PY
```

LevelDB file layout may vary on regeneration; the asserted state is reproducible.
Run `go test -tags mocknet ./app -run '^TestSDKScheduledUpgradeFromOldDatabase$'`
to open the fixture using the new application, execute the registered upgrade
through FinalizeBlock, commit, restart, and verify migrations, identities,
validator versions, a signed bank send and a signed contract execution.
The regular mocknet unit-test CI job includes this test.
