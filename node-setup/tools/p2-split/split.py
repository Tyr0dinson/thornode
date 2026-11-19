#!/usr/bin/env python3
"""Migration helper: if you started with one consolidated adapter (Deployment
`adapter`) serving every chain, this creates one adapter per chain beside it.

Run with kubectl access to the cluster. The object names used here (ConfigMap
`adapter`, Secret `adapter-targets`, NetworkPolicy `adapter`, the `gateway`
objects) must match your deployment.

  split.py backup              dump current router objects to the backup dir
  split.py render <chain>...   print the manifests
  split.py apply  <chain>...   create the per-chain objects; changes no traffic

For each chain it creates, in the router namespace (ROUTER_NAMESPACE):
  (no Secret)                           the pod mounts only that chain's files
                                        from the existing Secret `adapter-targets`
                                        (projected by key); no Secret is read
  ConfigMap   adapter-<chain>           the chain's block of ConfigMap `adapter`,
                                        default = the route selected on the shared adapter
  PVC         adapter-<chain>-state
  Job         adapter-<chain>-initialize
  Deployment  adapter-<chain>
  Service     adapter-<chain>
and once: NetworkPolicy `chain-adapter`, ServiceMonitor `chain-adapter`, and a
`gateway` NetworkPolicy egress rule to the new pods.

Nothing here touches P1's config or routes, the shared `adapter`, or Bifrost.
Per-chain overrides (edit OVERRIDES) are applied on top of the copied block.
"""
import copy
import json
import os
import subprocess
import sys
import time

NS = os.environ.get("ROUTER_NAMESPACE", "rpc-router")
BACKUP = os.path.expanduser(os.environ.get("P2_SPLIT_BACKUP", "~/p2-split-backup"))
# Set to the image you built from ../../bifrost-rpc-router (pin it by digest).
IMAGE = os.environ.get("ROUTER_IMAGE", "localhost/bifrost-rpc-router:REPLACE_WITH_YOUR_BUILD")
NAME = {"app.kubernetes.io/name": "rpc-router"}
COMPONENT = "chain-adapter"
MEMORY = {}  # chain -> limit; default 512Mi. UTXO chains with utxo_ahead_ms need about 1Gi.
# Example: start one chain's own adapter on a different backend with fetch-ahead on.
# OVERRIDES = {"<chain>": {"default": "<backend>", "head_cache_ms": 1000, "tip_ahead": True}}
OVERRIDES = {}


def kubectl(*args, data=None, check=True):
    p = subprocess.run(["kubectl", *args], input=data, text=True, capture_output=True)
    if check and p.returncode:
        raise SystemExit("kubectl failed: " + " ".join(args[:6]) + "\n" + p.stderr.strip()[-400:])
    return p.stdout


def get(kind, name, namespace=NS):
    return json.loads(kubectl("-n", namespace, "get", kind, name, "-o", "json"))


def labels(chain):
    return {**NAME, "app.kubernetes.io/component": COMPONENT, "rpc-router/chain": chain}


def shared_routes():
    pod = kubectl("-n", NS, "get", "pod", "-l", "app.kubernetes.io/component=adapter",
                  "--field-selector=status.phase=Running", "-o", "name").split()[0]
    out = kubectl("-n", NS, "exec", pod, "--", "/bifrost-rpc-router", "-socket",
                  "/runtime/private/control.sock", "-command", "status")
    return json.loads(out)["routes"]


