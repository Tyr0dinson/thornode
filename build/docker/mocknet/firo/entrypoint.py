#!/usr/bin/env python3
"""Persistent FIRO regtest miner and five real LLMQ masternodes.

Only for local mocknet: fixed RPC credentials, accelerated regtest time, and
wallet-owned collateral. No consensus flags or fabricated ChainLock responses.
"""
import base64
import json
import os
import signal
import socket
import subprocess
import threading
import time
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

MASTER_ADDRESS = "EXRKTVjv4YXSCYrX6C6or4WS2Sg1wWoKX5LK"
STOP = threading.Event()


class RPCError(RuntimeError):
    pass


def rpc(endpoint, method, *params):
    auth = base64.b64encode(
        (
            os.getenv("SIGNER_NAME", "thorchain")
            + ":"
            + os.getenv("SIGNER_PASSWD", "password")
        ).encode()
    ).decode()
    request = Request(
        endpoint,
        json.dumps(
            {"jsonrpc": "1.0", "id": "mocknet", "method": method, "params": params}
        ).encode(),
        {"Authorization": "Basic " + auth, "Content-Type": "application/json"},
    )
    try:
        response = urlopen(request, timeout=30)
    except HTTPError as error:
        response = error
    with response:
        result = json.load(response)
    if result.get("error"):
        raise RPCError(f"{method}: {result['error']}")
    return result["result"]


def atomic_json(path, value):
    temporary = path.with_suffix(".tmp")
    with temporary.open("w") as output:
        os.chmod(temporary, 0o600)
        json.dump(value, output)
        output.flush()
        os.fsync(output.fileno())
    temporary.replace(path)


def wait_for(check, description, timeout=180):
    deadline = time.monotonic() + timeout
    last_error = None
    while not STOP.is_set():
        try:
            value = check()
            if value:
                return value
        except (OSError, ValueError, RPCError) as error:
            last_error = error
        if time.monotonic() >= deadline:
            raise RuntimeError(f"Timed out waiting for {description}: {last_error}")
        STOP.wait(0.2)
    raise InterruptedError("stopping")


