#!/usr/bin/env python3
"""Exercise THORNode's FIRO finality adapter using Core's real quorum harness.

make test-firo-integration prepares the framework and official daemon in Docker.
For native debugging, set FIRO_SOURCE to the checkout and FIROD to the daemon.
No consensus patches or synthetic ChainLock RPC fields are used.
"""
import os
import subprocess
import sys
import time
from decimal import Decimal
from pathlib import Path
from urllib.parse import quote, urlsplit

source = os.environ.get("FIRO_SOURCE")
if not source:
    raise SystemExit("Set FIRO_SOURCE to a FIRO v0.14.18.2 source checkout")
sys.path.insert(0, str(Path(source) / "qa/rpc-tests"))
# The upstream framework can only be imported after FIRO_SOURCE is on sys.path.
# isort: off
from test_framework.test_framework import EvoZnodeTestFramework  # noqa: E402
from test_framework.util import (  # noqa: E402
    get_mocktime,
    isolate_node,
    reconnect_isolated_node,
    rpc_url,
    set_mocktime,
    set_node_times,
)

# isort: on

ROOT = Path(__file__).resolve().parents[3]


class ThornodeChainLocks(EvoZnodeTestFramework):
    def __init__(self):
        super().__init__(
            6, 5, extra_args=[["-txindex=1", "-dandelion=0"] for _ in range(6)]
        )

    def check_client(self, locked, healthy, deposit=None):
        env = os.environ.copy()
        endpoint = urlsplit(rpc_url(0))
        escaped = "http://{}:{}@{}:{}".format(
            quote(endpoint.username, safe=""),
            quote(endpoint.password, safe=""),
            endpoint.hostname,
            endpoint.port,
        )
        env.update(
            FIRO_TEST_RPC_URL=escaped,
            FIRO_TEST_LOCKED_HEIGHT=str(locked),
            FIRO_TEST_HEALTHY=str(healthy).lower(),
        )
        if deposit:
            env.update(
                FIRO_TEST_DEPOSIT=deposit,
                FIRO_TEST_OUTBOUND_FILE=self.options.tmpdir + "/outbound.txt",
            )
        subprocess.run(
            [
                "go",
                "test",
                "-mod=readonly",
                "-tags",
                "mocknet",
                "-count=1",
                "./bifrost/pkg/chainclients/utxo",
                "-run",
                "^TestFiroLiveChainLocks$",
            ],
            cwd=ROOT,
            env=env,
            check=True,
            timeout=180,
        )

    def wait_locked(self, node, expected):
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            block = node.getblock(expected)
            if block["confirmations"] > 0 and block["chainlock"]:
                return
            time.sleep(0.1)
        raise AssertionError("real quorum did not ChainLock the expected block")

    def run_test(self):
        self.check_client(-1, False)
        for _ in range(4):
            self.mine_quorum()
        self.nodes[0].generate(1)
        self.sync_all()
        self.wait_locked(self.nodes[0], self.nodes[0].getbestblockhash())
        locked = self.nodes[0].getblockcount()
        self.check_client(locked, True)

        # The isolated observer/miner cannot assemble a quorum. PoW growth
        # must not advance the adapter's finalized scan cursor.
        isolate_node(self.nodes[0])
        set_mocktime(get_mocktime() + 1)
        set_node_times(self.nodes, get_mocktime())
        self.nodes[0].generate(2)
        self.check_client(locked, False)

        reconnect_isolated_node(self.nodes[0], 1)
        self.sync_all()
        self.nodes[1].generate(1)
        self.sync_all()
        self.wait_locked(self.nodes[0], self.nodes[1].getbestblockhash())
        self.check_client(self.nodes[0].getblockcount(), True)

        # Activate exchange outputs and fund a P2PKH sender (coinbase inputs
        # use bare pubkeys, which are deliberately outside deposit attribution).
        node = self.nodes[0]
        if node.getblockcount() < 1001:
            node.generate(1001 - node.getblockcount())
            self.sync_all()
        sender = node.getnewaddress()
        funding = node.sendtoaddress(sender, 2)
        self.mine_payment(funding)
        raw = node.getrawtransaction(funding, True)
        index = next(
            out["n"]
            for out in raw["vout"]
            if sender in out["scriptPubKey"].get("addresses", [])
        )
        vault = "EXRP4znv9uRtZUBKewXrviobo7eNcgwUb87C"
        transaction = node.createrawtransaction(
            [{"txid": funding, "vout": index}],
            {vault: Decimal("1"), sender: Decimal("0.9999"), "data": "4e4f4f50"},
        )
        signed = node.signrawtransaction(transaction)
        assert signed["complete"]
        deposit = node.sendrawtransaction(signed["hex"])
        self.mine_payment(deposit)
        self.check_client(node.getblockcount(), True, deposit)
        outbound = Path(self.options.tmpdir + "/outbound.txt").read_text()
        self.mine_payment(outbound)
        assert node.getrawtransaction(outbound, True)["chainlock"]

    def mine_payment(self, txid):
        self.sync_all()
        set_mocktime(get_mocktime() + 601)
        set_node_times(self.nodes, get_mocktime())
        self.nodes[0].generate(1)
        self.sync_all()
        assert self.nodes[0].getrawtransaction(txid, True).get("confirmations", 0) > 0
        self.wait_locked(self.nodes[0], self.nodes[0].getbestblockhash())


if __name__ == "__main__":
    ThornodeChainLocks().main()
