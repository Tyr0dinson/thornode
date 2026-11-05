#!/usr/bin/env python3
"""Add search and AI discovery data to the built developer docs site.

`make docs-generate` runs this after `mdbook build`. It is warn-only there: a
failure prints a warning and the docs still publish (--strict makes any
warning fail, for tests). It writes only into the output directory and never
edits the sources under docs/.

- The host comes only from `[output.html] site-url` in docs/book.toml.
- docs/theme/index.hbs must keep the `<!-- thorchain-seo -->` marker after the
  description meta; on each page it is replaced with canonical and OpenGraph
  tags.
- Each page's meta description is its first paragraph (cut near 160 chars),
  falling back to the book description.
- robots.txt points at sitemap.xml; sitemap.xml, llms.txt and llms-full.txt
  follow docs/SUMMARY.md order and paths.
"""

import argparse
import html
import os
import posixpath
import re
import sys
import tomllib
from dataclasses import dataclass, field
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit
from xml.etree import ElementTree
from xml.sax.saxutils import escape as xml_escape

MARKER = "<!-- thorchain-seo -->"
MARKER_LINE_RE = re.compile(r"^([ \t]*)" + re.escape(MARKER) + r"\n?", re.MULTILINE)
DESCRIPTION_RE = re.compile(r'<meta name="description" content="[^"]*">')
SUMMARY_LINK_RE = re.compile(
    r"^(\s*)(?:[-*+]\s+)?\[((?:\\.|[^\]\\])*)\]\(([^)]*)\)\s*$"
)
EMBED_RE = re.compile(r"^[ \t]*\{\{#embed\s+(\S+)\s*\}\}[ \t]*$", re.MULTILINE)
# Sections whose paragraphs are bookkeeping, not a summary (ADR headers).
SKIP_SECTIONS = {"changelog", "status"}
URL_ONLY_RE = re.compile(r"^\S+://\S+$")
VOID_TAGS = {
    "area",
    "base",
    "br",
    "col",
    "embed",
    "hr",
    "img",
    "input",
    "link",
    "meta",
    "source",
    "track",
    "wbr",
}

# Retired features: built, but left out of the sitemap and both llms files.
ARCHIVED_ROOTS = {"archived/archived.md"}
# Decision records: under "## Optional" in llms.txt, left out of llms-full.txt.
HISTORY_ROOTS = {"architecture/README.md"}
# Pages that are not content (a blank ADR skeleton): built, but listed nowhere.
UNLISTED = {"architecture/TEMPLATE.md"}
NOINDEX = '<meta name="robots" content="noindex">'

# The same API descriptions docs.thorchain.org lists in /.well-known/api-catalog.
API_LINKS = [
    (
        "THORNode OpenAPI spec",
        "https://gateway.liquify.com/chain/thorchain_api/thorchain/doc/openapi.yaml",
        "THORNode REST API, OpenAPI 3.0",
    ),
    (
        "Midgard API",
        "https://gateway.liquify.com/chain/thorchain_midgard/v2/swagger.json",
        "Midgard REST API, OpenAPI 3.0",
    ),
]
USER_DOCS_LLMS = "https://docs.thorchain.org/llms.txt"

warning_count = 0


def warn(message):
    """Print a warning to stderr and count it."""
    global warning_count
    warning_count += 1
    print(f"docs-seo: warning: {message}", file=sys.stderr)


@dataclass
class Chapter:
    """A SUMMARY.md entry; md is None for draft and external links."""

    title: str
    md: str | None
    children: list = field(default_factory=list)

    def walk(self):
        """Yield this entry and all its descendants in SUMMARY order."""
        yield self
        for child in self.children:
            yield from child.walk()


def load_config(book):
    """Return title, description, site URL and source dir from book.toml."""
    path = book / "book.toml"
    if not path.is_file():
        sys.exit(f"docs-seo: error: {path} not found")
    config = tomllib.loads(path.read_text())
    meta = config.get("book", {})
    site = config.get("output", {}).get("html", {}).get("site-url", "")
    parts = urlsplit(site)
    if parts.scheme not in ("http", "https") or not parts.netloc:
        sys.exit("docs-seo: error: [output.html] site-url must be an absolute URL")
    return {
        "title": meta.get("title", ""),
        "description": meta.get("description", ""),
        "site": site.rstrip("/") + "/",
        "src": book / meta.get("src", "src"),
    }


