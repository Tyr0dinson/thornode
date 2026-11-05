import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "docs-seo.py"

BOOK_TOML = """[book]
title = "Sample Docs"
description = "Sample book description."
src = ""

[output.html]
site-url = "{site}"
"""

SUMMARY = """# Summary

- [Introduction](README.md)

  - [Guide & Tips](guide.md)
  - [Math](math.md#top)

- [Clients](clients/README.md)
  - [Chains](clients/chains.md)
  - [Draft]()

- [New Chains](new/readme.md)
  - [External](https://example.com/page)

- [Decisions](architecture/README.md)
  - [Template](architecture/TEMPLATE.md)
  - [ADR 001](architecture/adr-001.md)

- [Archived](archived/archived.md)
  - [Old Guide](archived/old.md)
"""

LONG = (
    "The first sentence of this paragraph is long enough to keep on its own. "
    "The second sentence pushes the paragraph well past the limit, so the "
    "description has to be cut at the end of the first sentence."
)
NO_STOP = "word " * 60

PAGES = {
    "README.md": "<p>Welcome to the sample docs, a short book used by tests.</p>",
    "guide.md": "<p>Short intro.</p>\n<div><p>Nested paragraph is not used here at all, too deep.</p></div>\n<p>"
    + LONG
    + "</p>",
    "math.md": '<p>Formula <span class="katex">x = y</span> is skipped by the parser.</p>\n<p>Tiny.</p>',
    "clients/README.md": "<p>" + NO_STOP + "</p>",
    "clients/chains.md": "<p>Chains &amp; clients <br> are listed\n here with <a href='x.html'>links</a>.</p>",
    "new/readme.md": "<pre>code only</pre>",
    "architecture/README.md": "<p>Decision records explain why the protocol changed.</p>",
    "architecture/TEMPLATE.md": "<p>A blank skeleton for writing a new decision record.</p>",
    "architecture/adr-001.md": "<p>The first decision record, kept here for history.</p>",
    "archived/archived.md": "<p>Archived pages that are kept for reference only.</p>",
    "archived/old.md": "<p>An old guide that should not appear in the full text.</p>",
}

PAGE = """<!DOCTYPE HTML>
<html>
    <head>
        <meta name="description" content="Sample book description.">
        <!-- thorchain-seo -->
        <meta name="viewport" content="width=device-width">
    </head>
    <body>
        <main>
            <link rel="stylesheet" href="katex.css">
<h1>Heading</h1>
{body}
        </main>
    </body>
</html>
"""


class DocsSeoTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.book = self.root / "docs"
        self.out = self.root / "public"
        self.write(
            self.book / "book.toml", BOOK_TOML.format(site="https://dev.example.org")
        )
        self.write(self.book / "SUMMARY.md", SUMMARY)
        for md, body in PAGES.items():
            self.write(self.book / md, f"# {md}\n\nSource of {md}.\n")
            html = md[:-3] + ".html"
            html = html.replace("README.html", "index.html").replace(
                "readme.html", "index.html"
            )
            self.write(self.out / html, PAGE.format(body=body))
        self.write(
            self.book / "guide.md",
            "# Guide\n\n{{#embed https://example.com/code.sol }}\n",
        )
        self.write(self.out / "print.html", PAGE.format(body="<p>print</p>"))
        self.write(self.out / "404.html", PAGE.format(body="<p>Not found</p>"))

    def tearDown(self):
        self.tmp.cleanup()

    def write(self, path, text):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def run_script(self, *extra):
        return subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--book",
                str(self.book),
                "--out",
                str(self.out),
                *extra,
            ],
            capture_output=True,
            text=True,
        )

    def read(self, path):
        return (self.out / path).read_text()

    def test_head_tags(self):
        result = self.run_script("--strict")
        self.assertEqual(result.returncode, 0, result.stderr)
        page = self.read("guide.html")
        url = "https://dev.example.org/guide.html"
        self.assertTrue(f'<link rel="canonical" href="{url}">' in page)
        self.assertTrue(f'<meta property="og:url" content="{url}">' in page)
        self.assertTrue('<meta property="og:title" content="Guide &amp; Tips">' in page)
        self.assertTrue('<meta property="og:site_name" content="Sample Docs">' in page)
        self.assertTrue('<meta property="og:type" content="website">' in page)
        self.assertTrue('<meta name="twitter:card" content="summary">' in page)
        self.assertFalse("thorchain-seo" in page)
        self.assertEqual(page.count('name="description"'), 1)

        root = self.read("index.html")
        self.assertTrue(
            '<link rel="canonical" href="https://dev.example.org/">' in root
        )
        self.assertTrue('<meta property="og:title" content="Sample Docs">' in root)
        self.assertTrue(
            '<meta name="description" content="Welcome to the sample docs, a short book used by tests.">'
            in root
        )
        self.assertTrue(
            '<link rel="canonical" href="https://dev.example.org/new/index.html">'
            in self.read("new/index.html")
        )

        printed = self.read("print.html")
        self.assertFalse("thorchain-seo" in printed)
        self.assertFalse("canonical" in printed)

    def description(self, path):
        page = self.read(path)
        return re.search(r'<meta name="description" content="([^"]*)">', page).group(1)

    def test_descriptions(self):
        self.run_script()
        first = LONG.split(". ")[0] + "."
        self.assertEqual(self.description("guide.html"), first)
        self.assertEqual(self.description("math.html"), "Tiny.")
        word_cut = self.description("clients/index.html")
        self.assertTrue(word_cut.endswith("word…"))
        self.assertTrue(len(word_cut) <= 160)
        self.assertEqual(
            self.description("clients/chains.html"),
            "Chains &amp; clients are listed here with links.",
        )
        self.assertEqual(self.description("new/index.html"), "Sample book description.")

    def test_descriptions_skip_adr_header_and_bare_urls(self):
        body = (
            "<h2>Changelog</h2>\n<p>01 Jan 25: First draft of this decision record.</p>\n"
            "<h2>Status</h2>\n<p>Proposed</p>\n"
            '<p><a href="https://example.com/code.sol">https://example.com/code.sol</a></p>\n'
            "<h2>Context</h2>\n<p>The context paragraph explains why this decision is needed.</p>"
        )
        self.write(self.out / "clients/chains.html", PAGE.format(body=body))
        self.run_script()
        self.assertEqual(
            self.description("clients/chains.html"),
            "The context paragraph explains why this decision is needed.",
        )

    def test_robots_and_sitemap(self):
        self.run_script()
        self.assertEqual(
            self.read("robots.txt"),
            "User-agent: *\nAllow: /\n\nSitemap: https://dev.example.org/sitemap.xml\n",
        )
        sitemap = self.read("sitemap.xml")
        locs = [
            line.split("<loc>")[1].split("</loc>")[0]
            for line in sitemap.splitlines()
            if "<loc>" in line
        ]
        site = "https://dev.example.org/"
        self.assertEqual(
            locs,
            [
                site,
                site + "guide.html",
                site + "math.html",
                site + "clients/index.html",
                site + "clients/chains.html",
                site + "new/index.html",
                site + "architecture/index.html",
                site + "architecture/adr-001.html",
            ],
        )

    def test_robots_skipped_below_root(self):
        self.write(
            self.book / "book.toml", BOOK_TOML.format(site="https://example.org/dev/")
        )
        result = self.run_script()
        self.assertEqual(result.returncode, 0)
        self.assertTrue("robots.txt" in result.stderr)
        self.assertFalse((self.out / "robots.txt").exists())
        self.assertTrue(
            "https://example.org/dev/guide.html" in self.read("sitemap.xml")
        )

    def test_llms(self):
        self.run_script()
        llms = self.read("llms.txt")
        self.assertTrue(
            llms.startswith(
                "# Sample Docs\n\n> Sample book description.\n\nThe Optional section"
            )
        )
        self.assertTrue(
            "- [Guide & Tips](https://dev.example.org/guide.html): "
            + LONG.split(". ")[0]
            + ".\n"
            in llms
        )
        for heading in (
            "## Clients",
            "## New Chains",
            "## Introduction",
            "## API reference",
            "## Related",
            "## Optional",
        ):
            self.assertTrue(f"\n{heading}\n" in llms, heading)
        self.assertFalse("## Archived" in llms)
        self.assertFalse("## Decisions" in llms)
        self.assertFalse("TEMPLATE" in llms)
        optional = llms.split("## Optional\n")[1]
        for page in ("architecture/index.html", "adr-001.html"):
            self.assertTrue(page in optional, page)
        self.assertFalse("archived" in llms)
        self.assertTrue(
            "- [New Chains](https://dev.example.org/new/index.html)\n" in llms
        )
        self.assertTrue("https://docs.thorchain.org/llms.txt" in llms)
        self.assertTrue("https://dev.example.org/llms-full.txt" in llms)

    def test_llms_full(self):
        self.run_script()
        full = self.read("llms-full.txt")
        self.assertTrue(
            full.startswith("# Sample Docs\n\n> Sample book description.\n")
        )
        self.assertTrue(
            "---\nSource: https://dev.example.org/\n\n# README.md\n" in full
        )
        self.assertTrue("Embedded source: https://example.com/code.sol" in full)
        self.assertFalse("{{#embed" in full)
        self.assertFalse("archived" in full)
        self.assertFalse("architecture" in full)
        self.assertEqual(full.count("\nSource: "), 6)

    def test_404_is_noindex_without_tags(self):
        self.run_script("--strict")
        page = self.read("404.html")
        self.assertTrue('<meta name="robots" content="noindex">' in page)
        self.assertFalse("canonical" in page)
        self.assertFalse("thorchain-seo" in page)

    def test_failed_check_leaves_site_unchanged(self):
        broken = PAGE.replace("</head>", "").format(body="<p>x</p>")
        self.write(self.out / "math.html", broken)
        before = self.read("guide.html")
        result = self.run_script()
        self.assertEqual(result.returncode, 0)
        self.assertTrue("left unchanged" in result.stdout)
        self.assertEqual(self.read("guide.html"), before)
        self.assertEqual(self.read("math.html"), broken)
        self.assertFalse((self.out / "robots.txt").exists())
        self.assertEqual(self.run_script("--strict").returncode, 1)

    def test_missing_marker_writes_nothing(self):
        self.write(
            self.out / "math.html",
            PAGE.replace("<!-- thorchain-seo -->", "").format(body="<p>x</p>"),
        )
        before = self.read("guide.html")
        result = self.run_script()
        self.assertEqual(result.returncode, 0)
        self.assertTrue("marker" in result.stderr)
        self.assertEqual(self.read("guide.html"), before)
        self.assertFalse((self.out / "sitemap.xml").exists())
        self.assertEqual(self.run_script("--strict").returncode, 1)

    def test_missing_page_writes_nothing(self):
        (self.out / "math.html").unlink()
        result = self.run_script()
        self.assertEqual(result.returncode, 0)
        self.assertTrue("not found" in result.stderr)
        self.assertFalse((self.out / "llms.txt").exists())

    def test_second_run_changes_nothing(self):
        self.run_script("--strict")
        snapshot = {p: p.read_bytes() for p in self.out.rglob("*") if p.is_file()}
        self.run_script()
        after = {p: p.read_bytes() for p in self.out.rglob("*") if p.is_file()}
        self.assertEqual(after, snapshot)

    def test_write_error_rolls_back(self):
        before = {p: p.read_bytes() for p in self.out.rglob("*") if p.is_file()}
        (self.out / "new").chmod(0o500)
        try:
            result = self.run_script()
        finally:
            (self.out / "new").chmod(0o700)
        if os.geteuid() == 0:
            self.skipTest("root ignores directory permissions")
        self.assertTrue("rolled back" in result.stderr)
        after = {p: p.read_bytes() for p in self.out.rglob("*") if p.is_file()}
        self.assertEqual(after, before)

    def test_missing_out_dir_is_fatal(self):
        result = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--book",
                str(self.book),
                "--out",
                str(self.root / "nope"),
            ],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
