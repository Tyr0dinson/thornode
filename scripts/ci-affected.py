#!/usr/bin/env python3
"""Decide whether an MR pipeline can skip a long CI job.

A job is skipped only when every file the MR changes is irrelevant to it:

  * documentation (*.md, docs/**) is irrelevant to every job listed here, and
  * a file under one of the job's prunable prefixes is irrelevant when the
    nearest enclosing Go package of the main module is not a dependency of
    what the job builds or tests, and no dependency embeds the file.

Anything else (go.mod, Makefile, CI config, build/, unknown paths, ...) runs the
job. So does every pipeline that is not an MR pipeline, a missing or unknown
base commit, a failing `go list`, FORCE_ALL_JOBS=1, and a job not listed in JOBS.
Every error runs the job.

Exit status: 0 run the job, 10 skip it. Anything else means an error; CI treats
it as "run".

Usage: scripts/ci-affected.py [job]           (default: $CI_JOB_NAME)
       scripts/ci-affected.py job --files f1 f2 ...   (offline check, no git)
Tests: build/scripts/tests/test_ci_affected.py
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

SKIP = 10
ROOT = Path(__file__).resolve().parent.parent

# go_roots: (packages, build tags, include test dependencies)
JOBS = {
    # builds cmd/thornode and the regtest driver; bifrost runs nowhere in it
    "test-regression": {
        "go_roots": [
            (["./cmd/thornode"], "mocknet,regtest", False),
            (["./test/regression/cmd"], "mocknet", False),
        ],
        "prunable": ["bifrost/", "test/simulation/"],
    },
    # go test ./app -run TestConsensusDeterminism, default tags
    "consensus-determinism": {
        "go_roots": [(["./app"], "", True)],
        "prunable": ["bifrost/", "test/regression/", "test/simulation/"],
    },
    # make test-race-bifrost
    "unit-tests-race-bifrost": {
        "go_roots": [(["./bifrost/...", "./x/thorchain/ebifrost"], "mocknet", True)],
        "prunable": ["test/regression/", "test/simulation/"],
    },
    # the cluster sim builds images from most of the tree; skip on docs only
    "test-simulation": {"go_roots": [], "prunable": []},
}


def is_docs(path):
    return path.endswith(".md") or path.startswith("docs/")


def package_dirs(root):
    """Directories of the main module holding Go files, as repo-relative paths.

    Nested modules (their own go.mod) are not part of the main module and are
    left out, as are testdata and vendor directories, which go never builds.
    """
    dirs = set()
    for dirpath, dirnames, filenames in os.walk(root):
        rel = Path(dirpath).relative_to(root).as_posix()
        if rel != "." and "go.mod" in filenames:
            dirnames[:] = []
            continue
        dirnames[:] = [
            d
            for d in dirnames
            if not d.startswith((".", "_")) and d not in ("testdata", "vendor")
        ]
        if any(f.endswith(".go") for f in filenames):
            dirs.add("" if rel == "." else rel)
    return dirs


def go_deps(root, go_roots):
    """(dependency package dirs, embedded files) of the job's roots, repo-relative."""
    dirs, embeds = set(), set()
    for packages, tags, with_tests in go_roots:
        cmd = [
            "go",
            "list",
            "-deps",
            "-json=Dir,Standard,EmbedFiles,TestEmbedFiles,XTestEmbedFiles",
        ]
        if tags:
            cmd.append("-tags=" + tags)
        if with_tests:
            cmd.append("-test")
        out = subprocess.run(
            cmd + packages, cwd=root, check=True, capture_output=True, text=True
        ).stdout
        decoder = json.JSONDecoder()
        idx = 0
        while True:
            while idx < len(out) and out[idx].isspace():
                idx += 1
            if idx >= len(out):
                break
            pkg, idx = decoder.raw_decode(out, idx)
            if pkg.get("Standard"):
                continue
            try:
                rel = Path(pkg["Dir"]).resolve().relative_to(root).as_posix()
            except ValueError:
                continue  # module cache, outside the repo
            rel = "" if rel == "." else rel
            dirs.add(rel)
            for key in ("EmbedFiles", "TestEmbedFiles", "XTestEmbedFiles"):
                for f in pkg.get(key) or []:
                    embeds.add(f"{rel}/{f}" if rel else f)
    return dirs, embeds


