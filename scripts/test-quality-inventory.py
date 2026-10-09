#!/usr/bin/env python3
"""Inventory Go modules, build constraints, and test files in the checkout."""

import argparse
import json
from pathlib import Path

EXCLUDED = {
    ".git",
    ".cache",
    "vendor",
    "node_modules",
    "test-quality-reports",
    "target",
}


def inventory(root):
    modules = []
    constraints = {}
    test_files = {}
    for path in root.rglob("go.mod"):
        if not any(part in EXCLUDED for part in path.parts):
            modules.append(str(path.parent.relative_to(root)))
    for path in root.rglob("*.go"):
        if any(part in EXCLUDED for part in path.parts):
            continue
        relative = str(path.relative_to(root))
        with path.open(errors="replace") as source:
            first_lines = [source.readline() for _ in range(12)]
        for line in first_lines:
            if line.startswith("//go:build "):
                constraints[relative] = line.removeprefix("//go:build ").strip()
                break
        if path.name.endswith("_test.go"):
            package = str(path.parent.relative_to(root))
            test_files[package] = test_files.get(package, 0) + 1
    return {
        "modules": sorted(modules),
        "build_constraints": dict(sorted(constraints.items())),
        "test_file_counts_by_directory": dict(sorted(test_files.items())),
    }


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    result = inventory(Path("."))
    destination = Path(args.output)
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(result, indent=2) + "\n")
    print(
        f"Inventory: {len(result['modules'])} modules, {len(result['build_constraints'])} tagged files, "
        f"{len(result['test_file_counts_by_directory'])} test directories"
    )