def pod_spec(chain, args, memory, keys):
    name = f"adapter-{chain}"
    return {
        "automountServiceAccountToken": False,
        "securityContext": {"fsGroup": 10001, "fsGroupChangePolicy": "OnRootMismatch", "runAsGroup": 10001,
                            "runAsNonRoot": True, "runAsUser": 10001, "seccompProfile": {"type": "RuntimeDefault"}},
        "containers": [{
            "name": "router", "image": IMAGE, "imagePullPolicy": "Never", "args": args,
            "resources": {"limits": {"cpu": "2", "memory": memory}, "requests": {"cpu": "20m", "memory": "32Mi"}},
            "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]},
                                "readOnlyRootFilesystem": True},
            "volumeMounts": [
                {"mountPath": "/etc/router/config", "name": "config", "readOnly": True},
                {"mountPath": "/etc/router/targets", "name": "targets", "readOnly": True},
                {"mountPath": "/state", "name": "state"},
                {"mountPath": "/runtime", "name": "runtime"}],
        }],
        "volumes": [
            {"name": "config", "configMap": {"name": name, "defaultMode": 420}},
            {"name": "targets", "secret": {"secretName": "adapter-targets", "defaultMode": 288,
                                           "items": [{"key": k, "path": k} for k in keys]}},
            {"name": "state", "persistentVolumeClaim": {"claimName": f"{name}-state"}},
            {"name": "runtime", "emptyDir": {"medium": "Memory", "sizeLimit": "1Mi"}}],
    }


def chain_objects(chain, shared_cfg, routes):
    name = f"adapter-{chain}"
    lab = labels(chain)
    block = copy.deepcopy(shared_cfg["chains"][chain])
    block["default"] = routes[chain]
    block.update(OVERRIDES.get(chain, {}))
    if block["default"] not in block["backends"]:
        raise SystemExit(f"{chain}: default {block['default']!r} is not one of its backends")
    cfg = {k: shared_cfg[k] for k in ("listen", "admin_socket", "state_file")}
    cfg["chains"] = {chain: block}

    keys = sorted(os.path.basename(f) for b in block["backends"].values() for f in b.values())
    meta = lambda n: {"name": n, "namespace": NS, "labels": lab}
    memory = MEMORY.get(chain, "512Mi")

    configmap = {"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta(name),
                 "data": {"config.json": json.dumps(cfg, indent=2) + "\n"}}
    pvc = {"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": meta(f"{name}-state"),
           "spec": {"accessModes": ["ReadWriteOnce"], "resources": {"requests": {"storage": "128Mi"}}}}
    if os.environ.get("ROUTER_STORAGE_CLASS"):
        pvc["spec"]["storageClassName"] = os.environ["ROUTER_STORAGE_CLASS"]
    init_spec = pod_spec(chain, ["-config", "/etc/router/config/config.json", "-command", "init"], memory, keys)
    init_spec["restartPolicy"] = "Never"
    job = {"apiVersion": "batch/v1", "kind": "Job", "metadata": meta(f"{name}-initialize"),
           "spec": {"backoffLimit": 0, "template": {
               "metadata": {"labels": {"app.kubernetes.io/name": "rpc-router-init", "rpc-router/chain": chain}},
               "spec": init_spec}}}
    serve = pod_spec(chain, ["-config", "/etc/router/config/config.json"], memory, keys)
    serve["terminationGracePeriodSeconds"] = 60
    c = serve["containers"][0]
    c["ports"] = [{"containerPort": 8080, "name": "http", "protocol": "TCP"}]
    probe = lambda path, **kw: {"httpGet": {"path": path, "port": "http"}, **kw}
    c["startupProbe"] = probe("/healthz", periodSeconds=2, failureThreshold=30, timeoutSeconds=1)
    c["readinessProbe"] = probe("/readyz", periodSeconds=5, failureThreshold=3, timeoutSeconds=2)
    c["livenessProbe"] = probe("/healthz", periodSeconds=15, failureThreshold=3, timeoutSeconds=2)
    deploy = {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta(name),
              "spec": {"replicas": 1, "strategy": {"type": "Recreate"}, "selector": {"matchLabels": lab},
                       "template": {"metadata": {"labels": lab}, "spec": serve}}}
    service = {"apiVersion": "v1", "kind": "Service", "metadata": meta(name),
               "spec": {"selector": lab, "ports": [{"name": "http", "port": 8080, "protocol": "TCP", "targetPort": "http"}]}}
    return configmap, pvc, job, deploy, service