def parse_summary(path):
    """Parse SUMMARY.md into a list of top-level chapters."""
    if not path.is_file():
        sys.exit(f"docs-seo: error: {path} not found")
    roots, stack = [], []
    for line in path.read_text().splitlines():
        match = SUMMARY_LINK_RE.match(line.expandtabs(4))
        if not match:
            continue
        indent = len(match.group(1))
        title = re.sub(r"\\(.)", r"\1", match.group(2)).strip()
        target = unquote(match.group(3).split("#")[0].strip())
        if not target or "://" in target or target.startswith("mailto:"):
            target = None
        else:
            target = posixpath.normpath(target)
        chapter = Chapter(title, target)
        while stack and stack[-1][0] >= indent:
            stack.pop()
        (stack[-1][1].children if stack else roots).append(chapter)
        stack.append((indent, chapter))
    return roots


def html_path(md):
    """Map a markdown path to mdBook's output path (README becomes index)."""
    directory, name = posixpath.split(md)
    if name.lower() == "readme.md":
        name = "index.md"
    return posixpath.join(directory, name[: -len(".md")] + ".html")


class ParagraphParser(HTMLParser):
    """Collect (text, has_katex, section) for each <p> directly under <main>.

    section is the text of the closest preceding <h2>, lower-cased.
    """

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.stack = []
        self.main_depth = None
        self.done = False
        self.text = None
        self.katex = False
        self.section = ""
        self.heading = None
        self.paragraphs = []

    def handle_starttag(self, tag, attrs):
        """Track nesting and start collecting at a top-level <p>."""
        self.handle_startendtag(tag, attrs)
        if tag in VOID_TAGS:
            return
        self.stack.append(tag)
        depth = len(self.stack)
        if tag == "main" and self.main_depth is None:
            self.main_depth = depth
        elif tag == "h2" and not self.done and self.main_depth is not None:
            self.heading = []
        elif (
            tag == "p"
            and not self.done
            and self.main_depth is not None
            and depth == self.main_depth + 1
        ):
            self.text, self.katex = [], False

    def handle_startendtag(self, tag, attrs):
        """Flag KaTeX markup inside the paragraph being collected."""
        if self.text is not None and "katex" in (dict(attrs).get("class") or ""):
            self.katex = True

    def handle_endtag(self, tag):
        """Close elements up to the matching start tag."""
        if tag not in self.stack:
            return
        while self.stack:
            popped = self.stack.pop()
            depth = len(self.stack) + 1
            if popped == "p" and self.text is not None and depth == self.main_depth + 1:
                text = " ".join("".join(self.text).split())
                self.paragraphs.append((text, self.katex, self.section))
                self.text = None
            if popped == "h2" and self.heading is not None:
                self.section = " ".join("".join(self.heading).split()).lower()
                self.heading = None
            if popped == "main" and depth == self.main_depth:
                self.done = True
            if popped == tag:
                break

    def handle_data(self, data):
        """Collect text inside the current paragraph."""
        if self.text is not None:
            self.text.append(data)
        if self.heading is not None:
            self.heading.append(data)


def shorten(text, limit=160):
    """Cut text to the limit at a sentence end, else at a word with an ellipsis."""
    if len(text) <= limit:
        return text
    end = text.rfind(". ", 0, limit)
    if end + 1 >= 70:
        return text[: end + 1]
    cut = text[: limit - 1].rsplit(" ", 1)[0].rstrip(",;:")
    return cut + "…"


def describe(page_html):
    """Return the page description, or None when no paragraph is usable."""
    parser = ParagraphParser()
    parser.feed(page_html)
    candidates = [
        text
        for text, katex, section in parser.paragraphs
        if text
        and not katex
        and section not in SKIP_SECTIONS
        and not URL_ONLY_RE.match(text)
    ]
    for text in candidates:
        if len(text) >= 50:
            return shorten(text)
    return shorten(candidates[0]) if candidates else None


def head_tags(url, title, description, site_name):
    """Build the canonical, OpenGraph and Twitter tags for one page."""

    def attr(value):
        return html.escape(value, quote=True)

    return [
        f'<link rel="canonical" href="{attr(url)}">',
        '<meta property="og:type" content="website">',
        f'<meta property="og:site_name" content="{attr(site_name)}">',
        f'<meta property="og:title" content="{attr(title)}">',
        f'<meta property="og:description" content="{attr(description)}">',
        f'<meta property="og:url" content="{attr(url)}">',
        '<meta name="twitter:card" content="summary">',
    ]


