import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from test_output import is_noise  # noqa: E402


class TestOutputTest(unittest.TestCase):
    def test_filters_log_formats_used_by_tests(self):
        for level in ("trace", "debug", "info"):
            for field in ("message", "msg"):
                self.assertTrue(is_noise(json.dumps({"level": level, field: "noise"})))
        for line in (
            "12:05PM DBG checking transaction",
            "<nil> INF loading vaults",
            "\x1b[90m4:30PM\x1b[0m \x1b[32mINF\x1b[0m loading vaults",
            "D[2026-11-11|12:34:56.789] checking transaction",
            "I[2026-11-11|12:34:56.789] loading vaults",
        ):
            with self.subTest(line=line):
                self.assertTrue(is_noise(line))

    def test_preserves_diagnostics_and_unrecognized_output(self):
        for line in (
            '{"level":"warn","message":"warning"}',
            '{"level":"error","message":"error"}',
            '{"level":"info","value":"test data"}',
            '{"level":"info","message":',
            "12:05PM WRN warning",
            "12:05PM ERR error",
            "E[2026-11-11|12:34:56.789] error",
            "FAIL: allowance_test.go:209: DashSuite.TestSnapshot",
            "--- FAIL: TestPackage (1.00s)",
            "... obtained int = 0",
            "... expected int = 1",
            "    test.go:42: expected INFO but got DEBUG",
            "panic: unexpected INFO message",
            "goroutine 1 [running]:",
            "    /workspace/test.go:42 +0x23",
            "unexpected output",
            "",
        ):
            with self.subTest(line=line):
                self.assertFalse(is_noise(line))

    def test_wrapper_preserves_output_and_exit_status(self):
        for status in (0, 3):
            code = (
                "import sys; "
                'print(\'{"level":"debug","message":"noise"}\'); '
                'print("diagnostic", file=sys.stderr); '
                f"sys.exit({status})"
            )
            proc = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPTS / "test_output.py"),
                    sys.executable,
                    "-c",
                    code,
                ],
                capture_output=True,
                text=True,
            )
            self.assertEqual(proc.returncode, status, proc.stderr)
            self.assertEqual(proc.stdout, "diagnostic\n")

    def test_make_test_hides_logs_but_still_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake_go = Path(tmp) / "go"
            fake_go.write_text(
                "#!/usr/bin/env python3\n"
                "import sys\n"
                "assert sys.argv[1] == 'test'\n"
                'print(\'{"level":"info","message":"noise"}\')\n'
                "print('assertion failed', file=sys.stderr)\n"
                "sys.exit(1)\n"
            )
            fake_go.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{tmp}:{env['PATH']}"
            for quiet in ("0", "1"):
                proc = subprocess.run(
                    [
                        "make",
                        "--no-print-directory",
                        "test",
                        "./example/pkg",
                        "TEST_PATHS=./all",
                        f"TEST_QUIET={quiet}",
                    ],
                    cwd=SCRIPTS.parent,
                    env=env,
                    text=True,
                    capture_output=True,
                )
                output = proc.stdout + proc.stderr
                self.assertNotEqual(proc.returncode, 0, output)
                self.assertTrue("assertion failed" in output, output)
                self.assertEqual("noise" in output, quiet == "0", output)


if __name__ == "__main__":
    unittest.main()
