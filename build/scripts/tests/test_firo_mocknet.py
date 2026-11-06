"""FIRO miner startup races; no native daemon or Docker required."""

import importlib.util
import unittest
from pathlib import Path
from unittest.mock import Mock, patch
from urllib.error import URLError

ROOT = Path(__file__).resolve().parents[3]
spec = importlib.util.spec_from_file_location(
    "firo_mocknet", ROOT / "build/docker/mocknet/firo/entrypoint.py"
)
mocknet = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mocknet)


class FiroMocknetClockTest(unittest.TestCase):
    def setUp(self):
        self.net = mocknet.Mocknet.__new__(mocknet.Mocknet)
        self.net.state = {"clock": 1000}
        self.net.save = Mock()
        self.net.sync = Mock()

    def test_restart_after_readiness_does_not_remine_or_advance_clock_twice(self):
        for failure in (
            URLError(ConnectionRefusedError("member restarting")),
            TimeoutError("clock applied but response lost"),
            mocknet.RPCError("Loading block index..."),
        ):
            with self.subTest(failure=failure):
                self.setUp()
                attempts = []
                clocks = {}
                mined = []

                def call(index, method, *params):
                    if method == "getblockchaininfo":
                        return {"blocks": 1011}
                    if method == "getrawmempool":
                        return []
                    if method == "setmocktime":
                        attempts.append((index, params[0]))
                        if index == 3 and attempts.count((index, params[0])) == 1:
                            if isinstance(failure, TimeoutError):
                                clocks[index] = params[0]
                            raise failure
                        clocks[index] = params[0]
                        return None
                    if method == "generate":
                        self.assertEqual(clocks, {i: 1601 for i in range(6)})
                        mined.append(params[0])
                        return ["block"]
                    self.fail(f"unexpected RPC {method}")

                self.net.call = call
                # The earlier readiness probe succeeds; the next RPC can fail.
                self.assertTrue(self.net.call(3, "getblockchaininfo"))
                with patch.object(mocknet.STOP, "wait") as sleep:
                    self.assertEqual(self.net.mine(seconds=601), "block")
                self.assertEqual(self.net.state["clock"], 1601)
                self.net.save.assert_called_once()
                self.net.sync.assert_called_once()
                self.assertEqual(attempts.count((3, 1601)), 2)
                self.assertEqual(len(attempts), 7)
                self.assertEqual(mined, [1])
                sleep.assert_called_once_with(0.2)

    def test_unavailable_member_times_out_before_mining(self):
        def call(index, method, *params):
            if method == "getrawmempool":
                return []
            if method == "setmocktime":
                raise URLError(ConnectionRefusedError("member stays offline"))
            self.fail(f"must not mine while a member is offline: {method}")

        self.net.call = call
        with patch.object(mocknet.time, "monotonic", side_effect=[0, 181]):
            with self.assertRaisesRegex(RuntimeError, "node 1 clock synchronization"):
                self.net.mine(seconds=601)
        self.assertEqual(self.net.state["clock"], 1601)
        self.net.save.assert_called_once()
        self.net.sync.assert_not_called()

    def test_shutdown_interrupts_the_clock_wait(self):
        self.net.call = Mock(side_effect=URLError("member restarting"))
        with patch.object(
            mocknet.STOP, "is_set", side_effect=[False, True]
        ), patch.object(mocknet.STOP, "wait"):
            with self.assertRaises(InterruptedError):
                self.net.tick()
        self.net.call.assert_called_once_with(0, "setmocktime", 1001)


if __name__ == "__main__":
    unittest.main()
