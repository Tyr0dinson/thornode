import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


RUNNER = Path(__file__).resolve().parents[1] / "test-quality-run.py"
spec = importlib.util.spec_from_file_location("quality_runner", RUNNER)
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class QualityRunnerTest(unittest.TestCase):
    def test_skipped_required_test_is_failure_even_with_other_passes(self):
        lines = [json.dumps(event) for event in (
            {"Action": "skip", "Package": "p", "Test": "SecurityGate"},
            {"Action": "pass", "Package": "p", "Test": "OtherTest"},
            {"Action": "pass", "Package": "p", "Test": "OtherTest/case"},
        )]
        tests, packages, _, executed, skipped, missing = runner.events_to_report(lines, ["SecurityGate"])
        self.assertEqual((executed, skipped, missing), (1, 1, ["SecurityGate"]))
        self.assertEqual(packages, {"p"})
        self.assertEqual(len(tests), 3)

    def test_failing_test_and_package_setup_create_junit_failures(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "junit.xml"
            runner.write_junit(target, {("p", "Failure"): "fail"}, ["assertion failed"], True)
            self.assertIn("assertion failed", target.read_text())
            self.assertIn('failures="1"', target.read_text())
            runner.write_junit(target, {}, ["compile failed"], True)
            self.assertIn("package setup", target.read_text())
            self.assertIn("compile failed", target.read_text())

    def test_command_fails_on_zero_tests_and_filters_generated_coverage(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            fake_go = root / "go"
            fake_go.write_text("""#!/usr/bin/env python3
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
""")
            fake_go.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{root}:{env['PATH']}"
            report = root / "reports"
            proc = subprocess.run([sys.executable, str(RUNNER), "--module", str(root),
                "--report-dir", str(report), "--require-test", "SecurityGate",
                "--require-package-tests", "/p", "--forbid-skip-package", "/p",
                "--max-skips", "0",
                "--exclude-prefix", "example.com/excluded",
                "--test-arg=-check.f", "--test-arg=Migrate", "./..."],
                env=env, text=True, capture_output=True)
            self.assertEqual(proc.returncode, 1, proc.stdout + proc.stderr)
            self.assertEqual(json.loads((report / "summary.json").read_text())["skipped_top_level"], 1)
            self.assertEqual(json.loads((report / "summary.json").read_text())["required_packages_without_executed_tests"], ["/p"])
            self.assertEqual(json.loads((report / "summary.json").read_text())["unexpected_skips"], ["example.com/p:SecurityGate"])
            self.assertEqual(json.loads((report / "inventory.json").read_text())["excluded_packages"], ["example.com/excluded"])
            self.assertIn("required test did not pass", (report / "junit.xml").read_text())
            self.assertIn("skipped 1 tests, maximum 0", (report / "junit.xml").read_text())
            self.assertEqual(json.loads((root / "arguments.json").read_text())[-2:], ["-check.f", "Migrate"])
            self.assertNotIn("a.pb.go", (report / "coverage.txt").read_text())
            self.assertIn("real.go", (report / "coverage.txt").read_text())


if __name__ == "__main__":
    unittest.main()
