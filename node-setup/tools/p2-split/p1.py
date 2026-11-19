#!/usr/bin/env python3
"""Gives the P1 gateway a `p2` backend per chain that points at the chain's own
P2 (`adapter-<chain>`). Run with kubectl access to the cluster.

  p1.py add <chain>...     add target files and backends; changes no route
  p1.py status             P1 revision, catalog digest and routes
  p1.py reload             reload P1 once the new config is in the pod
  p1.py switch <chain> <backend>

`add` writes new keys `<chain>-p2-rpc.json` into Secret `gateway-targets`. Their
content is an in-cluster service URL with no credentials, written with
stringData; the Secret is not read. It then adds backend `p2` to each chain in
ConfigMap `gateway`. Existing keys and backends are left alone, so
`switch <chain> adapter` (the consolidated adapter) or `native` is the rollback.
"""
import json
import os
import subprocess
import sys
import time

NS = os.environ.get("ROUTER_NAMESPACE", "rpc-router")
SOCK = ["/bifrost-rpc-router", "-socket", "/runtime/private/control.sock"]


def kubectl(*args, data=None, check=True):
    p = subprocess.run(["kubectl", *args], input=data, text=True, capture_output=True)
    if check and p.returncode:
        raise SystemExit("kubectl failed: " + " ".join(args[:6]) + "\n" + (p.stdout + p.stderr).strip()[-400:])
    return p.stdout


def gateway():
    return kubectl("-n", NS, "get", "pod", "-l", "app.kubernetes.io/component=gateway",
                   "--field-selector=status.phase=Running", "-o", "name").split()[0]


def status():
    return json.loads(kubectl("-n", NS, "exec", gateway(), "--", *SOCK, "-command", "status"))


def add(chains):
    cm = json.loads(kubectl("-n", NS, "get", "configmap", "gateway", "-o", "json"))
    cfg = json.loads(cm["data"]["config.json"])
    files = {}
    for chain in chains:
        block = cfg["chains"][chain]
        if list(block["endpoints"]) != ["rpc"] or block["endpoints"]["rpc"]["path"] != "/" + chain:
            raise SystemExit(f"{chain}: unexpected endpoints; not handled here")
        key = f"{chain}-p2-rpc.json"
        files[key] = json.dumps({
            "url": f"http://adapter-{chain}.{NS}.svc.cluster.local:8080/{chain}",
            "allow_http": True, "headers": {}})
        block["backends"]["p2"] = {"rpc": "/etc/router/targets/" + key}
    kubectl("-n", NS, "patch", "secret", "gateway-targets", "--type=merge", "--patch-file=/dev/stdin",
            data=json.dumps({"stringData": files}))
    print("gateway-targets: wrote", sorted(files))
    kubectl("-n", NS, "patch", "configmap", "gateway", "--type=merge", "--patch-file=/dev/stdin",
            data=json.dumps({"data": {"config.json": json.dumps(cfg, indent=2) + "\n"}}))
    print("configmap gateway: backend p2 added for", chains)


def reload():
    before = status()
    out = kubectl("-n", NS, "exec", gateway(), "--", "/bifrost-rpc-router", "-config",
                  "/etc/router/config/config.json", "-command", "check", check=False)
    print("check:", out.strip() or "FAILED (files not in the pod yet?)")
    if "valid" not in out:
        raise SystemExit(1)
    kubectl("-n", NS, "exec", gateway(), "--", *SOCK, "-command", "reload", "-revision", str(before["version"]))
    after = status()
    print("revision", before["version"], "->", after["version"], "| catalog changed:", before["catalog"] != after["catalog"])
    print("routes", after["routes"])


def switch(chain, backend):
    before = status()
    kubectl("-n", NS, "exec", gateway(), "--", *SOCK, "-command", "switch", "-chain", chain,
            "-backend", backend, "-revision", str(before["version"]))
    after = status()
    print(time.strftime("%H:%M:%S", time.gmtime()), chain, before["routes"][chain], "->", after["routes"][chain],
          "| revision", after["version"])


if __name__ == "__main__":
    cmd = sys.argv[1:2]
    if cmd == ["add"] and len(sys.argv) > 2:
        add(sys.argv[2:])
    elif cmd == ["status"]:
        print(json.dumps(status()))
    elif cmd == ["reload"]:
        reload()
    elif cmd == ["switch"] and len(sys.argv) == 4:
        switch(sys.argv[2], sys.argv[3])
    else:
        raise SystemExit(__doc__)
