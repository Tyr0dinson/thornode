"""Mocknet startup contract tests using the real shell helper and JSON parser.

Run with: python3 -m unittest discover -s build/scripts/tests -v
Requires bash, jq and timeout; no chain services or signing keys are used.
"""

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parents[1]
REGISTRATIONS = ["deposit", "set-node-keys", "set-ip-address", "set-version"]
SEQUENCE_ERROR = {
    "code": 32,
    "codespace": "sdk",
    "raw_log": "account sequence mismatch",
}

# Each broadcast has a scripted response and a list of subsequent query results.
# Repeating the final result lets tests model an index that never confirms a tx.
THORNODE = r"""#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import sys

root = Path(os.environ["MOCK_STATE"])
plan = json.loads((root / "plan.json").read_text())
state = json.loads((root / "state.json").read_text())
args = sys.argv[1:]
if args[0] in ("init", "render-config", "start"):
    state["events"].append([args[0]])
    (root / "state.json").write_text(json.dumps(state))
    if args[0] == "render-config" and os.environ.get("FAIL_RENDER"):
        sys.exit(1)
    if args[0] != "start":
        genesis = root / ".thornode/config/genesis.json"
        genesis.parent.mkdir(parents=True, exist_ok=True)
        genesis.write_text("{}")
    if args[0] == "init" and os.environ.get("FAIL_INIT"):
        sys.exit(1)
    sys.exit(0)
if args[0] == "keys":
    print("mock-node-key" if "--pubkey" in args else "tthor1mock")
    sys.exit(0)
if args[0] in ("tendermint", "pubkey"):
    print("mock-pubkey")
    sys.exit(0)

if args[:2] == ["tx", "thorchain"]:
    if os.environ.get("CHECK_REGISTRATION_STATE"):
        assert (root / ".thornode/mocknet-registration.state").read_text() == "registering\n"
    sys.stdin.read()
    stage = args[2]
    attempt = state["attempts"].get(stage, 0)
    state["attempts"][stage] = attempt + 1
    responses = plan.get(stage, [{}])
    spec = responses[min(attempt, len(responses) - 1)]
    txhash = hashlib.sha256(f"{stage}:{attempt}".encode()).hexdigest().upper()
    state["transactions"][txhash] = {"spec": spec, "polls": 0, "stage": stage}
    response = {"code": 0, "codespace": "", "height": "0", "txhash": txhash}
    response.update(spec.get("broadcast", {}))
    state["events"].append(["broadcast", stage, args, list(state["committed"])])
elif args[:2] == ["query", "tx"]:
    txhash = args[2]
    tx = state["transactions"][txhash]
    queries = tx["spec"].get("queries", [{}])
    spec = queries[min(tx["polls"], len(queries) - 1)]
    tx["polls"] += 1
    response = {"code": 0, "codespace": "", "height": "12", "txhash": txhash}
    response.update(spec.get("result", {}))
    state["events"].append(["query", tx["stage"], args])
    if not spec.get("exit", 0) and "raw" not in spec and response["code"] == 0 and int(response["height"]) > 0:
        state["committed"].append(tx["stage"])
else:
    raise AssertionError(f"unexpected command: {args}")

(root / "state.json").write_text(json.dumps(state))
print(spec.get("raw", json.dumps(response)))
sys.exit(spec.get("exit", 0))
"""


