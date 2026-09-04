#!/usr/bin/python3

import json
import sys

if len(sys.argv) != 5:
    print(f"usage: {sys.argv[0]} <relay-spec> <para-id> <head-file> <wasm-file>")
    sys.exit(1)

specfile, para_id, headfile, wasmfile = sys.argv[1:]

with open(headfile) as f:
    genesis_head = f.read().strip()

with open(wasmfile) as f:
    validation_code = f.read().strip()

with open(specfile) as f:
    data = json.load(f)

runtime_genesis = data.get("genesis", {}).get("runtimeGenesis")
if runtime_genesis is None:
    print(f"{specfile} is not a plain chain spec with a runtimeGenesis section")
    sys.exit(1)

# Named presets use "patch"; full specs use "config".
patch = runtime_genesis.get("patch", runtime_genesis.get("config"))
if patch is None:
    print(f"{specfile} has neither a genesis patch nor a genesis config")
    sys.exit(1)

# Register the parachain at genesis so it receives a core.
patch["paras"] = {
    "paras": [
        [
            int(para_id),
            {
                "genesis_head": genesis_head,
                "validation_code": validation_code,
                "parachain": True,
            },
        ]
    ]
}

# Enable async backing required by Asset Hub.
config = patch.get("configuration", {}).get("config")
if config is None or "async_backing_params" not in config:
    print(f"{specfile} has no configuration.config.async_backing_params to raise")
    sys.exit(1)
config["async_backing_params"] = {
    "max_candidate_depth": 3,
    "allowed_ancestry_len": 2,
}

with open(specfile, "w") as f:
    json.dump(data, f, indent=2)