def inject(page_html, description, tags):
    """Set the description meta and replace the marker with the tags."""
    match = MARKER_LINE_RE.search(page_html)
    if not match:
        return None
    if not DESCRIPTION_RE.search(page_html):
        warn("description meta not found")
    escaped = html.escape(description, quote=True)
    page_html = DESCRIPTION_RE.sub(
        lambda _: f'<meta name="description" content="{escaped}">', page_html, 1
    )
    indent = match.group(1)
    block = "".join(f"{indent}{tag}\n" for tag in tags)
    return MARKER_LINE_RE.sub(lambda _: block, page_html, 1)


def md_link(title, url):
    """Format a markdown link, escaping brackets in the title."""
    title = title.replace("[", r"\[").replace("]", r"\]")
    return f"[{title}]({url})"


def subtree(roots, starts):
    """Return the md paths of the chapters in SUMMARY rooted at any of starts."""
    return {
        c.md for r in roots for top in r.walk() if top.md in starts for c in top.walk()
    }


def llms_entry(page):
    """Format one llms.txt list item."""
    note = "" if page["fallback"] else f": {page['description']}"
    return f"- {md_link(page['title'], page['url'])}{note}"


def build_llms(config, roots, pages):
    """Return llms.txt: a linked index of every page plus API pointers."""
    history = subtree(roots, HISTORY_ROOTS)
    hidden = subtree(roots, ARCHIVED_ROOTS) | UNLISTED
    lines = [
        f"# {config['title']}",
        "",
        f"> {config['description']}",
        "",
        "The Optional section lists architecture decision records, kept for history.",
        "",
    ]
    for root in roots:
        entries = [
            pages[c.md]
            for c in root.walk()
            if c.md in pages and c.md not in history and c.md not in hidden
        ]
        if entries:
            lines += [f"## {root.title}", ""]
            lines += [llms_entry(page) for page in entries] + [""]
    lines += ["## API reference", ""]
    lines += [f"- {md_link(name, url)}: {note}" for name, url, note in API_LINKS]
    lines += [
        "",
        "## Related",
        "",
        f"- {md_link('THORChain Docs', USER_DOCS_LLMS)}: user, economics and node-operator docs",
        f"- {md_link('Full text', config['site'] + 'llms-full.txt')}: the markdown of every page listed here except the Optional ones",
        "",
        "## Optional",
        "",
    ]
    lines += [
        llms_entry(page)
        for md, page in pages.items()
        if md in history and md not in hidden
    ]
    return "\n".join(lines) + "\n"


def build_llms_full(config, roots, pages):
    """Return llms-full.txt: the markdown source of each page in SUMMARY order."""
    excluded = subtree(roots, HISTORY_ROOTS | ARCHIVED_ROOTS) | UNLISTED
    out = [f"# {config['title']}\n\n> {config['description']}\n"]
    for md, page in pages.items():
        if md in excluded:
            continue
        source = config["src"] / md
        if not source.is_file():
            warn(f"source {source} not found")
            continue
        text = EMBED_RE.sub(r"Embedded source: \1", source.read_text())
        out.append(f"\n---\nSource: {page['url']}\n\n{text.rstrip()}\n")
    return "".join(out)


def build_sitemap(roots, pages):
    """Return sitemap.xml listing the canonical URL of every current page."""
    excluded = subtree(roots, ARCHIVED_ROOTS) | UNLISTED
    urls = "".join(
        f"  <url><loc>{xml_escape(p['url'])}</loc></url>\n"
        for md, p in pages.items()
        if md not in excluded
    )
    return (
        '<?xml version="1.0" encoding="UTF-8"?>\n'
        '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n'
        f"{urls}</urlset>\n"
    )