class MocknetStartupTest(unittest.TestCase):
    def setUp(self):
        for tool in ("bash", "jq", "timeout"):
            self.assertIsNotNone(
                shutil.which(tool), f"required test tool missing: {tool}"
            )
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.write_executable("thornode", THORNODE)
        self.write_executable("sleep", "#!/bin/sh\nexit 0\n")
        self.write_executable("curl", "#!/bin/sh\nexit 0\n")
        self.env = {
            **os.environ,
            "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
            "MOCK_STATE": str(self.root),
            "NET": "test",
            "PEER": "mock-peer",
            "PORT_RPC": "26699",
            "SIGNER_PASSWD": "test-only",
            "SIGNER_NAME": "thorchain",
            "CHAIN_ID": "thorchain",
            "NODE_PUB_KEY_ED25519": "mock-ed25519",
            "EXTERNAL_IP": "172.19.0.3",
            "HOME": str(self.root),
        }

    def write_executable(self, name, content):
        path = self.root / name
        path.write_text(content)
        path.chmod(0o755)

    def initialize_state(self, plan):
        (self.root / "plan.json").write_text(json.dumps(plan))
        (self.root / "state.json").write_text(
            json.dumps(
                {
                    "attempts": {},
                    "transactions": {},
                    "events": [],
                    "committed": [],
                }
            )
        )

    def run_startup(self, plan):
        self.initialize_state(plan)
        script = os.environ.get("CORE_UNSAFE_SCRIPT", str(SCRIPTS / "core-unsafe.sh"))
        result = subprocess.run(
            ["bash", "-c", 'source "$1"; init_mocknet', "test", script],
            env=self.env,
            capture_output=True,
            text=True,
            timeout=20,
        )
        state = json.loads((self.root / "state.json").read_text())
        return result, state

    def test_waits_for_each_committed_registration(self):
        delayed = {"queries": [{"exit": 1}, {"result": {"height": "0"}}, {}]}
        result, state = self.run_startup({stage: [delayed] for stage in REGISTRATIONS})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(state["committed"], REGISTRATIONS)
        broadcasts = [e for e in state["events"] if e[0] == "broadcast"]
        for i, event in enumerate(broadcasts):
            self.assertEqual(event[1], REGISTRATIONS[i])
            self.assertEqual(event[3], REGISTRATIONS[:i])
            self.assertTrue("sync" in event[2])
            self.assertTrue("json" in event[2])
        for event in state["events"]:
            self.assertTrue("tcp://mock-peer:26699" in event[2])
        self.assertTrue(all(tx["polls"] == 3 for tx in state["transactions"].values()))

    def test_retries_exit_zero_sequence_rejections(self):
        for stage in REGISTRATIONS:
            with self.subTest(stage=stage):
                result, state = self.run_startup(
                    {stage: [{"broadcast": SEQUENCE_ERROR}, {}]}
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(state["attempts"][stage], 2)
                self.assertEqual(state["committed"], REGISTRATIONS)

    def test_retries_sequence_failure_at_execution(self):
        result, state = self.run_startup(
            {
                "set-ip-address": [{"queries": [{"result": SEQUENCE_ERROR}]}, {}],
            }
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(state["attempts"]["set-ip-address"], 2)
        self.assertEqual(state["committed"], REGISTRATIONS)

    def test_bounds_sequence_retries_and_stops_registration(self):
        result, state = self.run_startup(
            {"set-ip-address": [{"broadcast": SEQUENCE_ERROR}]}
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(state["attempts"]["set-ip-address"], 10)
        self.assertNotIn("set-version", state["attempts"])

    def test_stops_on_permanent_broadcast_or_execution_rejection(self):
        for rejection in (
            {"code": 5, "raw_log": "insufficient funds"},
            {"code": 32, "codespace": "other"},
        ):
            for phase in ("broadcast", "execution"):
                with self.subTest(rejection=rejection, phase=phase):
                    spec = (
                        {"broadcast": rejection}
                        if phase == "broadcast"
                        else {"queries": [{"result": rejection}]}
                    )
                    result, state = self.run_startup({"deposit": [spec]})
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(state["attempts"], {"deposit": 1})

    def test_does_not_rebroadcast_an_unconfirmed_bond(self):
        for query in (
            {"exit": 1},
            {"result": {"height": "0"}},
            {"raw": "{}"},
            {"raw": "invalid json"},
        ):
            with self.subTest(query=query):
                result, state = self.run_startup({"deposit": [{"queries": [query]}]})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(state["attempts"], {"deposit": 1})
                self.assertEqual(
                    next(iter(state["transactions"].values()))["polls"], 30
                )

    def test_stops_on_cli_error_or_malformed_broadcast(self):
        for spec in (
            {"exit": 1},
            {"raw": "invalid json"},
            {"raw": "{}"},
            {"broadcast": {"txhash": ""}},
        ):
            with self.subTest(spec=spec):
                result, state = self.run_startup({"deposit": [spec]})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(state["attempts"], {"deposit": 1})

    def prepare_validator(self, plan=None):
        self.initialize_state(plan or {})
        validator = os.environ.get("VALIDATOR_SCRIPT", str(SCRIPTS / "validator.sh"))
        shutil.copyfile(validator, self.root / "validator.sh")
        shutil.copyfile(SCRIPTS / "core-unsafe.sh", self.root / "core-unsafe.sh")
        (self.root / "core.sh").write_text(
            "resolve_signer_seed_phrase() { :; }\n"
            "create_thor_user() { :; }\n"
            "init_chain() { thornode init; }\n"
        )
        self.marker = self.root / ".thornode/mocknet-registration.state"
        self.genesis = self.root / ".thornode/config/genesis.json"

    def run_validator(self, **env):
        result = subprocess.run(
            ["bash", str(self.root / "validator.sh")],
            env={**self.env, "NET": "mocknet", "CHECK_REGISTRATION_STATE": "1", **env},
            capture_output=True,
            text=True,
            timeout=20,
        )
        return result, json.loads((self.root / "state.json").read_text())

    def test_validator_registers_once_and_restarts_normally(self):
        self.prepare_validator()
        first, before = self.run_validator()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(before["committed"], REGISTRATIONS)
        self.assertEqual(self.marker.read_text(), "registered\n")
        self.assertTrue(self.genesis.exists())
        second, after = self.run_validator()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(after["attempts"], before["attempts"])
        previous_events = len(before["events"])
        self.assertEqual(
            after["events"][previous_events:], [["render-config"], ["start"]]
        )

    def test_validator_refuses_restart_after_uncertain_bond(self):
        self.prepare_validator({"deposit": [{"queries": [{"exit": 1}]}]})
        first, before = self.run_validator()
        self.assertNotEqual(first.returncode, 0)
        self.assertEqual(before["attempts"], {"deposit": 1})
        self.assertEqual(self.marker.read_text(), "registering\n")
        second, after = self.run_validator()
        self.assertNotEqual(second.returncode, 0)
        self.assertTrue("refusing to submit again" in second.stderr)
        self.assertEqual(after, before, "restart sent commands after an uncertain bond")

    def test_validator_refuses_restart_after_committed_bond_and_failed_keys(self):
        self.prepare_validator({"set-node-keys": [{"broadcast": {"code": 5}}]})
        first, before = self.run_validator()
        self.assertNotEqual(first.returncode, 0)
        self.assertEqual(before["committed"], ["deposit"])
        self.assertEqual(self.marker.read_text(), "registering\n")
        second, after = self.run_validator()
        self.assertNotEqual(second.returncode, 0)
        self.assertEqual(after, before, "restart repeated a committed bond")

    def test_validator_checks_registering_before_temporary_genesis(self):
        self.prepare_validator()
        first, before = self.run_validator(FAIL_INIT="1")
        self.assertNotEqual(first.returncode, 0)
        self.assertTrue(self.genesis.exists())
        self.assertEqual(before["attempts"], {})
        second, after = self.run_validator()
        self.assertNotEqual(second.returncode, 0)
        self.assertEqual(
            after, before, "temporary genesis bypassed incomplete registration"
        )

    def test_validator_resumes_rendering_after_confirmed_registration(self):
        self.prepare_validator()
        first, before = self.run_validator(FAIL_RENDER="1")
        self.assertNotEqual(first.returncode, 0)
        self.assertFalse(self.genesis.exists())
        self.assertEqual(self.marker.read_text(), "registered\n")
        self.assertEqual(before["committed"], REGISTRATIONS)
        self.assertNotIn(["start"], before["events"])
        second, after = self.run_validator()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(after["attempts"], before["attempts"])
        previous_events = len(before["events"])
        self.assertEqual(
            after["events"][previous_events:], [["render-config"], ["start"]]
        )

    def test_validator_stops_if_initial_marker_cannot_be_persisted(self):
        self.prepare_validator()
        self.write_executable("mv", "#!/bin/sh\nexit 1\n")
        result, state = self.run_validator()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(state["events"], [])
        self.assertFalse(self.marker.exists())

    def test_validator_keeps_guard_if_registered_state_cannot_be_persisted(self):
        self.prepare_validator()
        self.write_executable(
            "mv",
            '#!/bin/sh\nif [ "$(cat "$2")" = registered ]; then exit 1; fi\n'
            'exec /bin/mv "$@"\n',
        )
        first, before = self.run_validator()
        self.assertNotEqual(first.returncode, 0)
        self.assertEqual(before["committed"], REGISTRATIONS)
        self.assertEqual(self.marker.read_text(), "registering\n")
        self.assertNotIn(["render-config"], before["events"])
        second, after = self.run_validator()
        self.assertNotEqual(second.returncode, 0)
        self.assertEqual(after, before)

    def test_validator_refuses_invalid_registration_state(self):
        self.prepare_validator()
        self.marker.parent.mkdir()
        self.marker.write_text("invalid\n")
        result, state = self.run_validator()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(state["events"], [])

    def test_validator_preserves_existing_initialized_volume_without_marker(self):
        self.prepare_validator()
        self.genesis.parent.mkdir(parents=True)
        self.genesis.write_text("{}")
        result, state = self.run_validator()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(state["attempts"], {})
        self.assertEqual(state["events"], [["render-config"], ["start"]])
        self.assertFalse(self.marker.exists())


if __name__ == "__main__":
    unittest.main()
