#!/usr/bin/env python3
"""
chainprobe — real-chain-balance solvency probe for the ADR-028 dry-run.

Compares THORChain's *recorded* vault holdings (TC accounting, from the THORNode
API) against the *actual* on-chain balances of the vault, to surface phantom
holdings — TC claims an asset the chain no longer holds, i.e. an exploit-drained
vault whose keeper record was never reconciled (the ADR-028 signature).

EVM ACCOUNTING MODEL (critical — got this wrong the naive way first):
  A THORChain EVM vault is an EOA that holds ONLY the chain's NATIVE coin
  (ETH/AVAX/BNB). Every ERC20 balance is held inside the THORChain ROUTER
  contract and tracked per-vault via `vaultAllowance(vault, token)`. So:
    - native  → eth_getBalance(vaultEOA)
    - ERC20   → router.vaultAllowance(vaultEOA, tokenContract)   (NOT balanceOf!)
  Querying balanceOf(vaultEOA) for an ERC20 always returns ~0 and would wrongly
  report every token as "drained".

WHY NOT bifrost's chain clients: GetAccountByAddress is a no-op stub on the UTXO
client, and the EVM/cosmos/tron clients only construct with a full bifrost
runtime (live ThorchainBridge + TSS + keys + managers) — too heavy to embed in
the offline dry-run for a read-only balance. The EVM read is a trivial eth_call,
so we issue it directly. EVM chains only (AVAX/ETH/BSC/BASE); UTXO/GAIA/TRON and
XRP (sdaemons has an xrp-daemon) are future work; SOL has no daemon exposed.

SETUP — use the tc-stagenet `sdaemons` namespace, NOT valle/tdaemons:
  Stagenet THORChain observes the MAINNET L1 chains, so sdaemons hold mainnet
  data — and they are SEPARATE from the daemons mainnet validators rely on for
  consensus, so probing them does not load consensus-critical infrastructure.
  (Do NOT use valle/tdaemons: those ARE the mainnet validator daemons.)
  kubectl port-forward --context tc-stagenet -n sdaemons svc/avalanche-daemon     19650:9650
  kubectl port-forward --context tc-stagenet -n sdaemons svc/ethereum-daemon      18546:8545
  kubectl port-forward --context tc-stagenet -n sdaemons svc/binance-smart-daemon 18547:8545
  # BASE has no sdaemons daemon — bifrost uses a QuickNode endpoint. Pull it at runtime:
  #   BASE_RPC=$(kubectl exec -n stagenet-v1 --context tc-stagenet bifrost-0 -- \
  #             printenv BIFROST_CHAINS_BASE_RPC_HOST)
  #   (it is credentialed — keep the URL OUT of committed files and logs.)
  # xrp-daemon is available in sdaemons (51234) for future XRP support
USAGE:
  THORNODE_API=http://100.64.0.4:31317 ./chainprobe.py
"""
import json
import os
import sys
import urllib.request

EVM_CHAINS = {
    "AVAX": os.environ.get("AVAX_RPC", "http://localhost:19650/ext/bc/C/rpc"),
    "ETH":  os.environ.get("ETH_RPC",  "http://localhost:18546"),
    "BSC":  os.environ.get("BSC_RPC",  "http://localhost:18547"),
    "BASE": os.environ.get("BASE_RPC", "http://localhost:18545"),
}
THORNODE = os.environ.get("THORNODE_API", "http://100.64.0.4:31317")
ONE_E8 = 10**8
VAULT_ALLOWANCE = "0x03b6a673"  # keccak("vaultAllowance(address,address)")[:4]
BALANCE_OF = "0x70a08231"       # keccak("balanceOf(address)")[:4]
DECIMALS = "0x313ce567"         # keccak("decimals()")[:4]


def _rpc(url, method, params):
    req = urllib.request.Request(
        url, data=json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode(),
        headers={"content-type": "application/json"})
    return json.load(urllib.request.urlopen(req, timeout=25)).get("result")