def common_objects():
    sel = {**NAME, "app.kubernetes.io/component": COMPONENT}
    old = get("networkpolicy", "adapter")["spec"]
    policy = {"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
              "metadata": {"name": COMPONENT, "namespace": NS, "labels": sel},
              "spec": {**old, "podSelector": {"matchLabels": sel}}}
    monitor = {"apiVersion": "monitoring.coreos.com/v1", "kind": "ServiceMonitor",
               "metadata": {"name": COMPONENT, "namespace": NS, "labels": {"release": "prometheus"}},
               "spec": {**get("servicemonitor", "adapter")["spec"], "selector": {"matchLabels": sel}}}
    return policy, monitor


def allow_gateway_egress():
    spec = get("networkpolicy", "gateway")["spec"]
    peer = {"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": NS}},
            "podSelector": {"matchLabels": {**NAME, "app.kubernetes.io/component": COMPONENT}}}
    for rule in spec["egress"]:
        if any(p.get("port") == 8080 for p in rule.get("ports", [])):
            if peer in rule["to"]:
                return "gateway egress to chain-adapter pods already allowed"
            rule["to"].append(peer)
            kubectl("-n", NS, "patch", "networkpolicy", "gateway", "--type=merge", "--patch-file=/dev/stdin",
                    data=json.dumps({"spec": {"egress": spec["egress"]}}))
            return "gateway egress to chain-adapter pods added"
    raise SystemExit("gateway NetworkPolicy has no port-8080 egress rule")


def backup():
    os.makedirs(BACKUP, mode=0o700, exist_ok=True)
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    for kind in ("deployment", "configmap", "service", "networkpolicy", "servicemonitor"):
        path = os.path.join(BACKUP, f"{kind}.{stamp}.json")
        with open(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as f:
            f.write(kubectl("-n", NS, "get", kind, "-o", "json"))
    print("backup written to", BACKUP, "at", stamp)


def main():
    if len(sys.argv) < 2 or sys.argv[1] not in ("backup", "render", "apply"):
        raise SystemExit(__doc__)
    if sys.argv[1] == "backup":
        return backup()
    chains = sys.argv[2:]
    shared_cfg = json.loads(get("configmap", "adapter")["data"]["config.json"])
    routes = shared_routes()
    unknown = [c for c in chains if c not in shared_cfg["chains"]]
    if unknown or not chains:
        raise SystemExit(f"unknown or missing chains: {unknown}; known: {sorted(shared_cfg['chains'])}")
    per_chain = {c: chain_objects(c, shared_cfg, routes) for c in chains}

    if sys.argv[1] == "render":
        for objs in per_chain.values():
            for o in objs:
                print(json.dumps(o, indent=1))
        return

    for o in common_objects():
        print(kubectl("apply", "-f", "-", data=json.dumps(o)).strip())
    print(allow_gateway_egress())
    for chain, (configmap, pvc, job, deploy, service) in per_chain.items():
        for o in (configmap, pvc):
            print(kubectl("apply", "-f", "-", data=json.dumps(o)).strip())
        if kubectl("-n", NS, "get", "job", job["metadata"]["name"], "-o", "name", check=False).strip():
            print(f"job {job['metadata']['name']} exists; not re-run")
        else:
            print(kubectl("apply", "-f", "-", data=json.dumps(job)).strip())
            kubectl("-n", NS, "wait", "--for=condition=complete", "--timeout=120s", "job/" + job["metadata"]["name"])
            print(f"  {chain}: state initialised")
        for o in (deploy, service):
            print(kubectl("apply", "-f", "-", data=json.dumps(o)).strip())
    for chain in chains:
        kubectl("-n", NS, "rollout", "status", f"deployment/adapter-{chain}", "--timeout=180s")
        print(f"adapter-{chain}: ready")


if __name__ == "__main__":
    main()