def nearest_package(path, pkg_dirs):
    """Nearest directory at or above path's directory that holds a Go package."""
    parts = path.split("/")[:-1]
    while parts:
        d = "/".join(parts)
        if d in pkg_dirs:
            return d
        parts.pop()
    return None


def relevant(path, job, pkg_dirs, dep_dirs, embeds):
    """Why path matters to job, or None when the job can ignore it."""
    if is_docs(path):
        return None
    if not any(path.startswith(p) for p in job["prunable"]):
        return "outside the prunable paths"
    if path in embeds:
        return "embedded by a dependency"
    pkg = nearest_package(path, pkg_dirs)
    if pkg is not None and pkg in dep_dirs:
        return f"in dependency package {pkg}"
    return None


def changed_files(root):
    """Files the MR changes, or None when they cannot be determined."""
    if not os.environ.get("CI_MERGE_REQUEST_IID"):
        print("ci-affected: not an MR pipeline")
        return None
    # merged-result and merge-train pipelines test the target branch tip plus
    # the MR (plus earlier train cars), so diff against that tip
    base = os.environ.get("CI_MERGE_REQUEST_TARGET_BRANCH_SHA") or os.environ.get(
        "CI_MERGE_REQUEST_DIFF_BASE_SHA"
    )
    if not base:
        print("ci-affected: no base commit")
        return None

    def git(*args):
        return subprocess.run(
            ["git", *args], cwd=root, capture_output=True, text=True, timeout=120
        )

    if git("cat-file", "-e", f"{base}^{{commit}}").returncode != 0:
        git("fetch", "--no-tags", "--depth=1", "origin", base)
    diff = git("diff", "--name-only", "--no-renames", base, "HEAD")
    if diff.returncode != 0:
        print(f"ci-affected: git diff against {base} failed: {diff.stderr.strip()}")
        return None
    print(f"ci-affected: changes against {base}")
    return [f for f in diff.stdout.splitlines() if f]


def decide(job_name, files, root=ROOT):
    """Return True when the job must run, printing the reason either way."""
    job = JOBS.get(job_name)
    if job is None:
        print(f"ci-affected: {job_name} has no selection rules")
        return True
    if files is None:
        return True
    if not files:
        print("ci-affected: no changed files")
        return True

    dep_dirs, embeds, pkg_dirs = set(), set(), set()
    needs_graph = [
        f
        for f in files
        if not is_docs(f) and any(f.startswith(p) for p in job["prunable"])
    ]
    if needs_graph:
        pkg_dirs = package_dirs(root)
        dep_dirs, embeds = go_deps(root, job["go_roots"])

    for f in files:
        why = relevant(f, job, pkg_dirs, dep_dirs, embeds)
        if why:
            print(f"ci-affected: {job_name} runs: {f} is {why}")
            return True
    print(
        f"ci-affected: skipping {job_name}; none of the {len(files)} changed files affect it:"
    )
    for f in files:
        print(f"  {f}")
    print("Set FORCE_ALL_JOBS=1 on the pipeline to run it anyway.")
    return False


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("job", nargs="?", default=os.environ.get("CI_JOB_NAME", ""))
    parser.add_argument("--files", nargs="*", help="changed files; skips git")
    args = parser.parse_args()
    if os.environ.get("FORCE_ALL_JOBS") == "1":
        print("ci-affected: FORCE_ALL_JOBS=1")
        return 0
    files = args.files if args.files is not None else changed_files(ROOT)
    return 0 if decide(args.job, files) else SKIP


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:  # any failure runs the job
        print(f"ci-affected: error, running the job: {exc!r}")
        sys.exit(1)
