import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

RUNNER = Path(__file__).resolve().parents[1] / "test-quality-run.py"
sys.path.insert(0, str(RUNNER.parent))
spec = importlib.util.spec_from_file_location("quality_runner", RUNNER)
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class QualityRunnerTest(unittest.TestCase):
    def test_gocheck_case_counts_are_scoped_to_package(self):
        lines = [
            json.dumps(event)
            for event in (
                {
                    "Action": "output",
                    "Package": "example.com/actors/core",
                    "Output": "OK: 1 passed\n",
                },
                {
                    "Action": "output",
                    "Package": "example.com/pkg/types",
                    "Output": "OK: 7 passed\n",
                },
                {
                    "Action": "output",
                    "Package": "example.com/pkg/types",
                    "Output": "ordinary output\n",
                },
            )
        ]
        self.assertEqual(
            runner.gocheck_counts(lines),
            {
                "example.com/actors/core": 1,
                "example.com/pkg/types": 7,
            },
        )

    def test_skipped_required_test_is_failure_even_with_other_passes(self):
        lines = [
            json.dumps(event)
            for event in (
                {"Action": "skip", "Package": "p", "Test": "SecurityGate"},
                {"Action": "pass", "Package": "p", "Test": "OtherTest"},
                {"Action": "pass", "Package": "p", "Test": "OtherTest/case"},
            )
        ]
        tests, packages, _, executed, skipped, missing = runner.events_to_report(
            lines, ["SecurityGate"]
        )
        self.assertEqual((executed, skipped, missing), (1, 1, ["SecurityGate"]))
        self.assertEqual(packages, {"p"})
        self.assertEqual(len(tests), 3)

    def test_failing_test_and_package_setup_create_junit_failures(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "junit.xml"
            runner.write_junit(
                target, {("p", "Failure"): "fail"}, ["assertion failed"], True
            )
            self.assertTrue("assertion failed" in target.read_text())
            self.assertTrue('failures="1"' in target.read_text())
            runner.write_junit(target, {}, ["compile failed"], True)
            self.assertTrue("package setup" in target.read_text())
            self.assertTrue("compile failed" in target.read_text())

    def test_command_fails_on_zero_tests_and_filters_generated_coverage(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            fake_go = root / "go"
            fake_go.write_text(
                """#!/usr/bin/env python3
import json, sys
from pathlib import Path
if sys.argv[1] == 'list':
    print('example.com/p')
    print('example.com/excluded')
else:
    Path('arguments.json').write_text(json.dumps(sys.argv[1:]))
    path = Path(sys.argv[sys.argv.index('-coverprofile') + 1])
    path.write_text('mode: set\\nexample.com/p/a.pb.go:1.1,1.2 1 1\\nexample.com/p/real.go:1.1,1.2 1 1\\n')
    print(json.dumps({'Action': 'skip', 'Package': 'example.com/p', 'Test': 'SecurityGate'}))
"""
            )
            fake_go.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{root}:{env['PATH']}"
            report = root / "reports"
            proc = subprocess.run(
                [
                    sys.executable,
                    str(RUNNER),
                    "--module",
                    str(root),
                    "--report-dir",
                    str(report),
                    "--require-test",
                    "SecurityGate",
                    "--require-package-tests",
                    "/p",
                    "--forbid-skip-package",
                    "/p",
                    "--max-skips",
                    "0",
                    "--exclude-prefix",
                    "example.com/excluded",
                    "--test-arg=-check.f",
                    "--test-arg=Migrate",
                    "./...",
                ],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertEqual(proc.returncode, 1, proc.stdout + proc.stderr)
            self.assertEqual(
                json.loads((report / "summary.json").read_text())["skipped_top_level"],
                1,
            )
            self.assertEqual(
                json.loads((report / "summary.json").read_text())[
                    "required_packages_without_executed_tests"
                ],
                ["/p"],
            )
            self.assertEqual(
                json.loads((report / "summary.json").read_text())["unexpected_skips"],
                ["example.com/p:SecurityGate"],
            )
            self.assertEqual(
                json.loads((report / "inventory.json").read_text())[
                    "excluded_packages"
                ],
                ["example.com/excluded"],
            )
            self.assertTrue(
                "required test did not pass" in (report / "junit.xml").read_text()
            )
            self.assertTrue(
                "skipped 1 tests, maximum 0" in (report / "junit.xml").read_text()
            )
            self.assertEqual(
                json.loads((root / "arguments.json").read_text())[-2:],
                ["-check.f", "Migrate"],
            )
            self.assertNotIn("a.pb.go", (report / "coverage.txt").read_text())
            self.assertTrue("real.go" in (report / "coverage.txt").read_text())

    def test_allowlist_violation_is_reported_in_summary(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            fake_go = root / "go"
            fake_go.write_text(
                """#!/usr/bin/env python3
import json, sys
from pathlib import Path
if sys.argv[1] == 'list':
    print('example.com/p')
else:
    path = Path(sys.argv[sys.argv.index('-coverprofile') + 1])
    path.write_text('mode: set\\n')
    for name, action in (('Ran', 'pass'), ('Allowed', 'skip'), ('Stray', 'skip')):
        print(json.dumps({'Action': action, 'Package': 'example.com/p', 'Test': name}))
"""
            )
            fake_go.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{root}:{env['PATH']}"
            report = root / "reports"
            proc = subprocess.run(
                [
                    sys.executable,
                    str(RUNNER),
                    "--module",
                    str(root),
                    "--report-dir",
                    str(report),
                    "--allow-skip-test",
                    "Allowed",
                    "./...",
                ],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertEqual(proc.returncode, 1, proc.stdout + proc.stderr)
            summary = json.loads((report / "summary.json").read_text())
            self.assertEqual(summary["go_test_exit"], 0)
            self.assertEqual(summary["unexpected_skips"], ["example.com/p:Stray"])
            self.assertEqual(
                summary["selection_errors"],
                ["unexpected skipped test: example.com/p:Stray"],
            )

    def test_quiet_mode_preserves_early_failures_and_raw_logs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            fake_go = root / "go"
            fake_go.write_text(
                """#!/usr/bin/env python3
import json, sys
if sys.argv[1] == 'list':
    print('example.com/p')
else:
    def output(text):
        print(json.dumps({'Action': 'output', 'Package': 'example.com/p',
                          'Test': 'TestPackage', 'Output': text + '\\n'}))
    output('FAIL: test.go:10: Suite.TestSnapshot\\n... expected int = 1')
    for _ in range(150):
        output('{"level":"info","message":"noisy log"}')
        output('--- PASS: LaterTest')
    output('{"level":"warn","message":"warning"}')
    output('panic: diagnostic')
    print('{"level":"debug","message":"stderr noise"}', file=sys.stderr)
    print('compiler diagnostic', file=sys.stderr)
    print(json.dumps({'Action': 'fail', 'Package': 'example.com/p', 'Test': 'TestPackage'}))
    sys.exit(1)
"""
            )
            fake_go.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{root}:{env['PATH']}"
            env["TEST_QUIET"] = "1"
            report = root / "reports"
            proc = subprocess.run(
                [sys.executable, str(RUNNER), "--report-dir", str(report), "./..."],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertEqual(proc.returncode, 1, proc.stdout + proc.stderr)
            self.assertNotIn("noisy log", proc.stderr)
            self.assertNotIn("stderr noise", proc.stderr)
            for diagnostic in (
                "Suite.TestSnapshot",
                "expected int = 1",
                "warning",
                "panic:",
            ):
                self.assertTrue(diagnostic in proc.stderr, proc.stderr)
                self.assertTrue(
                    diagnostic in (report / "junit.xml").read_text(), diagnostic
                )
            self.assertTrue("compiler diagnostic" in proc.stderr, proc.stderr)
            self.assertTrue("noisy log" in (report / "events.jsonl").read_text())
            summary = json.loads((report / "summary.json").read_text())
            self.assertEqual(summary["go_test_exit"], 1)
            self.assertEqual(summary["executed_top_level"], 1)


if __name__ == "__main__":
    unittest.main()
