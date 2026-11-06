#!/usr/bin/env python3
"""Real FIRO InstantSend: two independent clients, persistent allowance, delivery recovery.

Uses the same upstream six-node quorum and native binary as chainlocks.py.
No blocks are mined between the deposit broadcast and both early observations.
One client stores and acknowledges the observation; the other loses the handoff
and recovers it from a ChainLocked block after restarting with InstantSend disabled.
"""
import os
import subprocess
import time
from decimal import Decimal
from pathlib import Path
from urllib.parse import quote, urlsplit

# chainlocks configures the upstream framework's import path.
# isort: off
from chainlocks import ROOT, ThornodeChainLocks
from test_framework.util import rpc_url, set_mocktime, set_node_times, sync_znodes

# isort: on


class ThornodeInstantSend(ThornodeChainLocks):
    def check_instant_send(self, phase, deposit="", anchor=0):
        endpoints = []
        for i in range(2):
            endpoint = urlsplit(rpc_url(i))
            endpoints.append(
                "http://{}:{}@{}:{}".format(
                    quote(endpoint.username, safe=""),
                    quote(endpoint.password, safe=""),
                    endpoint.hostname,
                    endpoint.port,
                )
            )
        env = os.environ.copy()
        env.update(
            FIRO_IS_RPC_URLS=",".join(endpoints),
            FIRO_IS_PHASE=phase,
            FIRO_IS_DB=str(Path(self.options.tmpdir) / "bifrost"),
            FIRO_IS_DEPOSIT=deposit,
            FIRO_IS_ANCHOR=str(anchor),
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
                "^TestPackage$",
                "-check.f",
                "^FiroSuite.TestFiroLiveInstantSend$",
            ],
            cwd=ROOT,
            env=env,
            check=True,
            timeout=180,
        )

    def run_test(self):
        for _ in range(4):
            self.mine_quorum()
        node = self.nodes[0]
        if node.getblockcount() < 1001:
            node.generate(1001 - node.getblockcount())
            self.sync_all()
        sender = node.getnewaddress()
        funding = node.sendtoaddress(sender, 2)
        self.mine_payment(funding)
        for observer in self.nodes[:2]:
            self.wait_locked(observer, node.getbestblockhash())
        self.check_instant_send("init")

        # Advance past the empty startup snapshot before introducing a deposit.
        # Align Core's test clock with Bifrost's wall clock for the real age gate.
        set_mocktime(int(time.time()))
        set_node_times(self.nodes, int(time.time()))
        # Core resets masternode sync after a large clock jump. Let its scheduler
        # process the jump, then use the upstream regtest sync helper to catch up.
        time.sleep(2)
        sync_znodes(self.nodes, fast_mnsync=True)
        node.generate(1)
        self.sync_all()
        for observer in self.nodes[:2]:
            self.wait_locked(observer, node.getbestblockhash())
        parent = node.getrawtransaction(funding, True)
        index = next(
            out["n"]
            for out in parent["vout"]
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
        self.sync_all()
        height = node.getblockcount()
        for observer in self.nodes[:2]:
            self.wait_for_instantlock(deposit, observer, timeout=60, do_assert=True)
            tx = observer.getrawtransaction(deposit, True)
            assert tx.get("confirmations", 0) == 0 and not tx["chainlock"]
            assert observer.getrawtransaction(funding, True)["chainlock"]
        self.check_instant_send("observe", deposit, parent["height"])
        assert node.getblockcount() == height

        self.mine_payment(deposit)
        for observer in self.nodes[:2]:
            self.wait_locked(observer, node.getbestblockhash())
        self.check_instant_send("settle", deposit, parent["height"])


if __name__ == "__main__":
    ThornodeInstantSend().main()
