"""Skip decisions of scripts/ci-affected.py.

The Go dependency graph is stubbed, so no go toolchain is needed except for the
one test that runs `go list` on a throwaway module.

Run with: python3 -m unittest discover -s build/scripts/tests -v
"""

import importlib.util
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

SCRIPT = Path(__file__).resolve().parents[3] / "scripts/ci-affected.py"
spec = importlib.util.spec_from_file_location("ci_affected", SCRIPT)
ca = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ca)

REGTEST = ca.JOBS["test-regression"]


def write(root, *paths):
    for p in paths:
        f = root / p
        f.parent.mkdir(parents=True, exist_ok=True)
        f.write_text("package x\n" if p.endswith(".go") else "x\n")


class RelevantTest(unittest.TestCase):
    pkgs = {"bifrost/a", "bifrost/a/sub", "bifrost/b", "x/thorchain"}
    deps = {"bifrost/a", "x/thorchain"}

    def why(self, path, job=REGTEST, embeds=()):
        return ca.relevant(path, job, self.pkgs, self.deps, set(embeds))

    def test_docs_are_irrelevant_everywhere(self):
        for path in ("README.md", "x/thorchain/notes.md", "docs/book.toml"):
            with self.subTest(path):
                self.assertIsNone(self.why(path))

    def test_paths_outside_prunable_prefixes_run(self):
        for path in (
            "go.mod",
            "Makefile",
            ".gitlab-ci.yml",
            "x/thorchain/h.go",
            "build/docker/Dockerfile",
        ):
            with self.subTest(path):
                self.assertEqual(self.why(path), "outside the prunable paths")

    def test_non_dependency_package_is_irrelevant(self):
        self.assertIsNone(self.why("bifrost/b/client.go"))
        self.assertIsNone(self.why("bifrost/b/testdata/fixture.json"))

    def test_dependency_package_and_its_data_run(self):
        self.assertEqual(self.why("bifrost/a/x.go"), "in dependency package bifrost/a")
        # nearest package of abi/router.json is bifrost/a
        self.assertEqual(
            self.why("bifrost/a/abi/router.json"), "in dependency package bifrost/a"
        )

    def test_nearest_package_wins(self):
        # bifrost/a/sub is its own package and not a dependency
        self.assertIsNone(self.why("bifrost/a/sub/y.go"))

    def test_embedded_file_runs_even_outside_dependency_package(self):
        self.assertEqual(
            self.why("bifrost/b/data.json", embeds={"bifrost/b/data.json"}),
            "embedded by a dependency",
        )

    def test_file_with_no_enclosing_package_is_irrelevant_under_prunable_prefix(self):
        self.assertIsNone(self.why("bifrost/config.yaml"))
        self.assertIsNone(self.why("test/simulation/pkg/evm/evm.go"))


class PackageDirsTest(unittest.TestCase):
    def test_skips_nested_modules_testdata_and_hidden_dirs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write(
                root,
                "go.mod",
                "app/app.go",
                "bifrost/a/a.go",
                "bifrost/a/testdata/t.go",
                "test/simulation/go.mod",
                "test/simulation/pkg/s.go",
                ".git/hooks/h.go",
                "docs/README.md",
            )
            self.assertEqual(ca.package_dirs(root), {"app", "bifrost/a"})


class DecideTest(unittest.TestCase):
    def decide(self, job, files):
        with mock.patch.object(
            ca, "package_dirs", return_value={"bifrost/a", "bifrost/b"}
        ), mock.patch.object(
            ca, "go_deps", return_value=({"bifrost/a"}, set())
        ) as deps:
            return ca.decide(job, files), deps.called

    def test_skip_needs_every_file_irrelevant(self):
        self.assertEqual(
            self.decide("test-regression", ["bifrost/b/x.go", "README.md"]),
            (False, True),
        )
        self.assertEqual(
            self.decide("test-regression", ["bifrost/b/x.go", "bifrost/a/x.go"]),
            (True, True),
        )

    def test_docs_only_does_not_load_the_graph(self):
        self.assertEqual(
            self.decide("test-simulation", ["docs/a.md", "README.md"]), (False, False)
        )

    def test_simulation_runs_on_any_code_change(self):
        self.assertEqual(
            self.decide("test-simulation", ["bifrost/b/x.go"]), (True, False)
        )

    def test_unknown_job_and_unknown_changes_run(self):
        self.assertTrue(self.decide("lint", ["README.md"])[0])
        self.assertTrue(self.decide("test-regression", None)[0])
        self.assertTrue(self.decide("test-regression", [])[0])


