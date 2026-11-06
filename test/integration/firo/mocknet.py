#!/usr/bin/env python3
"""Run the actual mocknet entrypoint on six native daemons, including restart.

FIROD=/path/to/firod python3 test/integration/firo/mocknet.py
No upstream Python framework, Docker, or third-party Python packages are needed.
Set FIRO_TEST_DIR to retain daemon data and logs for diagnosis.
"""
import importlib.util
import json
import os
import socket
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest.mock import patch

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[3]
ENTRYPOINT = ROOT / "build/docker/mocknet/firo/entrypoint.py"
HEALTHCHECK = ROOT / "build/docker/firo/healthcheck.py"
spec = importlib.util.spec_from_file_location("firo_mocknet", ENTRYPOINT)
mocknet = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mocknet)


class MocknetConfigTest(unittest.TestCase):
    def test_default_nodes_share_loopback_with_distinct_ports(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.dict(
                os.environ,
                {
                    "FIRO_DATA_DIR": directory + "/data",
                    "FIRO_QUORUM_DIR": directory + "/quorum",
                    "FIRO_NODE": "1",
                },
            ):
                os.environ.pop("FIRO_NODES", None)
                nodes = mocknet.Mocknet().nodes
        self.assertEqual(len(nodes), 6)
        self.assertEqual({node["host"] for node in nodes}, {"127.0.0.1"})
        ports = [node[kind] for node in nodes for kind in ("p2p", "rpc")]
        self.assertEqual(len(set(ports)), 12)
        self.assertEqual(nodes[0]["rpc"], 28888)  # Compose's published Bifrost endpoint


@unittest.skipUnless(os.getenv("FIROD"), "set FIROD to run native mocknet tests")
class MocknetTest(unittest.TestCase):
    def setUp(self):
        self.temporary = None
        if os.getenv("FIRO_TEST_DIR"):
            self.directory = Path(os.environ["FIRO_TEST_DIR"]) / self._testMethodName
            self.directory.mkdir(parents=True, exist_ok=True)
        else:
            self.temporary = tempfile.TemporaryDirectory(prefix="firo-mocknet-")
            self.directory = Path(self.temporary.name)
        print(f"FIRO mocknet test data: {self.directory}", flush=True)
        sockets = []
        try:
            for _ in range(12):
                sock = socket.socket()
                sock.bind(("127.0.0.1", 0))
                sockets.append(sock)
            ports = [sock.getsockname()[1] for sock in sockets]
        finally:
            for sock in sockets:
                sock.close()
        self.nodes = [
            {"host": "127.0.0.1", "p2p": ports[i], "rpc": ports[6 + i]}
            for i in range(6)
        ]
        self.env = dict(
            os.environ,
            FIRO_NODES=json.dumps(self.nodes),
            FIRO_QUORUM_DIR=str(self.directory / "quorum"),
            BLOCK_TIME="2",
            FIRO_MASTER_ADDR=mocknet.MASTER_ADDRESS,
            FIRO_MASTER_AMOUNT="10000",
            FIRO_MOCKNET="1",
            FIRO_NETWORK="regtest",
            SIGNER_NAME="thorchain",
            SIGNER_PASSWD="password",
            FIROCLI=os.getenv(
                "FIROCLI", str(Path(os.environ["FIROD"]).with_name("firo-cli"))
            ),
        )
        self.processes = []
        self.logs = []
        self.entrypoint = ENTRYPOINT
        self.addCleanup(self.cleanup)

    def cleanup(self):
        self.stop()
        for log in self.logs:
            log.close()
        if self.temporary:
            self.temporary.cleanup()

    def start(self, wait_ready=True):
        for i in range(6):
            env = dict(
                self.env,
                FIRO_NODE=str(i + 1),
                FIRO_DATA_DIR=str(self.directory / f"node{i+1}"),
            )
            log = (self.directory / f"node{i+1}.log").open("a")
            self.logs.append(log)
            self.processes.append(
                subprocess.Popen(
                    [sys.executable, str(self.entrypoint)],
                    env=env,
                    stdout=log,
                    stderr=subprocess.STDOUT,
                )
            )
        if wait_ready:
            self.wait(
                lambda: (self.directory / "quorum/ready.json").exists(),
                "mocknet readiness",
                timeout=600,
            )

    def stop(self):
        for process in self.processes:
            if process.poll() is None:
                process.terminate()
        for process in self.processes:
            try:
                process.wait(timeout=60)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        self.processes.clear()

    def wait(self, check, description, timeout=120):
        def checked():
            for i, process in enumerate(self.processes):
                if process.poll() is not None:
                    log = (self.directory / f"node{i+1}.log").read_text()
                    self.fail(f"Node {i+1} exited: {log[-6000:]}")
            return check()

        return mocknet.wait_for(checked, description, timeout)

    def call(self, index, method, *params):
        return mocknet.rpc(
            f"http://127.0.0.1:{self.nodes[index]['rpc']}", method, *params
        )

    def health(self, index, quorum=None):
        env = dict(self.env, FIRO_DATA_DIR=str(self.directory / f"node{index+1}"))
        if quorum is not None:
            env["FIRO_QUORUM_DIR"] = str(quorum)
        return subprocess.run(
            [sys.executable, str(HEALTHCHECK)],
            env=env,
            capture_output=True,
            text=True,
            timeout=20,
        )

    def state(self):
        return json.loads((self.directory / "quorum/state.json").read_text())

    def assert_ready(self):
        for i in range(6):
            health = self.health(i)
            self.assertEqual(health.returncode, 0, health.stderr)
            if i:
                status = self.call(i, "evoznode", "status")
                self.assertEqual(status["state"], "READY", status)
                self.assertEqual(status["service"], f"127.0.0.1:{self.nodes[i]['p2p']}")
        state = self.state()
        funding = self.call(0, "getrawtransaction", state["funding"]["txid"], True)
        self.assertTrue(funding["chainlock"])
        self.assertEqual(
            self.call(0, "getreceivedbyaddress", mocknet.MASTER_ADDRESS), 10000
        )
        registrations = self.call(0, "protx", "list", "valid", True)
        self.assertEqual(len(registrations), 5)
        return state

    def test_restart_during_clock_sync(self):
        # Hold node 6 offline during its normal post-registration restart until
        # the controller actually calls setmocktime. This makes the readiness
        # probe / restart race deterministic instead of relying on CPU timing.
        self.entrypoint = self.directory / "restart-race.py"
        self.entrypoint.write_text(
            textwrap.dedent(
                f"""\
            import importlib.util
            from pathlib import Path
            spec = importlib.util.spec_from_file_location("entrypoint", {str(ENTRYPOINT)!r})
            app = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(app)
            directory = Path({str(self.directory)!r})
            offline = directory / "member-offline"
            release = directory / "release-member"
            failed_rpc = directory / "failed-clock-rpc"
            original_close = app.Mocknet.close
            original_call = app.Mocknet.call

            def close(self):
                original_close(self)
                if self.index == 5 and not offline.exists():
                    offline.touch()
                    app.wait_for(release.exists, "controller attempted the clock update", timeout=60)

            def call(self, index, method, *params):
                if self.index == 0 and index == 5 and method == "setmocktime" and not release.exists():
                    app.wait_for(offline.exists, "member restart", timeout=60)
                    try:
                        return original_call(self, index, method, *params)
                    except OSError:
                        failed_rpc.touch()
                        raise
                    finally:
                        release.touch()
                return original_call(self, index, method, *params)

            app.Mocknet.close = close
            app.Mocknet.call = call
            app.main()
            """
            )
        )
        self.start()
        self.assertTrue(
            (self.directory / "failed-clock-rpc").exists(), "race was not exercised"
        )
        self.assert_ready()
        print(
            "Clock update survived a real member restart without restarting the controller",
            flush=True,
        )

    def test_recover_private_address_registrations(self):
        # Reproduce the old Docker setup on native daemons: Core discards a
        # private externalip and reports a loopback identity, so DKG cannot start.
        for node in self.nodes:
            node["host"] = "127.0.0.2"  # non-routable; binds without extra interfaces
        self.env["FIRO_NODES"] = json.dumps(self.nodes)
        self.start(wait_ready=False)
        self.wait(
            lambda: len(self.call(0, "protx", "list", "registered")) == 5,
            "old registrations",
        )
        self.wait(
            lambda: all(
                self.call(i, "evoznode", "status").get("state") == "ERROR"
                for i in range(1, 6)
            ),
            "old address mismatch",
        )
        for i in range(1, 6):
            status = self.call(i, "evoznode", "status")
            self.assertIn(  # codespell:ignore
                "Local address does not match", status["status"]
            )
        self.assertFalse((self.directory / "quorum/ready.json").exists())
        original = self.state()
        self.stop()

        # Recreate the services with the fixed loopback layout and the same data.
        for node in self.nodes:
            node["host"] = "127.0.0.1"
        self.env["FIRO_NODES"] = json.dumps(self.nodes)
        self.start()
        recovered = self.assert_ready()
        self.assertEqual(recovered["members"], original["members"])
        print("Recovered mismatched registrations without resetting data", flush=True)

    def test_start_mine_and_restart(self):
        self.start()
        original = self.assert_ready()
        print("Bootstrap, funding and health passed", flush=True)
        initial = self.call(0, "quorum", "list")["llmq_5_60"]
        payment = self.call(0, "sendtoaddress", self.call(0, "getnewaddress"), 1)
        self.wait(
            lambda: self.call(0, "getrawtransaction", payment, True).get("chainlock"),
            "ordinary payment ChainLock",
        )
        self.wait(
            lambda: self.call(0, "quorum", "list")["llmq_5_60"] != initial,
            "continued quorum rotation",
            timeout=180,
        )
        print("Continuous mining, payment and quorum rotation passed", flush=True)
        height = self.call(0, "getblockcount")
        self.stop()
        self.assertFalse((self.directory / "quorum/ready.json").exists())
        # Use new P2P ports, as can happen when Compose recreates its network.
        # The controller must update on-chain service endpoints with saved keys.
        for node in self.nodes:
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                node["p2p"] = sock.getsockname()[1]
        self.env["FIRO_NODES"] = json.dumps(self.nodes)
        self.start()
        resumed = self.assert_ready()
        self.assertEqual(resumed["members"], original["members"])
        self.assertEqual(resumed["funding"], original["funding"])
        self.assertGreater(self.call(0, "getblockcount"), height)
        print(
            "Restart with new peer addresses preserved keys, registrations and funding",
            flush=True,
        )
        stale = json.loads((self.directory / "quorum/ready.json").read_text())
        stale["updated"] = 0
        stale_directory = self.directory / "stale-heartbeat"
        stale_directory.mkdir(exist_ok=True)
        mocknet.atomic_json(stale_directory / "ready.json", stale)
        self.assertNotEqual(self.health(0, stale_directory).returncode, 0)
        print("Stale readiness is rejected", flush=True)
        self.call(0, "stop")
        self.assertNotEqual(self.processes[0].wait(timeout=30), 0)
        self.assertFalse((self.directory / "quorum/ready.json").exists())
        print("Daemon exit stops the controller so Compose can restart it", flush=True)


if __name__ == "__main__":
    unittest.main(verbosity=2)