class Mocknet:
    def __init__(self):
        self.index = int(os.getenv("FIRO_NODE", "1")) - 1
        # Core ignores private externalip values and falls back to loopback for
        # the regtest masternode identity. Compose shares one network namespace,
        # just like the native tests, so registered addresses match that identity.
        self.nodes = json.loads(
            os.getenv(
                "FIRO_NODES",
                json.dumps(
                    [
                        {"host": "127.0.0.1", "p2p": 18444 + i, "rpc": 28888 + i}
                        for i in range(6)
                    ]
                ),
            )
        )
        if len(self.nodes) != 6 or not 0 <= self.index < 6:
            raise ValueError("FIRO mocknet requires six nodes, FIRO_NODE=1..6")
        self.data = Path(os.getenv("FIRO_DATA_DIR", "/data"))
        self.shared = Path(os.getenv("FIRO_QUORUM_DIR", "/quorum"))
        self.data.mkdir(parents=True, exist_ok=True)
        self.shared.mkdir(parents=True, exist_ok=True)
        self.state_path = self.shared / "state.json"
        self.ready_path = self.shared / "ready.json"
        self.state = None
        self.process = None
        self.interval = float(os.getenv("BLOCK_TIME", "12"))
        if self.interval <= 0:
            raise ValueError("BLOCK_TIME must be positive")
        self.master = os.getenv("FIRO_MASTER_ADDR", MASTER_ADDRESS)
        self.funding = int(os.getenv("FIRO_MASTER_AMOUNT", "10000"))
        if self.funding <= 0:
            raise ValueError("FIRO_MASTER_AMOUNT must be positive")

    def call(self, index, method, *params):
        node = self.nodes[index]
        return rpc(f"http://{node['host']}:{node['rpc']}", method, *params)

    def save(self):
        atomic_json(self.state_path, self.state)

    def start(self):
        if self.index:

            def provisioned():
                if not self.state_path.exists():
                    return False
                state = json.loads(self.state_path.read_text())
                return state if str(self.index) in state["members"] else False

            self.state = wait_for(provisioned, "masternode provisioning", timeout=600)
        else:
            self.ready_path.unlink(missing_ok=True)
            if self.state_path.exists():
                self.state = json.loads(self.state_path.read_text())
            else:
                # Refuse to silently pair an existing wallet/chain with lost keys.
                if (self.data / "regtest" / "blocks").exists():
                    raise RuntimeError(
                        "Missing quorum state for existing chain; restore or reset all FIRO volumes"
                    )
                self.state = {"version": 1, "clock": int(time.time()), "members": {}}
                self.save()
        if self.state["version"] != 1:
            raise ValueError("Unsupported FIRO mocknet state version")
        node = self.nodes[self.index]
        settings = [
            "regtest=1",
            "server=1",
            "txindex=1",
            "prune=0",
            "disablewallet=0",
            "discover=0",
            "dnsseed=0",
            "listenonion=0",
            "dandelion=0",
            "usemnemonic=0",
            "createwalletbackups=0",
            "minrelaytxfee=0.00001",
            "rpcworkqueue=256",
            "rpcbind=0.0.0.0",
            "rpcallowip=0.0.0.0/0",
            "printtoconsole=0",
            f"rpcuser={os.getenv('SIGNER_NAME', 'thorchain')}",
            f"rpcpassword={os.getenv('SIGNER_PASSWD', 'password')}",
            f"rpcport={node['rpc']}",
            f"port={node['p2p']}",
            f"externalip={socket.gethostbyname(node['host'])}:{node['p2p']}",
            f"mocktime={self.state['clock']}",
        ]
        if self.index:
            settings += [
                "znode=1",
                "znodeblsprivkey=" + self.state["members"][str(self.index)]["secret"],
            ]
        for i, peer in enumerate(self.nodes):
            if i != self.index:
                settings.append(f"addnode={peer['host']}:{peer['p2p']}")
        config = self.data / "firo.conf"
        config.write_text("\n".join(settings) + "\n")
        config.chmod(0o600)
        self.process = subprocess.Popen(
            [os.getenv("FIROD", "firod"), f"-datadir={self.data}"]
        )

        def running():
            if self.process.poll() is not None:
                raise RuntimeError(
                    f"firod exited ({self.process.returncode}); see {self.data}/regtest/debug.log"
                )
            return self.call(self.index, "getblockchaininfo")

        info = wait_for(running, "FIRO RPC")
        if info["chain"] != "regtest" or info["pruned"]:
            raise RuntimeError("Mocknet requires unpruned regtest")
        print(
            f"FIRO node {self.index+1} started at height {info['blocks']}", flush=True
        )
        return info["blocks"]

    def tick(self, seconds=1, nodes=None):
        self.state["clock"] += seconds
        self.save()
        for i in nodes if nodes is not None else range(6):

            def set_clock():
                # A member may restart after the RPC readiness probe to refresh
                # its masternode handshake. Retry this idempotent update with the
                # same saved timestamp, even if only the RPC response was lost.
                self.call(i, "setmocktime", self.state["clock"])
                return True  # setmocktime itself returns JSON null

            wait_for(set_clock, f"node {i+1} clock synchronization")

    def mine(self, count=1, nodes=None, seconds=1):
        if nodes is None:
            # Peers must see transactions before accelerated time advances;
            # otherwise their ten-minute safety timer starts at block arrival.
            pending = set(self.call(0, "getrawmempool"))
            if pending:
                wait_for(
                    lambda: all(
                        pending.issubset(self.call(i, "getrawmempool"))
                        for i in range(1, 6)
                    ),
                    "transaction relay",
                )
        self.tick(seconds, nodes)
        result = self.call(0, "generate", count)
        if nodes is None:
            self.sync()
        return result[-1]

    def sync(self):
        best = self.call(0, "getbestblockhash")
        wait_for(
            lambda: all(self.call(i, "getbestblockhash") == best for i in range(1, 6)),
            "block sync",
        )

    def sync_znodes(self):
        for i in range(6):

            def synced():
                if self.call(i, "evoznsync", "status")["IsSynced"]:
                    return True
                self.call(i, "evoznsync", "next")
                return False

            wait_for(synced, f"node {i+1} masternode sync")

    def prepare(self):
        # Registered collateral and the funding transaction are owned by node 1.
        # Mine before registration so all required network upgrades are active.
        height = self.call(0, "getblockcount")
        if height < 1001:
            self.mine(1001 - height, nodes=[0])
        if self.state["members"]:
            self.mine(nodes=[0], seconds=601)  # finish an interrupted registration
        for i in range(1, 6):
            key = str(i)
            if key not in self.state["members"]:
                bls = self.call(0, "bls", "generate")
                self.state["members"][key] = {
                    **bls,
                    "owner": self.call(0, "getnewaddress"),
                    "collateral": self.call(0, "getnewaddress"),
                    "payout": self.call(0, "getnewaddress"),
                }
                self.save()  # retain operator keys even if registration RPC is interrupted
            member = self.state["members"][key]
            registrations = self.call(0, "protx", "list", "registered", True)
            registered = next(
                (
                    item
                    for item in registrations
                    if item["state"]["pubKeyOperator"] == member["public"]
                ),
                None,
            )
            service = (
                f"{socket.gethostbyname(self.nodes[i]['host'])}:{self.nodes[i]['p2p']}"
            )
            if not registered:
                if member.get("proTxHash"):
                    raise RuntimeError(
                        "Saved registration missing from chain; restore or reset all FIRO volumes"
                    )
                while self.call(0, "getbalance") < 1001:
                    self.mine(100, nodes=[0])
                # FIRO's register_fund takes coins from the payout address
                # unless an explicit fee source is supplied.
                if self.call(0, "getreceivedbyaddress", member["payout"], 0) < 1001:
                    self.call(0, "sendtoaddress", member["payout"], 1001)
                    self.mine(nodes=[0], seconds=601)
                txid = self.call(
                    0,
                    "protx",
                    "register_fund",
                    member["collateral"],
                    service,
                    member["owner"],
                    member["public"],
                    member["owner"],
                    0,
                    member["payout"],
                )
                member["proTxHash"] = txid
                self.save()
                self.mine(nodes=[0], seconds=601)
            else:
                member["proTxHash"] = registered["proTxHash"]
                if (
                    registered["state"]["service"] != service
                    or registered["state"].get("PoSeBanHeight", -1) != -1
                ):
                    self.call(
                        0,
                        "protx",
                        "update_service",
                        member["proTxHash"],
                        service,
                        member["secret"],
                    )
                    self.mine(nodes=[0], seconds=601)
                self.save()
        for i in range(1, 6):
            wait_for(
                lambda i=i: self.call(i, "getblockchaininfo"),
                f"node {i+1} RPC",
                timeout=600,
            )
        self.tick()
        self.sync()
        self.sync_znodes()
        self.mine()
        print("Five masternodes registered and synced", flush=True)

    def wait_phase(self, phase, field=None):
        pending = []

        def reached():
            pending.clear()
            for i in range(1, 6):
                sessions = self.call(i, "quorum", "dkgstatus")["session"]
                session = sessions.get("llmq_5_60", {})
                if session.get("phase") != phase or (
                    field and session.get(field, 0) < 5
                ):
                    status = self.call(i, "evoznode", "status")
                    pending.append(
                        f"node {i+1}: phase={session.get('phase')}, "
                        f"{field}={session.get(field, 0) if field else '-'}, "
                        f"state={status.get('state')}, status={status.get('status')}"
                    )
            return not pending

        try:
            wait_for(reached, f"DKG phase {phase}")
        except RuntimeError as error:
            raise RuntimeError(f"{error}; {'; '.join(pending)}") from error

    def quorum(self):
        self.mine(24 - self.call(0, "getblockcount") % 24)
        self.wait_phase(1)
        STOP.wait(2)  # quorum peers finish their handshake
        for phase, field in [
            (2, "receivedContributions"),
            (3, None),
            (4, None),
            (5, "receivedPrematureCommitments"),
            (6, None),
        ]:
            self.mine(2)
            self.wait_phase(phase, field)

        def committed():
            status = self.call(0, "quorum", "dkgstatus")
            return bool(status.get("minableCommitments", {}).get("llmq_5_60"))

        wait_for(committed, "quorum commitment")
        self.mine()
        print(f"Quorum formed at height {self.call(0, 'getblockcount')}", flush=True)

    def locked(self, block):
        return all(
            self.call(i, "getblockheader", block).get("chainlock", False)
            for i in range(6)
        )

    def fund_master(self):
        saved = self.state.get("funding")
        if saved and (
            saved["address"] != self.master or saved["amount"] != self.funding
        ):
            raise RuntimeError(
                "Funding settings changed; reset FIRO volumes for a different simulation master"
            )
        self.call(0, "importaddress", self.master, "simulation-master", False)
        if not saved:
            while self.call(0, "getbalance") < self.funding + 1:
                # Continue complete DKG rounds while accumulating mature coins.
                self.quorum()
            # Persist signed bytes before sending. A restart rebroadcasts the
            # same transaction instead of funding the master twice.
            raw = self.call(0, "createrawtransaction", [], {self.master: self.funding})
            funded = self.call(0, "fundrawtransaction", raw)
            signed = self.call(0, "signrawtransaction", funded["hex"])
            if not signed["complete"]:
                raise RuntimeError("Could not sign simulation funding")
            decoded = self.call(0, "decoderawtransaction", signed["hex"])
            saved = {
                "address": self.master,
                "amount": self.funding,
                "raw": signed["hex"],
                "txid": decoded["txid"],
            }
            self.state["funding"] = saved
            self.save()
        try:
            tx = self.call(0, "getrawtransaction", saved["txid"], True)
        except RPCError:
            self.call(0, "sendrawtransaction", saved["raw"])
            tx = self.call(0, "getrawtransaction", saved["txid"], True)
        if not tx.get("chainlock", False):
            block = self.mine(seconds=601)
            wait_for(lambda: self.locked(block), "funding ChainLock")
        tx = self.call(0, "getrawtransaction", saved["txid"], True)
        if not tx.get("chainlock", False):
            raise RuntimeError("Simulation funding is not ChainLocked")
        print(
            f"Simulation master funded: {self.master}, txid {saved['txid']}", flush=True
        )

    def ready(self):
        atomic_json(
            self.ready_path,
            {
                "updated": time.time(),
                "max_age": max(60, 3 * self.interval),
                "height": self.call(0, "getblockcount"),
                "funding_txid": self.state["funding"]["txid"],
            },
        )

    def run(self):
        height = self.start()
        if self.index:
            if (
                height == 0
                or self.call(self.index, "evoznode", "status").get("state") != "READY"
            ):
                # Fresh nodes and nodes whose registered service address changed
                # connect before they know their on-chain identity. Reconnect once
                # the controller's registration/update is confirmed so peers get
                # masternode authentication during the new connection handshake.
                wait_for(
                    lambda: self.call(self.index, "evoznode", "status").get("state")
                    == "READY",
                    "registered masternode identity",
                    timeout=600,
                )
                self.close()
                self.start()
            while not STOP.wait(1):
                if self.process.poll() is not None:
                    raise RuntimeError("Masternode daemon exited")
            return
        self.prepare()
        for _ in range(4):
            self.quorum()
        block = self.mine()
        wait_for(lambda: self.locked(block), "first ChainLock")
        self.fund_master()
        self.ready()
        print(f"FIRO mocknet ready; mining every {self.interval:g}s", flush=True)
        while not STOP.wait(self.interval):
            if self.process.poll() is not None:
                raise RuntimeError("Mining daemon exited")
            try:
                self.sync()
                # Regtest time advances together so non-InstantSend transactions
                # pass Core's ten-minute mining delay without slowing simulation.
                block = self.mine(seconds=601)
                wait_for(lambda: self.locked(block), "ChainLock", timeout=60)
                self.ready()
            except (OSError, RPCError, RuntimeError) as error:
                self.ready_path.unlink(missing_ok=True)
                if self.process.poll() is not None:
                    raise RuntimeError("Mining daemon exited") from error
                print(f"Mining paused: {error}", flush=True)
                # On daemon/peer recovery, resync before the next block.

    def close(self):
        if not self.index:
            self.ready_path.unlink(missing_ok=True)
        if self.process is not None and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=45)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()


def main():
    signal.signal(signal.SIGTERM, lambda *_: STOP.set())
    signal.signal(signal.SIGINT, lambda *_: STOP.set())
    net = Mocknet()
    try:
        net.run()
    except InterruptedError:
        pass
    finally:
        net.close()


if __name__ == "__main__":
    main()
