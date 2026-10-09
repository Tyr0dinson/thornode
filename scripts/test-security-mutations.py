#!/usr/bin/env python3
"""Run curated THORNode security mutations through Go overlays, never changing source files."""
import argparse
import fcntl
import json
import os
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MANIFEST = Path(__file__).with_suffix(".json")
LOCK = "/tmp/thornode-test-quality-build.lock"


def lock_timeout(_signum, _frame):
    raise TimeoutError("build lock wait exceeded")


def invoke(package, test, overlay, seconds, toolchain):
    command = ["go", "test", "-json", "-count=1", "-p", "2", "-run", test]
    if overlay:
        command += ["-overlay", str(overlay)]
    command += [package]
    env = dict(os.environ, GOMAXPROCS="4", GOTOOLCHAIN=toolchain)
    env.setdefault("GOCACHE", "/tmp/thornode-test-quality-mutation-gocache")
    start = time.monotonic()
    try:
        result = subprocess.run(
            command, cwd=ROOT, env=env, capture_output=True, text=True, timeout=seconds
        )
    except subprocess.TimeoutExpired as exc:
        return {
            "status": "timeout",
            "reason": f"go test exceeded {seconds}s",
            "command": command,
            "output": (
                (exc.stdout or b"")[-3000:].decode(errors="replace")
                if isinstance(exc.stdout, bytes)
                else (exc.stdout or "")[-3000:]
            ),
        }
    events = []
    for line in result.stdout.splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    test_failures = sorted(
        {e["Test"] for e in events if e.get("Action") == "fail" and e.get("Test")}
    )
    test_passes = sorted(
        {e["Test"] for e in events if e.get("Action") == "pass" and e.get("Test")}
    )
    if result.returncode == 0 and test_passes:
        status = "passed"
    elif test_failures:
        status = "killed"
    elif result.returncode == 0:
        status = "invalid"
    else:
        status = "invalid"
    return {
        "status": status,
        "exit_code": result.returncode,
        "command": command,
        "toolchain": toolchain,
        "failures": test_failures,
        "passes": test_passes,
        "output_tail": (result.stdout + result.stderr)[-3000:],
        "seconds": round(time.monotonic() - start, 2),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=["fast", "deep"], default="fast")
    parser.add_argument(
        "--summary", type=Path, default=Path("security-mutations-summary.json")
    )
    parser.add_argument("--seconds-per-command", type=int, default=300)
    parser.add_argument("--lock-wait", type=int, default=600)
    parser.add_argument("--budget-seconds", type=int, default=1800)
    args = parser.parse_args()
    data = json.loads(MANIFEST.read_text())
    toolchain = next(
        line.split()[1]
        for line in (ROOT / "go.mod").read_text().splitlines()
        if line.startswith("toolchain ")
    )
    mutants = [m for m in data["mutants"] if args.mode == "deep" or m["mode"] == "fast"]
    summary = {
        "schema": 1,
        "seed": data["seed"],
        "mode": args.mode,
        "toolchain": toolchain,
        "budget_seconds": args.budget_seconds,
        "seconds_per_command": args.seconds_per_command,
        "lock_wait_seconds": args.lock_wait,
        "baseline": [],
        "control": None,
        "mutants": [],
        "counts": {},
        "runner_command": sys.argv,
    }
    started = time.monotonic()
    baseline_keys = list(dict.fromkeys((m["package"], m["test"]) for m in mutants))
    control = data["control"]
    control_mutant = next(m for m in mutants if m["id"] == control["mutant"])
    baseline_keys.append((control_mutant["package"], control["test"]))
    with open(LOCK, "a+") as lock:
        previous_handler = signal.signal(signal.SIGALRM, lock_timeout)
        signal.setitimer(signal.ITIMER_REAL, args.lock_wait)
        try:
            fcntl.flock(lock, fcntl.LOCK_EX)
        except TimeoutError:
            summary["result"] = "baseline_failed"
            summary["baseline"].append(
                {"status": "infrastructure", "reason": "build lock wait exceeded"}
            )
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, previous_handler)
        summary["lock_wait_elapsed_seconds"] = round(time.monotonic() - started, 2)
        if "result" not in summary:
            with tempfile.TemporaryDirectory(
                prefix="thornode-security-mutations-"
            ) as temp:
                tempdir = Path(temp)
                for package, test in baseline_keys:
                    result = invoke(
                        package, test, None, args.seconds_per_command, toolchain
                    )
                    summary["baseline"].append(
                        {"package": package, "test": test, **result}
                    )
                    if result["status"] != "passed":
                        summary["result"] = "baseline_failed"
                        break
                if "result" not in summary:
                    for index, mutant in enumerate([control_mutant] + mutants):
                        if time.monotonic() - started >= args.budget_seconds:
                            result = {
                                "status": "timeout",
                                "reason": "suite execution budget exhausted",
                            }
                        else:
                            source = (ROOT / mutant["file"]).resolve()
                            original = source.read_text()
                            if original.count(mutant["old"]) != 1:
                                result = {
                                    "status": "invalid",
                                    "reason": "mutation anchor is not unique",
                                }
                            else:
                                replacement = tempdir / f"mutant-{index}.go"
                                replacement.write_text(
                                    original.replace(mutant["old"], mutant["new"], 1)
                                )
                                overlay = tempdir / f"overlay-{index}.json"
                                overlay.write_text(
                                    json.dumps(
                                        {"Replace": {str(source): str(replacement)}}
                                    )
                                )
                                test = control["test"] if index == 0 else mutant["test"]
                                result = invoke(
                                    mutant["package"],
                                    test,
                                    overlay,
                                    args.seconds_per_command,
                                    toolchain,
                                )
                                if result["status"] == "passed":
                                    result["status"] = "survived"
                        row = {
                            "id": control["id"] if index == 0 else mutant["id"],
                            "file": mutant["file"],
                            "intent": mutant["intent"],
                            **result,
                        }
                        if index == 0:
                            summary["control"] = row
                        else:
                            summary["mutants"].append(row)
        summary["counts"] = {
            status: sum(m["status"] == status for m in summary["mutants"])
            for status in ("killed", "survived", "invalid", "timeout", "infrastructure")
        }
        if "result" not in summary:
            summary["result"] = (
                "passed"
                if (
                    summary["control"]["status"] == "survived"
                    and all(m["status"] == "killed" for m in summary["mutants"])
                )
                else "failed"
            )
        fcntl.flock(lock, fcntl.LOCK_UN)
    summary["elapsed_seconds"] = round(time.monotonic() - started, 2)
    args.summary.parent.mkdir(parents=True, exist_ok=True)
    args.summary.write_text(json.dumps(summary, indent=2) + "\n")
    print(
        json.dumps(
            {
                "result": summary["result"],
                "counts": summary["counts"],
                "control": (summary["control"] or {}).get("status"),
                "summary": str(args.summary),
            },
            indent=2,
        )
    )
    return 0 if summary["result"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
