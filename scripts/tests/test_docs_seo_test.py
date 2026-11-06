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
NO_STOP = "word, " * 60

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
        first = (
            "The first sentence of this paragraph is long enough to keep on its own."
        )
        self.assertTrue(f'<meta property="og:description" content="{first}">' in page)
        # The tags take the marker's indentation and leave no blank line behind.
        self.assertTrue(
            f'        <meta name="description" content="{first}">\n'
            '        <link rel="canonical" href="https://dev.example.org/guide.html">\n'
            '        <meta property="og:type" content="website">\n' in page
        )
        self.assertTrue(
            '        <meta name="twitter:card" content="summary">\n'
            '        <meta name="viewport" content="width=device-width">\n' in page
        )
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
        self.assertFalse("noindex" in printed)

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
        # Each skipped paragraph is over 50 characters, so it would be chosen
        # if the Changelog, Status and bare-URL rules stopped working.
        url = "https://example.com/contracts/aggregators/the/longest/path/code.sol"
        body = (
            "<h2>Changelog</h2>\n"
            "<p>01 Jan 25: First draft of this decision record, written up for review.</p>\n"
            "<h2>Status</h2>\n"
            "<p>Proposed, awaiting a vote by the node operators and the core team.</p>\n"
            "<h2>Context</h2>\n"
            f'<p><a href="{url}">{url}</a></p>\n'
            "<p>The context paragraph explains why this decision is needed.</p>"
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

    def test_archived_and_template_are_noindex(self):
        self.run_script("--strict")
        for path in (
            "archived/archived.html",
            "archived/old.html",
            "architecture/TEMPLATE.html",
        ):
            page = self.read(path)
            self.assertTrue('<meta name="robots" content="noindex">' in page, path)
            self.assertFalse("canonical" in page, path)
            self.assertFalse("og:url" in page, path)
        self.assertTrue(
            '<meta name="description" content="Archived pages that are kept for reference only.">'
            in self.read("archived/archived.html")
        )
        adr = self.read("architecture/adr-001.html")
        self.assertTrue('rel="canonical"' in adr)
        self.assertFalse("noindex" in adr)

    def test_configured_path_missing_from_summary_writes_nothing(self):
        summary = SUMMARY.split("- [Archived]")[0]
        self.write(self.book / "SUMMARY.md", summary)
        before = self.read("guide.html")
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertTrue("archived/archived.md is configured" in result.stderr)
        self.assertEqual(self.read("guide.html"), before)
        self.assertFalse((self.out / "sitemap.xml").exists())

    def test_llms_full_links_are_absolute(self):
        site = "https://dev.example.org/"
        source = "\n".join(
            [
                "# Chains",
                '[up](../guide.md) [index](README.md#top "mention") [here](#sec)',
                "[root](../README.md) [img](../images/a.png) [ext](https://example.com/x.md)",
                "[outside](../../outside.md) [mail](mailto:a@example.com)",
                "[ref]: ../new/readme.md",
                "```text",
                "[code](../guide.md)",
                "```",
                "```admonish info",
                "[note](../math.md)",
                "```",
                "- ```go",
                "  [listcode](../guide.md)",
                "  ```",
                "[after](../guide.md) [dir](../new/) [top](../)",
                "[tel](tel:123) [spaced](<../a b.md>) `[inline](../guide.md)`",
                "~~~",
                "[tilde](../guide.md)",
                "~~~",
                "````",
                "```",
                "[inner](../guide.md)",
                "````",
                "```inline``` text [span](../guide.md)",
                "[parens](../a_(b).md) [dot](.) [dotdot](..)",
                "| table | [cell](../guide.md) |",
            ]
        )
        self.write(self.book / "clients/chains.md", source + "\n")
        self.run_script("--strict")
        full = self.read("llms-full.txt")
        for expected in (
            f"[up]({site}guide.html)",
            f'[index]({site}clients/index.html#top "mention")',
            f"[here]({site}clients/chains.html#sec)",
            f"[root]({site})",
            f"[img]({site}images/a.png)",
            "[ext](https://example.com/x.md)",
            "[outside](../../outside.md)",
            "[mail](mailto:a@example.com)",
            "[ref]: ../new/readme.md",  # reference definitions are not rewritten
            "[code](../guide.md)",
            f"[note]({site}math.html)",
            "[listcode](../guide.md)",
            f"[after]({site}guide.html)",
            f"[dir]({site}new/index.html)",
            f"[top]({site})",
            "[tel](tel:123)",
            f"[spaced](<{site}a b.html>)",
            "`[inline](../guide.md)`",
            "[tilde](../guide.md)",
            "[inner](../guide.md)",
            f"[span]({site}guide.html)",
            f"[parens]({site}a_(b).html)",
            f"[dot]({site}clients/index.html)",
            f"[dotdot]({site})",
            f"[cell]({site}guide.html)",
        ):
            self.assertTrue(expected in full, expected)

    def test_failed_check_leaves_site_unchanged(self):
        broken = PAGE.replace("</head>", "").format(body="<p>x</p>")
        self.write(self.out / "math.html", broken)
        before = self.read("guide.html")
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
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
        self.assertEqual(result.returncode, 1)
        self.assertTrue("marker" in result.stderr)
        self.assertEqual(self.read("guide.html"), before)
        self.assertFalse((self.out / "sitemap.xml").exists())
        self.assertEqual(self.run_script("--strict").returncode, 1)

    def test_missing_page_writes_nothing(self):
        (self.out / "math.html").unlink()
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertTrue("not found" in result.stderr)
        self.assertFalse((self.out / "llms.txt").exists())

    def test_second_run_fails_closed_and_changes_nothing(self):
        self.run_script("--strict")
        snapshot = {p: p.read_bytes() for p in self.out.rglob("*") if p.is_file()}
        # The markers are gone after the first run, so the second fails closed.
        self.assertEqual(self.run_script().returncode, 1)
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
        self.assertEqual(result.returncode, 1)
        self.assertTrue("output dir" in result.stderr, result.stderr)

    def test_strict_fails_on_warning_but_still_writes(self):
        self.write(
            self.book / "book.toml", BOOK_TOML.format(site="https://example.org/dev/")
        )
        result = self.run_script("--strict")
        self.assertEqual(result.returncode, 1)
        self.assertTrue("warning: site-url" in result.stderr)
        self.assertTrue("1 warning(s)" in result.stdout)
        self.assertTrue((self.out / "sitemap.xml").exists())

    def test_bad_config_is_fatal(self):
        for site in ("dev.example.org", "ftp://dev.example.org", "https://"):
            self.write(self.book / "book.toml", BOOK_TOML.format(site=site))
            result = self.run_script()
            self.assertEqual(result.returncode, 1, site)
            self.assertTrue("site-url must be an absolute URL" in result.stderr, site)
        (self.book / "book.toml").unlink()
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertTrue("book.toml not found" in result.stderr)
        self.write(
            self.book / "book.toml", BOOK_TOML.format(site="https://dev.example.org")
        )
        (self.book / "SUMMARY.md").unlink()
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertTrue("SUMMARY.md not found" in result.stderr)
        self.assertFalse((self.out / "sitemap.xml").exists())

    def test_missing_description_meta_writes_nothing(self):
        self.write(
            self.out / "math.html",
            PAGE.replace(
                '<meta name="description" content="Sample book description.">', ""
            ).format(body="<p>x</p>"),
        )
        before = self.read("guide.html")
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertTrue("description meta not found" in result.stderr)
        self.assertEqual(self.read("guide.html"), before)
        self.assertFalse((self.out / "sitemap.xml").exists())

    def test_missing_source_writes_nothing(self):
        (self.book / "math.md").unlink()
        before = self.read("guide.html")
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertTrue("math.md not found" in result.stderr, result.stderr)
        self.assertEqual(self.read("guide.html"), before)
        self.assertFalse((self.out / "llms-full.txt").exists())

    def test_check_rejects_stray_canonical_or_marker(self):
        extra = '<link rel="canonical" href="https://other.example/">\n        <!-- thorchain-seo -->'
        cases = (
            ("math.html", extra, "expected one canonical tag"),
            ("archived/old.html", extra, "expected noindex and no canonical"),
            (
                "math.html",
                "<!-- thorchain-seo -->\n        <!-- thorchain-seo -->",
                "bad head after injection",
            ),
        )
        for path, head, message in cases:
            with self.subTest(message):
                self.tearDown()
                self.setUp()
                before = self.read("guide.html")
                self.write(
                    self.out / path,
                    PAGE.replace("<!-- thorchain-seo -->", head).format(
                        body="<p>x</p>"
                    ),
                )
                result = self.run_script()
                self.assertEqual(result.returncode, 1)
                self.assertTrue(message in result.stderr, result.stderr)
                self.assertEqual(self.read("guide.html"), before)
                self.assertFalse((self.out / "sitemap.xml").exists())

    def test_check_rejects_noindex_outside_head(self):
        # The marker sits after </head>, so noindex would land in the body.
        page = PAGE.replace("        <!-- thorchain-seo -->\n", "").format(
            body="        <!-- thorchain-seo -->\n<p>x</p>"
        )
        self.write(self.out / "archived/old.html", page)
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertTrue("expected noindex and no canonical" in result.stderr)
        self.assertFalse((self.out / "sitemap.xml").exists())

    def test_marker_not_on_its_own_line_writes_nothing(self):
        page = PAGE.format(body="<p>Not found</p>").replace(
            "        <!-- thorchain-seo -->\n", "<p><!-- thorchain-seo --></p>\n"
        )
        self.write(self.out / "404.html", page)
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertTrue("not on its own line" in result.stderr, result.stderr)
        self.assertFalse((self.out / "sitemap.xml").exists())

    def test_empty_summary_writes_nothing(self):
        self.write(self.book / "SUMMARY.md", "# Summary\n")
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertTrue("sitemap.xml has no URLs" in result.stderr)
        self.assertTrue("architecture/README.md is configured" in result.stderr)
        self.assertTrue("architecture/TEMPLATE.md is configured" in result.stderr)
        self.assertFalse((self.out / "llms.txt").exists())

    def test_write_error_removes_new_files(self):
        before = {p: p.read_bytes() for p in self.out.rglob("*") if p.is_file()}
        # A directory in llms.txt's place fails after sitemap.xml was already
        # created, so the rollback has to delete a new file.
        (self.out / "llms.txt").mkdir()
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertTrue("rolled back" in result.stderr)
        self.assertFalse((self.out / "sitemap.xml").exists())
        after = {p: p.read_bytes() for p in self.out.rglob("*") if p.is_file()}
        self.assertEqual(after, before)

    def test_summary_titles_paths_and_duplicates(self):
        summary = (
            SUMMARY.replace("- [Introduction](README.md)", "[Introduction](README.md)")
            .replace(
                "  - [Math](math.md#top)",
                "  - [Math \\[v2\\]](./math.md)\n  - [Math again](math.md)\n  - [Amp](a&b.md)",
            )
            .replace(
                "- [Clients](clients/README.md)", "- [Clients](clients/README%2Emd)"
            )
        )
        self.write(self.book / "a&b.md", "# Amp\n")
        self.write(self.out / "a&b.html", PAGE.format(body="<p>Ampersand page.</p>"))
        self.write(self.book / "SUMMARY.md", summary)
        result = self.run_script("--strict")
        self.assertEqual(result.returncode, 0, result.stderr)
        llms = self.read("llms.txt")
        self.assertTrue(
            "- [Math \\[v2\\]](https://dev.example.org/math.html): Tiny.\n" in llms
        )
        self.assertFalse("Math again" in llms)
        self.assertTrue("\n## Introduction\n" in llms)
        self.assertTrue(
            "- [Clients](https://dev.example.org/clients/index.html)" in llms
        )
        self.assertEqual(self.read("sitemap.xml").count("math.html"), 1)
        self.assertTrue(
            "<loc>https://dev.example.org/a&amp;b.html</loc>"
            in self.read("sitemap.xml")
        )
        self.assertTrue(
            '<meta property="og:title" content="Math [v2]">' in self.read("math.html")
        )

    def test_llms_full_links_more_cases(self):
        site = "https://dev.example.org/"
        source = "\n".join(
            [
                "# Chains",
                "[abs](/guide.md) [proto](//cdn.example.com/x.md) [query](../guide.md?v=1#a)",
                "```admonish note",
                "```go",
                "[innercode](../guide.md)",
                "```",
                "[innernote](../guide.md)",
                "```",
                "```",
                "~~~",
                "[mixed](../guide.md)",
                "```",
                "[afterfence](../guide.md)",
                "> ```",
                "> [quoted](../guide.md)",
                "> ```",
            ]
        )
        self.write(self.book / "clients/chains.md", source + "\n")
        self.run_script("--strict")
        full = self.read("llms-full.txt")
        for expected in (
            f"[abs]({site}guide.html)",
            "[proto](//cdn.example.com/x.md)",
            f"[query]({site}guide.html?v=1#a)",
            "[innercode](../guide.md)",
            f"[innernote]({site}guide.html)",
            "[mixed](../guide.md)",
            "> [quoted](../guide.md)",
        ):
            self.assertTrue(expected in full, expected)


if __name__ == "__main__":
    unittest.main()