def check(writes, chapter_files):
    """Return a list of problems with the output; empty means safe to write."""
    problems = []
    for file, text in writes.items():
        if file.suffix != ".html":
            continue
        head_end = text.find("</head>")
        if head_end < 0 or MARKER in text:
            problems.append(f"{file}: bad head after injection")
        elif file in chapter_files and text.count('rel="canonical"', 0, head_end) != 1:
            problems.append(f"{file}: expected one canonical tag in the head")
    if "sitemap.xml" in {f.name for f in writes}:
        sitemap = next(t for f, t in writes.items() if f.name == "sitemap.xml")
        try:
            if not len(ElementTree.fromstring(sitemap)):
                problems.append("sitemap.xml has no URLs")
        except ElementTree.ParseError as err:
            problems.append(f"sitemap.xml does not parse: {err}")
    return problems


def write_atomic(file, text):
    """Replace file with text so readers never see a half-written file."""
    tmp = file.with_name(file.name + ".tmp")
    try:
        tmp.write_text(text)
        os.replace(tmp, file)
    finally:
        tmp.unlink(missing_ok=True)


def write_all(writes):
    """Write every file, or on any error put back what was already written."""
    originals = {f: f.read_text() for f in writes if f.is_file()}
    done = []
    try:
        for file, text in writes.items():
            write_atomic(file, text)
            done.append(file)
    except OSError as err:
        for file in done:
            if file in originals:
                write_atomic(file, originals[file])
            else:
                file.unlink(missing_ok=True)
        return str(err)
    return None


def main():
    """Inject head tags into the built site and write the discovery files.

    Everything is built in memory and checked first; nothing in the output
    directory changes unless the whole result passes.
    """
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--book", type=Path, required=True, help="dir with book.toml")
    parser.add_argument("--out", type=Path, required=True, help="mdbook output dir")
    parser.add_argument("--strict", action="store_true", help="fail on any warning")
    args = parser.parse_args()

    config = load_config(args.book)
    if not args.out.is_dir():
        sys.exit(f"docs-seo: error: output dir {args.out} not found")
    roots = parse_summary(config["src"] / "SUMMARY.md")

    writes = {}  # output file -> new content
    problems = []  # anything here means nothing is written
    pages = {}  # md path -> page info, SUMMARY order, de-duplicated
    for chapter in (c for r in roots for c in r.walk()):
        if chapter.md is None or chapter.md in pages:
            continue
        path = html_path(chapter.md)
        file = args.out / path
        if not file.is_file():
            problems.append(f"{file} not found")
            continue
        root_page = path == "index.html"
        page_html = file.read_text()
        description = describe(page_html)
        pages[chapter.md] = page = {
            "title": config["title"] if root_page else chapter.title,
            "url": config["site"] + ("" if root_page else path),
            "description": description or config["description"],
            "fallback": description is None,
        }
        tags = head_tags(
            page["url"], page["title"], page["description"], config["title"]
        )
        updated = inject(page_html, page["description"], tags)
        if updated is None:
            problems.append(f"{file}: marker {MARKER} not found")
        else:
            writes[file] = updated

    # Other pages (print, toc, 404) get no tags; the 404 page is kept out of search.
    chapter_files = set(writes)
    for file in args.out.rglob("*.html"):
        if file in chapter_files:
            continue
        text = file.read_text()
        if MARKER in text:
            match = MARKER_LINE_RE.search(text)
            noindex = f"{match.group(1)}{NOINDEX}\n" if file.name == "404.html" else ""
            writes[file] = MARKER_LINE_RE.sub(lambda _: noindex, text, 1)

    files = {
        "sitemap.xml": build_sitemap(roots, pages),
        "llms.txt": build_llms(config, roots, pages),
        "llms-full.txt": build_llms_full(config, roots, pages),
    }
    if urlsplit(config["site"]).path == "/":
        files["robots.txt"] = (
            f"User-agent: *\nAllow: /\n\nSitemap: {config['site']}sitemap.xml\n"
        )
    else:
        warn("site-url is not at the host root, so robots.txt is not written")
    writes.update({args.out / name: text for name, text in files.items()})

    problems += check(writes, chapter_files)
    if not problems:
        error = write_all(writes)
        if error:
            problems.append(f"write failed, changes rolled back: {error}")
    if problems:
        for problem in problems:
            warn(problem)
        print("docs-seo: output failed checks; the site is left unchanged")
        sys.exit(1 if args.strict else 0)

    print(
        f"docs-seo: {len(pages)} pages, wrote {', '.join(sorted(files))}, "
        f"{warning_count} warning(s)"
    )
    if args.strict and warning_count:
        sys.exit(1)


if __name__ == "__main__":
    main()
