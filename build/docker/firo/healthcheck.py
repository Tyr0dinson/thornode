#!/usr/bin/env python3
"""Require the configured network, an unpruned node and a recent ChainLock."""
import json
import os
import subprocess
import sys
import time
from pathlib import Path


def rpc(*args):
    result = subprocess.run(
        [
            os.environ.get("FIROCLI", "firo-cli"),
            "-datadir=" + os.environ.get("FIRO_DATA_DIR", "/data"),
            *args,
        ],
        capture_output=True,
        text=True,
        timeout=10,
    )
    if result.returncode:
        raise RuntimeError("FIRO RPC unavailable")
    return json.loads(result.stdout)


try:
    if os.environ.get("FIRO_MOCKNET") == "1":
        ready = json.loads(
            (
                Path(os.environ.get("FIRO_QUORUM_DIR", "/quorum")) / "ready.json"
            ).read_text()
        )
        if time.time() - ready["updated"] > ready["max_age"]:
            raise RuntimeError("mocknet miner is not ready")
    info = rpc("getblockchaininfo")
    if info["chain"] != os.environ.get("FIRO_NETWORK", "main") or info["pruned"]:
        raise RuntimeError("wrong network or pruned node")
    if info["headers"] > info["blocks"] + 1:
        raise RuntimeError("node is still catching up")
    tip = rpc("getblockheader", info["bestblockhash"])
    if not tip.get("chainlock", False):
        if info["blocks"] == 0:
            raise RuntimeError("no ChainLock")
        previous = rpc("getblockheader", rpc("getblockhash", str(info["blocks"] - 1)))
        if not previous.get("chainlock", False):
            raise RuntimeError("ChainLocks are behind the tip")
except (
    OSError,
    KeyError,
    ValueError,
    RuntimeError,
    subprocess.SubprocessError,
) as error:
    print(str(error), file=sys.stderr)
    sys.exit(1)
