#!/usr/bin/env python3
"""Hide low-level application logs while preserving test diagnostics and exit status."""

import json
import re
import subprocess
import sys

ANSI = re.compile(r"\x1b\[[0-9;]*m")
CONSOLE_LOG = re.compile(
    r"^(?:\d{1,2}:\d{2}(?:AM|PM)|<nil>)\s+(?:TRC|DBG|INF)\s"
    r"|^[DI]\[\d{4}-\d{2}-\d{2}\|\d{2}:\d{2}:\d{2}\.\d{3}\]\s"
)


def is_noise(line):
    """Recognize JSON, zerolog console, and CometBFT debug/info/trace logs."""
    plain = ANSI.sub("", line).strip()
    if plain.startswith("{"):
        try:
            record = json.loads(plain)
        except json.JSONDecodeError:
            return False
        return (
            isinstance(record, dict)
            and record.get("level") in ("trace", "debug", "info")
            and ("message" in record or "msg" in record)
        )
    return CONSOLE_LOG.match(plain) is not None


def main():
    if len(sys.argv) < 2:
        print("usage: test_output.py command [args ...]", file=sys.stderr)
        return 2
    with subprocess.Popen(
        sys.argv[1:],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        errors="replace",
        bufsize=1,
    ) as proc:
        try:
            for line in proc.stdout:
                if not is_noise(line):
                    print(line, end="", flush=True)
            status = proc.wait()
        except KeyboardInterrupt:
            proc.terminate()
            proc.wait()
            return 130
    return status if status >= 0 else 128 - status


if __name__ == "__main__":
    sys.exit(main())