def _get(url):
    return json.load(urllib.request.urlopen(url, timeout=20))


def _call(url, to, data):
    return _rpc(url, "eth_call", [{"to": to, "data": data}, "latest"])


def _pad(addr):
    return addr[2:].rjust(64, "0")


def reachable(url):
    try:
        _rpc(url, "eth_chainId", [])
        return True
    except Exception:
        return False


def main():
    live = {c: u for c, u in EVM_CHAINS.items() if reachable(u)}
    down = sorted(set(EVM_CHAINS) - set(live))
    # routers + halted flag per chain
    inbound = {x["chain"]: x for x in _get(f"{THORNODE}/thorchain/inbound_addresses")}
    routers = {c: inbound.get(c, {}).get("router") for c in live}
    vaults = _get(f"{THORNODE}/thorchain/vaults/asgard")
    active = [v for v in vaults if v.get("status") == "ActiveVault"]

    print(f"THORNode: {THORNODE} | active asgard vaults: {len(active)}")
    print(f"EVM reachable: {sorted(live)}" + (f" | UNREACHABLE: {down}" if down else ""))
    for c in sorted(live):
        print(f"  {c}: router={routers[c]} halted={inbound.get(c, {}).get('halted')}")
    print()

    dec_cache = {}

    def decimals(url, contract):
        key = (url, contract.lower())
        if key not in dec_cache:
            dec_cache[key] = int(_call(url, contract, DECIMALS), 16)
        return dec_cache[key]

    shorts = []          # (vault, asset, tc_e8, real_e8)
    print(f"{'vault':10} {'asset':56} {'TC':>16} {'REAL on-chain':>16}  verdict")
    for v in active:
        short = v.get("pub_key", "")[-6:]
        addrs = {a["chain"]: a["address"] for a in (v.get("addresses") or [])}
        for c in (v.get("coins") or []):
            asset = c["asset"]
            chain = asset.split(".", 1)[0]
            if chain not in live:
                continue
            tc_e8 = int(c["amount"])
            eoa = addrs.get(chain)
            url = live[chain]
            try:
                if "-" not in asset:  # native
                    real = int(_rpc(url, "eth_getBalance", [eoa, "latest"]), 16)
                    real_e8 = real * ONE_E8 // (10**18)
                else:                 # ERC20 → router vaultAllowance
                    contract = asset.split("-", 1)[1].lower()
                    router = routers[chain]
                    raw = _call(url, router, VAULT_ALLOWANCE + _pad(eoa) + _pad(contract))
                    real = int(raw, 16) if raw and raw != "0x" else 0
                    real_e8 = real * ONE_E8 // (10**decimals(url, contract))
            except Exception as e:
                print(f"{short:10} {asset:56} RPC ERROR {e}")
                continue
            diff = real_e8 - tc_e8
            if tc_e8 > ONE_E8 and real_e8 * 100 < tc_e8:   # real < 1% of TC → drained
                verdict = "PHANTOM (drained)"
                shorts.append((short, asset, tc_e8, real_e8))
            elif abs(diff) > ONE_E8:
                verdict = f"short {diff/ONE_E8:,.2f}" if diff < 0 else f"extra {diff/ONE_E8:,.2f}"
            else:
                continue  # match — don't print (keep output focused on discrepancies)
            print(f"{short:10} {asset:56} {tc_e8/ONE_E8:>16,.4f} {real_e8/ONE_E8:>16,.4f}  {verdict}")

    print()
    if shorts:
        print(f"=== {len(shorts)} PHANTOM holding(s): TC records assets the chain no longer holds ===")
        print("    (per-asset, these are real exploit-drained balances still on TC's books)")
    else:
        print("=== no phantom holdings on reachable EVM chains: TC matches chain ===")


if __name__ == "__main__":
    sys.exit(main())