class GoDepsTest(unittest.TestCase):
    @unittest.skipUnless(shutil.which("go"), "go not installed")
    def test_reads_dirs_and_embeds_from_go_list(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            (root / "go.mod").write_text("module example.com/m\n\ngo 1.22\n")
            (root / "lib").mkdir()
            (root / "lib/lib.go").write_text(
                'package lib\n\nimport _ "embed"\n\n//go:embed data.txt\nvar Data string\n'
            )
            (root / "lib/data.txt").write_text("hi\n")
            (root / "lib/lib_test.go").write_text(
                'package lib\n\nimport _ "example.com/m/testonly"\n'
            )
            (root / "testonly").mkdir()
            (root / "testonly/t.go").write_text("package testonly\n")
            (root / "cmd").mkdir()
            (root / "cmd/main.go").write_text(
                'package main\n\nimport _ "example.com/m/lib"\n\nfunc main() {}\n'
            )
            env = {**os.environ, "GOFLAGS": "-mod=mod", "GOWORK": "off"}
            with mock.patch.dict(os.environ, env):
                dirs, embeds = ca.go_deps(root, [(["./cmd"], "", False)])
                self.assertEqual(dirs, {"cmd", "lib"})
                self.assertEqual(embeds, {"lib/data.txt"})
                dirs, _ = ca.go_deps(root, [(["./lib"], "", True)])
                self.assertTrue("testonly" in dirs)


class ChangedFilesTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)

        def git(*args):
            return subprocess.run(
                ["git", "-c", "user.name=t", "-c", "user.email=t@t", *args],
                cwd=self.root,
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()

        git("init", "-q")
        write(self.root, "a.go", "old.md")
        git("add", ".")
        git("commit", "-qm", "base")
        self.base = git("rev-parse", "HEAD")
        git("mv", "old.md", "new.md")
        write(self.root, "bifrost/b/x.go")
        git("add", ".")
        git("commit", "-qm", "change")

    def changed(self, **env):
        clean = {k: v for k, v in os.environ.items() if not k.startswith("CI_")}
        with mock.patch.dict(os.environ, {**clean, **env}, clear=True):
            return ca.changed_files(self.root)

    def test_not_an_mr_pipeline(self):
        self.assertIsNone(self.changed())

    def test_target_branch_sha_preferred_and_renames_list_both_paths(self):
        files = self.changed(
            CI_MERGE_REQUEST_IID="1",
            CI_MERGE_REQUEST_TARGET_BRANCH_SHA=self.base,
            CI_MERGE_REQUEST_DIFF_BASE_SHA="0" * 40,
        )
        self.assertEqual(sorted(files), ["bifrost/b/x.go", "new.md", "old.md"])

    def test_unknown_base_runs(self):
        self.assertIsNone(
            self.changed(
                CI_MERGE_REQUEST_IID="1", CI_MERGE_REQUEST_DIFF_BASE_SHA="f" * 40
            )
        )
        self.assertIsNone(self.changed(CI_MERGE_REQUEST_IID="1"))


class CliTest(unittest.TestCase):
    def run_cli(self, *args, **env):
        clean = {k: v for k, v in os.environ.items() if not k.startswith("CI_")}
        return subprocess.run(
            ["python3", str(SCRIPT), *args],
            env={**clean, **env},
            capture_output=True,
            text=True,
            timeout=60,
        )

    def test_exit_codes(self):
        self.assertEqual(
            self.run_cli("test-simulation", "--files", "README.md").returncode, 10
        )
        self.assertEqual(
            self.run_cli("test-simulation", "--files", "go.mod").returncode, 0
        )
        forced = self.run_cli(
            "test-simulation", "--files", "README.md", FORCE_ALL_JOBS="1"
        )
        self.assertEqual(forced.returncode, 0)
        self.assertTrue("FORCE_ALL_JOBS=1" in forced.stdout)
        # outside an MR pipeline the job always runs
        self.assertEqual(self.run_cli("test-simulation").returncode, 0)


if __name__ == "__main__":
    unittest.main()
