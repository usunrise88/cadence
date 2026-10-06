#!/usr/bin/env python3
"""Build the HTML edition of the book from docs/tutorial/chapters/*.md (GUIDELINES.md §5.1).

One self-contained page: the chapters' Markdown rendered by a small converter for the subset the book uses,
``annotated`` blocks as annotated figures (the component of ``annotated-prototype.html``) with their screenshots
embedded as data URIs, and the contents taken from OUTLINE.md. The output has no <html>/<head>/<body>: it is
wrapped when it is published as an Artifact.

Usage: python3 build.py [--out PATH]      (stdlib + PyYAML; no network)
"""

from __future__ import annotations

import argparse
import base64
import html
import json
import re
import struct
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml

HERE = Path(__file__).resolve().parent
TUTORIAL = HERE.parent
CHAPTERS = TUTORIAL / "chapters"
OUTLINE = TUTORIAL / "OUTLINE.md"
BOOK_TITLE = "Fine-Tuning ASR Models with Cadence"
SIZE_LIMIT = 16 * 1024 * 1024

# Boxes are blockquotes that start with a bold label (GUIDELINES.md §4).
BOXES = {
    "Note": "note",
    "Tip": "tip",
    "Warning": "warning",
    "Under the hood": "hood",
    "The ML behind it": "ml",
    "In the field": "field",
}


def warn(msg: str) -> None:
    print(f"build.py: {msg}", file=sys.stderr)


# ---------------------------------------------------------------------------------------------------------------
# Inline Markdown


@dataclass
class Ctx:
    """Per-chapter rendering state."""

    chapter: int
    tbd: list[str] = field(default_factory=list)  # visible TBD markers lifted out of HTML comments
    figures: int = 0


CODE_SPAN = re.compile(r"(`+)(.+?)(?<!`)\1(?!`)", re.S)
TBD_TOKEN = re.compile(r"\x02(\d+)\x02")


def tbd_span(text: str) -> str:
    text = re.sub(r"\s+", " ", text).strip()
    rest = re.sub(r"^TBD:?\s*", "", text)
    title = html.escape(rest, quote=True)
    label = "TBD" + (f" — {html.escape(rest)}" if rest else "")
    return f'<span class="tbd" title="{title}">{label}</span>'


def inline(text: str, ctx: Ctx) -> str:
    codes: list[str] = []

    def keep_code(m: re.Match[str]) -> str:
        body = m.group(2)
        if body.startswith(" ") and body.endswith(" ") and body.strip():
            body = body[1:-1]
        codes.append(f"<code>{html.escape(body.replace(chr(10), ' '))}</code>")
        return f"\x01{len(codes) - 1}\x01"

    s = CODE_SPAN.sub(keep_code, text)
    s = html.escape(s, quote=False)
    s = re.sub(
        r"\[([^\]]+)\]\(([^)\s]+)\)",
        lambda m: f'<a href="{html.escape(m.group(2), quote=True)}">{m.group(1)}</a>',
        s,
    )
    s = re.sub(r"\*\*(?=\S)(.+?)(?<=\S)\*\*", r"<strong>\1</strong>", s, flags=re.S)
    s = re.sub(r"(?<![*\w])\*(?=[^\s*])(.+?)(?<=[^\s*])\*(?![*\w])", r"<em>\1</em>", s, flags=re.S)
    s = re.sub(r"\bTBD\b", '<span class="tbd">TBD</span>', s)
    s = TBD_TOKEN.sub(lambda m: tbd_span(ctx.tbd[int(m.group(1))]), s)
    s = re.sub(r"\x01(\d+)\x01", lambda m: codes[int(m.group(1))], s)
    return s.strip()


# ---------------------------------------------------------------------------------------------------------------
# Block Markdown

FENCE = re.compile(r"^( {0,3})(`{3,}|~{3,})\s*(.*?)\s*$")
HEAD = re.compile(r"^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$")
BQ = re.compile(r"^ {0,3}> ?(.*)$")
LI = re.compile(r"^( {0,3})([-*+]|\d{1,9}[.)])( +|$)")
TABLE_SEP = re.compile(r"^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$")
COMMENT = re.compile(r"<!--(.*?)-->", re.S)


def strip_comments(text: str, ctx: Ctx) -> str:
    """Drop HTML comments outside fenced code; a comment that starts with TBD becomes a visible marker."""
    out: list[str] = []
    chunk: list[str] = []
    fence: str | None = None

    def flush() -> None:
        def repl(m: re.Match[str]) -> str:
            body = m.group(1).strip()
            if body.startswith("TBD"):
                ctx.tbd.append(body)
                return f"\x02{len(ctx.tbd) - 1}\x02"
            return ""

        if chunk:
            out.append(COMMENT.sub(repl, "\n".join(chunk)))
            chunk.clear()

    for line in text.split("\n"):
        m = FENCE.match(line)
        if fence is None and m:
            flush()
            fence = m.group(2)[0] * len(m.group(2))
            out.append(line)
        elif fence is not None:
            out.append(line)
            if line.strip().startswith(fence) and set(line.strip()) <= {fence[0]}:
                fence = None
        else:
            chunk.append(line)
    flush()
    # Lines a comment emptied become blank (a trailing space after the text is harmless).
    return "\n".join(line.rstrip() for line in "\n".join(out).split("\n"))


def is_blank(line: str) -> bool:
    return not line.strip()


def starts_block(line: str) -> bool:
    if HEAD.match(line) or FENCE.match(line) or BQ.match(line):
        return True
    m = LI.match(line)
    return bool(m and m.group(3) and (not m.group(2)[0].isdigit() or m.group(2)[:-1] == "1"))


def slug(text: str) -> str:
    text = re.sub(r"<[^>]+>", "", text)
    return re.sub(r"[^a-z0-9]+", "-", html.unescape(text).lower()).strip("-")


def split_row(line: str) -> list[str]:
    s = line.strip()
    if s.startswith("|"):
        s = s[1:]
    if s.endswith("|") and not s.endswith("\\|"):
        s = s[:-1]
    cells, cur, in_code = [], "", False
    for ch in s:
        if ch == "`":
            in_code = not in_code
        if ch == "|" and not in_code:
            cells.append(cur.strip())
            cur = ""
        else:
            cur += ch
    cells.append(cur.strip())
    return cells


Block = tuple[str, str]  # (kind, html): kind is "prose" or "figure"


def parse_blocks(lines: list[str], ctx: Ctx, tight: bool = False, level_shift: int = 1) -> list[Block]:
    blocks: list[Block] = []
    i, n = 0, len(lines)
    while i < n:
        line = lines[i]
        if is_blank(line):
            i += 1
            continue

        m = FENCE.match(line)
        if m:
            indent, fence, info = len(m.group(1)), m.group(2), m.group(3)
            body: list[str] = []
            i += 1
            while i < n:
                close = lines[i].strip()
                if close.startswith(fence[0] * len(fence)) and set(close) <= {fence[0]}:
                    i += 1
                    break
                raw = lines[i]
                lead = len(raw) - len(raw.lstrip(" "))
                body.append(raw[min(indent, lead):])
                i += 1
            lang, _, rest = info.partition(" ")
            if lang == "annotated":
                blocks.append(("figure", annotated_figure(rest, "\n".join(body), ctx)))
            else:
                cls = f' class="language-{html.escape(lang)}"' if lang else ""
                blocks.append(("prose", f"<pre><code{cls}>{html.escape(chr(10).join(body))}</code></pre>"))
            continue

        m = HEAD.match(line)
        if m:
            level = min(6, len(m.group(1)) + level_shift)
            text = inline(m.group(2), ctx)
            hid = f"ch{ctx.chapter}-{slug(text)}"
            blocks.append(("prose", f'<h{level} id="{hid}">{text}</h{level}>'))
            i += 1
            continue

        if BQ.match(line):
            inner: list[str] = []
            while i < n and BQ.match(lines[i]):
                inner.append(BQ.match(lines[i]).group(1))  # type: ignore[union-attr]
                i += 1
            blocks.append(("prose", blockquote(inner, ctx)))
            continue

        m = LI.match(line)
        if m and m.group(3):
            html_list, i = parse_list(lines, i, ctx)
            blocks.append(("prose", html_list))
            continue

        if line.lstrip().startswith("|") and i + 1 < n and TABLE_SEP.match(lines[i + 1]):
            head = split_row(line)
            rows = []
            i += 2
            while i < n and lines[i].lstrip().startswith("|"):
                rows.append(split_row(lines[i]))
                i += 1
            th = "".join(f"<th>{inline(c, ctx)}</th>" for c in head)
            trs = "".join(
                "<tr>" + "".join(f"<td>{inline(c, ctx)}</td>" for c in (r + [""] * len(head))[: len(head)]) + "</tr>"
                for r in rows
            )
            blocks.append(
                ("prose", f'<div class="table-wrap"><table><thead><tr>{th}</tr></thead><tbody>{trs}</tbody></table></div>')
            )
            continue

        para = [line.strip()]
        i += 1
        while i < n and not is_blank(lines[i]) and not starts_block(lines[i]):
            para.append(lines[i].strip())
            i += 1
        text = inline("\n".join(para), ctx)
        if not re.sub(r"<[^>]+>", "", text).strip() and "tbd" not in text:
            continue
        cls = ' class="tbd-note"' if text.startswith('<span class="tbd"') else ""
        blocks.append(("prose", text if tight else f"<p{cls}>{text}</p>"))
    return blocks


def parse_list(lines: list[str], i: int, ctx: Ctx) -> tuple[str, int]:
    first = LI.match(lines[i])
    assert first
    ordered = first.group(2)[-1] in ".)"
    start = int(first.group(2)[:-1]) if ordered else 1
    items: list[list[str]] = []
    loose = False
    n = len(lines)
    while i < n:
        m = LI.match(lines[i])
        if not m or not m.group(3) or (m.group(2)[-1] in ".)") != ordered:
            break
        spaces = len(m.group(3))
        col = m.end() if spaces <= 4 else m.start(3) + 1
        item = [lines[i][col:]]
        i += 1
        while i < n:
            line = lines[i]
            if is_blank(line):
                item.append("")
                i += 1
                continue
            lead = len(line) - len(line.lstrip(" "))
            if lead >= col:
                item.append(line[col:])
                i += 1
                continue
            if item[-1] != "" and not starts_block(line) and not LI.match(line):
                item.append(line.strip())  # lazy continuation of the item's paragraph
                i += 1
                continue
            break
        trailing = 0
        while item and item[-1] == "":
            item.pop()
            trailing += 1
        if "" in item:
            loose = True
        items.append(item)
        if trailing and i < n and LI.match(lines[i]):
            loose = True
        if trailing and not (i < n and LI.match(lines[i])):
            break
    parts = []
    for item in items:
        inner = parse_blocks(item, ctx, tight=not loose)
        parts.append("<li>" + "\n".join(h for _, h in inner) + "</li>")
    tag = "ol" if ordered else "ul"
    attr = f' start="{start}"' if ordered and start != 1 else ""
    return f"<{tag}{attr}>" + "".join(parts) + f"</{tag}>", i


def blockquote(inner: list[str], ctx: Ctx) -> str:
    first = inner[0] if inner else ""
    m = re.match(r"^\*\*(.+?)\*\*\s*[—–-]\s*", first)
    if m and m.group(1) in BOXES:
        label = m.group(1)
        inner = [first[m.end():], *inner[1:]]
        body = "\n".join(h for _, h in parse_blocks(inner, ctx))
        return (
            f'<aside class="box {BOXES[label]}"><div class="eyebrow">{html.escape(label)}</div>{body}</aside>'
        )
    body = "\n".join(h for _, h in parse_blocks(inner, ctx))
    return f"<blockquote>{body}</blockquote>"


# ---------------------------------------------------------------------------------------------------------------
# Annotated figures


def png_size(data: bytes) -> tuple[int, int] | None:
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        return None
    w, h = struct.unpack(">II", data[16:24])
    return w, h


def caption_html(caption: str, ctx: Ctx) -> str:
    m = re.match(r"^(Figure \d+[-–]\d+\.)\s*(.*)$", caption, re.S)
    if m:
        return f"<b>{html.escape(m.group(1))}</b> {inline(m.group(2), ctx)}"
    return inline(caption, ctx)


def plain(text: str) -> str:
    return re.sub(r"\s+", " ", re.sub(r"[`*]", "", text)).strip()


def load_block_yaml(body: str, fid: str) -> dict[str, Any]:
    """Parse a block's YAML; an unquoted top-level value with ': ' in it (an easy slip in alt text) is quoted."""
    try:
        return yaml.safe_load(body) or {}
    except yaml.YAMLError as err:
        fixed = re.sub(
            r"^(\w+): (?![\"'>|])(.*: .*)$",
            lambda m: f"{m.group(1)}: {json.dumps(m.group(2))}",
            body,
            flags=re.M,
        )
        try:
            spec = yaml.safe_load(fixed) or {}
        except yaml.YAMLError:
            raise SystemExit(f"annotated block {fid}: invalid YAML: {err}") from err
        warn(f"annotated block {fid}: a top-level value contains ': ' and should be quoted; read it quoted")
        return spec


def annotated_figure(info: str, body: str, ctx: Ctx) -> str:
    ctx.figures += 1
    attrs = dict(re.findall(r"(\w+)=(\S+)", info))
    spec = load_block_yaml(body, attrs.get("id", "?"))
    fid = attrs.get("id") or spec.get("id") or f"ch{ctx.chapter}-fig{ctx.figures}"
    caption = str(spec.get("caption", ""))
    alt = str(spec.get("alt", ""))
    regions_spec: list[dict[str, Any]] = spec.get("regions") or []
    image = TUTORIAL / str(spec.get("image", f"screens/{fid}.png"))
    meta_path = TUTORIAL / "screens" / f"{fid}.json"
    meta: dict[str, Any] = json.loads(meta_path.read_text()) if meta_path.exists() else {}
    cap = caption_html(caption, ctx)
    html_id = f"fig-{slug(fid)}"

    regions: list[dict[str, Any]] = []
    captured = meta.get("regions") or []
    for k, r in enumerate(regions_spec):
        note = str(r.get("note", "")).strip()
        label = str(r.get("label", ""))
        box = r.get("rect")
        if box is None and r.get("target") is not None:
            match = next((c for c in captured if c.get("target") == r["target"] and c.get("box")), None)
            if match is None and k < len(captured) and captured[k].get("label") == label:
                match = captured[k]
            box = match.get("box") if match else None
        regions.append(
            {
                "label": label,
                "noteHtml": inline(note, ctx),
                "noteText": plain(note),
                "style": str(r.get("style", "focus")),
                "box": box,
            }
        )

    if not image.exists():
        warn(f"figure {fid}: {image.relative_to(TUTORIAL)} does not exist; rendering a placeholder")
        notes = "".join(
            f'<li><span class="num">{k + 1}</span><span><b>{html.escape(r["label"])}</b> — {r["noteHtml"]}</span></li>'
            for k, r in enumerate(regions)
        )
        return (
            f'<figure class="pending" id="{html_id}">'
            f'<div class="pending-stage" role="img" aria-label="{html.escape(alt, quote=True)}">'
            f'<span class="pending-tag">Figure pending</span>'
            f'<span class="pending-what">{html.escape(alt)}</span></div>'
            f"<figcaption>{cap}</figcaption>"
            f'<ol class="pending-notes" aria-label="What the figure will point at">{notes}</ol></figure>'
        )

    data = image.read_bytes()
    size = png_size(data)
    width = int(meta.get("width") or (size[0] if size else 1440))
    height = int(meta.get("height") or (size[1] if size else 900))
    missing = [r["label"] for r in regions if not r["box"]]
    if missing:
        warn(f"figure {fid}: no box for {', '.join(missing)}; those regions are left out")
    regions = [r for r in regions if r["box"]]
    payload = json.dumps({"W": width, "H": height, "regions": regions}, ensure_ascii=False).replace("</", "<\\/")
    uri = "data:image/png;base64," + base64.b64encode(data).decode("ascii")
    keys = f"{html_id}-keys"
    return f"""<figure class="annotated" id="{html_id}" aria-labelledby="{html_id}-cap">
  <div class="stage-wrap">
    <div class="stage" tabindex="0" aria-describedby="{keys}">
      <div class="zoomer">
        <img src="{uri}" width="{width}" height="{height}" alt="{html.escape(alt, quote=True)}">
        <svg viewBox="0 0 {width} {height}" aria-hidden="true"></svg>
      </div>
      <div class="zoomhint" hidden>Esc or click to zoom out</div>
    </div>
    <figcaption id="{html_id}-cap">{cap}
      <span class="legend-keys" id="{keys}"><span class="wide-only">Point at a note to light its region; with the figure focused, <kbd>←</kbd> <kbd>→</kbd> step through the regions, <kbd>Enter</kbd> zooms and <kbd>Esc</kbd> zooms out.</span><span class="narrow-only"> Swipe the cards, or tap a number on the screenshot.</span></span>
    </figcaption>
  </div>
  <div class="notes-wrap">
    <ol class="notes" aria-label="Annotations"></ol>
    <div class="strip-nav"><button type="button" data-step="-1" aria-label="Previous annotation">‹</button><span class="pos" aria-live="polite"></span><button type="button" data-step="1" aria-label="Next annotation">›</button></div>
  </div>
  <script type="application/json" class="fig-data">{payload}</script>
</figure>"""


# ---------------------------------------------------------------------------------------------------------------
# Chapters and the outline


@dataclass
class Chapter:
    number: int
    meta: dict[str, Any]
    sha: str
    body: str


def read_chapter(path: Path) -> Chapter:
    text = path.read_text(encoding="utf-8")
    m = re.match(r"^---\n(.*?)\n---\n", text, re.S)
    if not m:
        raise SystemExit(f"{path}: no YAML front matter")
    meta = yaml.safe_load(m.group(1)) or {}
    raw_sha = re.search(r"^written-against:\s*['\"]?([^'\"\s]+)", m.group(1), re.M)
    return Chapter(int(meta["chapter"]), meta, raw_sha.group(1) if raw_sha else "", text[m.end():])


@dataclass
class TocEntry:
    key: str
    title: str
    status: str
    phase: str = ""


@dataclass
class Part:
    name: str
    entries: list[TocEntry]


def read_outline() -> list[Part]:
    parts: list[Part] = []
    current: Part | None = None
    for line in OUTLINE.read_text(encoding="utf-8").split("\n"):
        h = re.match(r"^## (Part .+|Appendices)$", line)
        if h:
            current = Part(h.group(1), [])
            parts.append(current)
            continue
        if line.startswith("## "):
            current = None
            continue
        if current is None or not line.startswith("|") or TABLE_SEP.match(line):
            continue
        cells = split_row(line)
        if cells[1] in ("Chapter", "Appendix"):
            continue
        if current.name == "Appendices":
            current.entries.append(TocEntry(cells[0], cells[1], cells[2]))
        else:
            current.entries.append(TocEntry(cells[0], cells[1], cells[3], cells[2]))
    return parts


def part_label(name: str) -> str:
    """'Part III · Data at scale (phase 4)' → 'Part III · Data at scale'."""
    return re.sub(r"\s*\(.*$", "", name)


def render_chapter(ch: Chapter, parts: list[Part]) -> str:
    ctx = Ctx(ch.number)
    body = strip_comments(ch.body, ctx)
    lines = body.split("\n")
    # The chapter's own H1 is replaced by the header below.
    for k, line in enumerate(lines):
        if HEAD.match(line) and line.lstrip().startswith("# "):
            lines = lines[:k] + lines[k + 1 :]
            break
    blocks = parse_blocks(lines, ctx, level_shift=1)

    sections: list[str] = []
    run: list[str] = []
    for kind, h in blocks:
        if kind == "figure":
            if run:
                sections.append('<section class="col prose">' + "\n".join(run) + "</section>")
                run = []
            sections.append(f'<section class="wide">{h}</section>')
        else:
            run.append(h)
    if run:
        sections.append('<section class="col prose">' + "\n".join(run) + "</section>")

    m = ch.meta
    status = str(m.get("status", "draft"))
    part = next((p for p in parts if p.name.startswith(f"Part {m.get('part')} ")), None)
    eyebrow = " · ".join(
        x for x in [part_label(part.name) if part else f"Part {m.get('part', '')}", f"Phase {m.get('phase')}"] if x
    )
    sha = html.escape(ch.sha)
    terms = ", ".join(f"<em>{html.escape(str(t))}</em>" for t in m.get("terms") or [])
    return f"""<article class="chapter" id="ch-{ch.number}" aria-labelledby="ch-{ch.number}-title">
  <header class="col chapter-head">
    <div class="eyebrow">{html.escape(eyebrow)} <span class="pill {html.escape(status)}">{html.escape(status)}</span></div>
    <h2 id="ch-{ch.number}-title"><span class="chno">Chapter {ch.number}</span>{inline(str(m.get("title", "")), ctx)}</h2>
    <p class="subtitle">{inline(str(m.get("summary", "")), ctx)}</p>
    <p class="meta-line">Written against Cadence <code>{sha}</code>{" · new terms: " + terms if terms else ""}</p>
  </header>
  {chr(10).join(sections)}
</article>"""


def render_toc(parts: list[Part], have: set[int]) -> str:
    out = ['<nav class="col toc" aria-label="Contents"><h2 class="toc-title">Contents</h2>']
    for p in parts:
        out.append(f'<h3 class="toc-part">{html.escape(part_label(p.name))}</h3><ol class="toc-list">')
        for e in p.entries:
            num = e.key if e.key not in ("", "—") else ""
            title = html.escape(e.title)
            linked = num.isdigit() and int(num) in have
            name = f'<a href="#ch-{num}">{title}</a>' if linked else f"<span>{title}</span>"
            out.append(
                f'<li class="{"has" if linked else "not-yet"}"><span class="toc-num">{html.escape(num)}</span>'
                f'{name}<span class="pill {html.escape(e.status)}">{html.escape(e.status)}</span></li>'
            )
        out.append("</ol>")
    out.append("</nav>")
    return "".join(out)


def git_sha() -> str:
    try:
        return subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"], cwd=TUTORIAL, capture_output=True, text=True, check=True
        ).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return ""


def build() -> str:
    chapters = sorted((read_chapter(p) for p in CHAPTERS.glob("*.md")), key=lambda c: c.number)
    parts = read_outline()
    have = {c.number for c in chapters}
    planned = sum(len(p.entries) for p in parts if p.name != "Appendices")
    sha = git_sha()
    cover = f"""<header class="col cover">
    <div class="eyebrow">{BOOK_TITLE}</div>
    <h1>From Start to <em>Finnish</em></h1>
    <p class="subtitle">A working edition: {len(chapters)} of {planned} chapters drafted. Unfinished places are marked <span class="tbd">TBD</span>.</p>
  </header>"""
    body = "\n".join(render_chapter(c, parts) for c in chapters)
    foot = f'<p class="col meta">Built from <code>docs/tutorial</code>{f" at <code>{sha}</code>" if sha else ""} · method: <code>docs/tutorial/GUIDELINES.md</code></p>'
    return (
        "<title>From Start to Finnish</title>\n"
        f"<style>\n{CSS}</style>\n"
        f'<div class="page">\n{cover}\n{render_toc(parts, have)}\n{body}\n{foot}\n</div>\n'
        f"<script>\n{JS}</script>\n"
    )


# ---------------------------------------------------------------------------------------------------------------
# Page style and the figure component (from annotated-prototype.html, generalised to many figures)

CSS = r"""@import url("https://fonts.googleapis.com/css2?family=Literata:ital,opsz,wght@0,7..72,400;0,7..72,600;1,7..72,400&family=IBM+Plex+Sans+Condensed:wght@500;600&family=IBM+Plex+Mono:wght@400;500&display=swap");
/* Layout: a book page — a reading column for prose, a wide figure band where the screenshot and its numbered notes sit side by side. */
:root {
  --paper: #fbfcfd;      /* Radix slate 1: Cadence's own neutral */
  --ink: #1c2024;        /* slate 12 */
  --ink-soft: #60646c;   /* slate 11 */
  --rule: #dfe3e6;       /* slate 6 */
  --panel: #f0f2f5;      /* slate 3 */
  --accent: #3e63dd;     /* indigo 9: Cadence's accent; a focus region */
  --accent-ink: #3a5bc7; /* indigo 10 */
  --warn: #dc3e42;       /* red 9: a region that spends, deletes or fails */
  --subtle: #8b8d98;     /* slate 9: context regions */
  --shade: rgba(17, 24, 39, 0.42);
  --badge-fg: #ffffff;
  --code-bg: #f0f2f5;
  --tbd-bg: #fff3c4;     /* amber 3 */
  --tbd-ink: #8a5300;    /* amber 11 */
  --tbd-rule: #f3d673;   /* amber 6 */
  --font-body: "Literata", "Iowan Old Style", "Palatino Linotype", Georgia, serif;
  --font-label: "IBM Plex Sans Condensed", "Arial Narrow", system-ui, sans-serif;
  --font-mono: "IBM Plex Mono", ui-monospace, "SFMono-Regular", Menlo, monospace;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    --paper: #111113; --ink: #edeef0; --ink-soft: #b0b4ba; --rule: #363a3f; --panel: #212225;
    --accent: #5472e4; --accent-ink: #9eb1ff; --warn: #ec5d5e; --subtle: #6c6e79;
    --shade: rgba(0, 0, 0, 0.55); --badge-fg: #ffffff; --code-bg: #18191b;
    --tbd-bg: #302008; --tbd-ink: #ffca16; --tbd-rule: #5c3d05; color-scheme: dark;
  }
}
:root[data-theme="dark"] {
  --paper: #111113; --ink: #edeef0; --ink-soft: #b0b4ba; --rule: #363a3f; --panel: #212225;
  --accent: #5472e4; --accent-ink: #9eb1ff; --warn: #ec5d5e; --subtle: #6c6e79;
  --shade: rgba(0, 0, 0, 0.55); --badge-fg: #ffffff; --code-bg: #18191b;
  --tbd-bg: #302008; --tbd-ink: #ffca16; --tbd-rule: #5c3d05; color-scheme: dark;
}
* { box-sizing: border-box; }
html { overflow-x: hidden; }
body { margin: 0; background: var(--paper); color: var(--ink); font: 400 17px/1.62 var(--font-body); }
.page { padding-block: 40px 72px; padding-inline: max(16px, 4vw); }
.col { max-width: 40rem; margin-inline: auto; }
.wide { max-width: 82rem; margin-inline: auto; }
.eyebrow { font: 600 12px/1.2 var(--font-label); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); }
h1 { font: 600 clamp(28px, 4.2vw, 40px)/1.12 var(--font-body); margin: 10px 0 6px; text-wrap: balance; letter-spacing: -0.01em; }
h1 em { font-weight: 400; }
.subtitle { font: 500 15px/1.4 var(--font-label); color: var(--ink-soft); margin: 0 0 28px; }
h2 { font: 600 24px/1.25 var(--font-body); margin: 40px 0 10px; text-wrap: balance; }
h3 { font: 600 21px/1.3 var(--font-body); margin: 36px 0 10px; text-wrap: balance; }
h4 { font: 600 13px/1.3 var(--font-label); letter-spacing: 0.06em; text-transform: uppercase; color: var(--ink-soft); margin: 28px 0 8px; }
p { margin: 0 0 14px; }
p, li, td, th, figcaption { overflow-wrap: break-word; }
a { color: var(--accent-ink); text-underline-offset: 2px; }
code { font: 500 0.86em var(--font-mono); background: var(--code-bg); padding: 0.08em 0.32em; border-radius: 3px; overflow-wrap: anywhere; }
.prose ul, .prose ol { margin: 0 0 14px; padding-left: 1.4em; }
.prose li { margin: 4px 0; }
.prose li > p { margin-bottom: 8px; }
.prose li > aside.box, .prose li > pre { margin-block: 10px 14px; }
.prose blockquote { margin: 18px 0; padding-left: 14px; border-left: 3px solid var(--rule); color: var(--ink-soft); }
.table-wrap { overflow-x: auto; margin: 18px 0 22px; border: 1px solid var(--rule); border-radius: 5px; }
table { border-collapse: collapse; width: 100%; font: 400 15px/1.45 var(--font-label); }
th, td { text-align: left; vertical-align: top; padding: 8px 12px; border-bottom: 1px solid var(--rule); min-width: 7rem; }
th { font-weight: 600; background: var(--panel); }
tbody tr:last-child td { border-bottom: 0; }
td code, th code { font-size: 0.84em; }
.tbd { font: 600 0.82em/1.2 var(--font-label); letter-spacing: 0.02em; color: var(--tbd-ink); background: var(--tbd-bg); border: 1px dashed var(--tbd-rule); border-radius: 3px; padding: 0.05em 0.4em; white-space: normal; }
p.tbd-note { margin: 10px 0 18px; }

/* Cover and contents */
.cover { padding-bottom: 8px; }
.cover h1 { font-size: clamp(34px, 6vw, 56px); }
.toc { border-top: 1px solid var(--rule); border-bottom: 1px solid var(--rule); padding-block: 8px 20px; margin-bottom: 24px; }
.toc-title { font: 600 13px/1.3 var(--font-label); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); margin: 16px 0 4px; }
.toc-part { font: 600 16px/1.3 var(--font-body); margin: 18px 0 6px; }
.toc-list { list-style: none; margin: 0; padding: 0; }
.toc-list li { display: grid; grid-template-columns: 2rem minmax(0, 1fr) auto; gap: 10px; align-items: baseline; padding: 3px 0; font: 500 15px/1.4 var(--font-label); }
.toc-list li.not-yet { color: var(--ink-soft); }
.toc-list a { font-weight: 600; }
.toc-num { color: var(--ink-soft); text-align: right; font-variant-numeric: tabular-nums; }
.pill { display: inline-block; font: 600 11px/1 var(--font-label); letter-spacing: 0.06em; text-transform: uppercase; padding: 4px 7px 3px; border-radius: 999px; border: 1px solid var(--rule); color: var(--ink-soft); vertical-align: 0.1em; white-space: nowrap; }
.pill.draft { color: var(--tbd-ink); background: var(--tbd-bg); border-color: var(--tbd-rule); }
.pill.reviewed, .pill.published { color: var(--badge-fg); background: var(--accent); border-color: var(--accent); }
.eyebrow .pill { margin-left: 6px; letter-spacing: 0.06em; }

/* Chapters */
.chapter { padding-top: 32px; }
.chapter + .chapter { border-top: 1px solid var(--rule); margin-top: 48px; }
.chapter-head h2 { font-size: clamp(28px, 4vw, 36px); line-height: 1.15; margin: 10px 0 10px; letter-spacing: -0.01em; }
.chno { display: block; font: 600 14px/1.4 var(--font-label); letter-spacing: 0.08em; text-transform: uppercase; color: var(--accent-ink); margin-bottom: 4px; }
.chapter-head .subtitle { font: 400 italic 18px/1.5 var(--font-body); color: var(--ink-soft); margin-bottom: 10px; }
.meta-line { font: 500 13px/1.5 var(--font-label); color: var(--ink-soft); margin-bottom: 28px; }


/* The annotated figure.
   Wide screens (≥ 1024px): the screenshot stays put on the left while the notes scroll on the right; a note crossing
   the middle of the window lights its region, and so does pointing at it.
   Narrow screens: a small overview of the whole screen with numbered marks, then a strip of cards to swipe, each
   with a magnified crop of its region, because a 1440-pixel screen shrunk to a phone is unreadable. */
figure.annotated { margin: 28px 0 8px; display: grid; gap: 14px; grid-template-columns: minmax(0, 1fr); }
.stage { position: relative; border: 1px solid var(--rule); border-radius: 6px; overflow: hidden; background: var(--panel); outline: none; touch-action: manipulation; }
.stage:focus-visible { box-shadow: 0 0 0 2px var(--paper), 0 0 0 4px var(--accent); }
.zoomer { position: relative; transform-origin: 0 0; transition: transform 320ms cubic-bezier(.2,.7,.2,1); }
.zoomer img { display: block; width: 100%; height: auto; }
.zoomer svg { position: absolute; inset: 0; width: 100%; height: 100%; }
.shade { fill: var(--shade); opacity: 0; transition: opacity 180ms; pointer-events: none; }
.stage.has-active .shade { opacity: 1; }
.region rect.frame { fill: transparent; stroke: var(--accent); stroke-width: 2.5; vector-effect: non-scaling-stroke; cursor: pointer; }
.region.warning rect.frame { stroke: var(--warn); }
.region.subtle rect.frame { stroke: var(--subtle); stroke-dasharray: 6 4; }
.region.active rect.frame { stroke-width: 4; }
.region .badge circle { fill: var(--accent); stroke: var(--paper); stroke-width: 2; vector-effect: non-scaling-stroke; }
.region.warning .badge circle { fill: var(--warn); }
.region.subtle .badge circle { fill: var(--subtle); }
.region .badge text { fill: var(--badge-fg); font-family: var(--font-label); font-weight: 600; text-anchor: middle; dominant-baseline: central; pointer-events: none; }
.region .badge { cursor: pointer; }
.zoomhint { position: absolute; right: 10px; bottom: 10px; font: 500 12px/1 var(--font-label); color: var(--ink); background: var(--paper); border: 1px solid var(--rule); border-radius: 4px; padding: 6px 8px; opacity: 0.94; }
figcaption { font: 500 14px/1.45 var(--font-label); color: var(--ink-soft); }
figcaption b { color: var(--ink); font-weight: 600; }

/* Notes: one list, laid out as a column of notes (wide) or a strip of cards (narrow). */
ol.notes { list-style: none; margin: 0; padding: 0; min-width: 0; }
ol.notes li { min-width: 0; }
.card { box-sizing: border-box; display: grid; grid-template-columns: 1.8rem minmax(0, 1fr); gap: 3px 10px; width: 100%; padding: 12px 14px; border-radius: 6px; cursor: pointer; border: 1px solid transparent; border-left: 3px solid transparent; background: transparent; color: inherit; text-align: left; font: inherit; }
.card:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.card .num { grid-row: span 2; align-self: start; margin-top: 2px; width: 1.6rem; height: 1.6rem; border-radius: 999px; display: grid; place-items: center; background: var(--accent); color: var(--badge-fg); font: 600 13px/1 var(--font-label); }
li.warning .card .num { background: var(--warn); }
li.subtle .card .num { background: var(--subtle); }
.card .label { font: 600 15px/1.3 var(--font-label); align-self: center; }
.card .note { font: 400 15px/1.55 var(--font-body); color: var(--ink-soft); }
.card.active .note { color: var(--ink); }
.card .crop { display: none; }
.strip-nav { display: none; }

@media (min-width: 1024px) {
  figure.annotated { grid-template-columns: minmax(0, 1fr) minmax(19rem, 24rem); column-gap: 28px; align-items: start; }
  figure.annotated .stage-wrap { position: sticky; top: calc(env(safe-area-inset-top, 0px) + 16px); display: grid; gap: 10px; }
  ol.notes { display: grid; gap: 6px; padding-block: 2px 30vh; }
  .card:hover, .card.active { background: var(--panel); border-left-color: var(--accent); }
  li.warning .card:hover, li.warning .card.active { border-left-color: var(--warn); }
  li.subtle .card:hover, li.subtle .card.active { border-left-color: var(--subtle); }
}

@media (max-width: 1023.98px) {
  .stage-wrap { display: grid; gap: 8px; }
  .legend-keys .wide-only { display: none; }
  ol.notes { display: grid; grid-auto-flow: column; grid-auto-columns: min(86%, 26rem); gap: 12px; overflow-x: auto; scroll-snap-type: x mandatory; overscroll-behavior-x: contain; padding: 2px 2px 10px; scrollbar-width: thin; }
  ol.notes li { scroll-snap-align: center; display: grid; }
  .card { grid-template-columns: 1.8rem minmax(0, 1fr); align-content: start; border: 1px solid var(--rule); background: var(--paper); }
  .card.active { border-color: var(--accent); }
  li.warning .card.active { border-color: var(--warn); }
  li.subtle .card.active { border-color: var(--subtle); }
  .card .crop { display: block; grid-column: 1 / -1; order: -1; margin: -4px -6px 8px; border-radius: 4px; overflow: hidden; background: var(--panel); border: 1px solid var(--rule); }
  .card .crop svg { display: block; width: 100%; height: auto; }
  .strip-nav { display: flex; align-items: center; justify-content: space-between; gap: 10px; font: 500 13px/1 var(--font-label); color: var(--ink-soft); }
  .strip-nav button { all: unset; box-sizing: border-box; display: grid; place-items: center; width: 40px; height: 36px; border: 1px solid var(--rule); border-radius: 6px; color: var(--ink); cursor: pointer; font: 600 16px/1 var(--font-label); }
  .strip-nav button:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
  .zoomhint { display: none; }
}
@media (max-width: 640px) {
  body { font-size: 16px; }
  .card .note { font-size: 14.5px; }
  th, td { padding: 7px 9px; }
}
.crop .cf { fill: none; stroke: var(--accent); stroke-width: 3; vector-effect: non-scaling-stroke; }
li.warning .crop .cf { stroke: var(--warn); }
li.subtle .crop .cf { stroke: var(--subtle); stroke-dasharray: 6 4; }

@media (min-width: 1024px) { .narrow-only { display: none; } }
.legend-keys { display: block; font: 500 12.5px/1.4 var(--font-label); color: var(--ink-soft); margin: 6px 0 0; }
kbd { font: 500 11.5px var(--font-mono); border: 1px solid var(--rule); border-bottom-width: 2px; border-radius: 3px; padding: 0 4px; background: var(--paper); }

/* A figure whose screenshot is not captured yet. */
figure.pending { margin: 28px auto 8px; max-width: 40rem; display: grid; gap: 10px; }
.pending-stage { aspect-ratio: 16 / 10; border: 2px dashed var(--tbd-rule); border-radius: 6px; background: repeating-linear-gradient(135deg, var(--panel) 0 14px, var(--paper) 14px 28px); display: grid; place-content: center; justify-items: center; gap: 8px; padding: 20px; text-align: center; }
.pending-tag { font: 600 13px/1 var(--font-label); letter-spacing: 0.08em; text-transform: uppercase; color: var(--tbd-ink); background: var(--tbd-bg); border: 1px solid var(--tbd-rule); border-radius: 999px; padding: 6px 10px 5px; }
.pending-what { font: 500 14px/1.45 var(--font-label); color: var(--ink-soft); max-width: 30rem; }
ol.pending-notes { list-style: none; margin: 0; padding: 0; display: grid; gap: 8px; font: 400 15px/1.55 var(--font-body); color: var(--ink-soft); }
ol.pending-notes li { display: grid; grid-template-columns: 1.8rem minmax(0, 1fr); gap: 10px; }
ol.pending-notes b { font: 600 15px/1.3 var(--font-label); color: var(--ink); }
ol.pending-notes .num { width: 1.6rem; height: 1.6rem; border-radius: 999px; display: grid; place-items: center; background: var(--subtle); color: var(--badge-fg); font: 600 13px/1 var(--font-label); margin-top: 2px; }

aside.box { border-left: 3px solid var(--accent); background: var(--panel); padding: 12px 16px; margin: 22px 0; border-radius: 0 5px 5px 0; }
aside.box .eyebrow { margin-bottom: 4px; }
aside.box.field, aside.box.warning { border-left-color: var(--warn); }
aside.box.hood, aside.box.note { border-left-color: var(--subtle); }
aside.box p:last-child { margin-bottom: 0; }
pre { font: 400 13px/1.55 var(--font-mono); background: var(--code-bg); border: 1px solid var(--rule); border-radius: 5px; padding: 14px 16px; overflow-x: auto; margin: 14px 0 20px; }
pre code { font: inherit; background: none; padding: 0; border-radius: 0; overflow-wrap: normal; }
.meta { font: 500 13px/1.5 var(--font-label); color: var(--ink-soft); border-top: 1px solid var(--rule); margin-top: 44px; padding-top: 14px; }
@media (prefers-reduced-motion: reduce) { .zoomer, .shade, rect.frame { transition: none; } }
"""

JS = r"""(() => {
  const NS = "http://www.w3.org/2000/svg";
  const wide = matchMedia("(min-width: 1024px)");
  const reduced = matchMedia("(prefers-reduced-motion: reduce)");
  const el = (tag, attrs, parent) => { const e = document.createElementNS(NS, tag); for (const k in attrs) e.setAttribute(k, attrs[k]); if (parent) parent.appendChild(e); return e; };
  const zoomedFigures = new Set();
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") for (const f of [...zoomedFigures]) f(false); });

  function initFigure(fig) {
    const { W, H, regions: data } = JSON.parse(fig.querySelector("script.fig-data").textContent);
    if (!data.length) return;
    const stage = fig.querySelector(".stage");
    const zoomer = fig.querySelector(".zoomer");
    const img = zoomer.querySelector("img");
    const svg = zoomer.querySelector("svg");
    const list = fig.querySelector("ol.notes");
    const hint = fig.querySelector(".zoomhint");
    const pos = fig.querySelector(".strip-nav .pos");
    const name = (fig.querySelector("figcaption b") || {}).textContent || "the figure";
    list.setAttribute("aria-label", `Annotations of ${name.replace(/\.$/, "")}`);
    let active = -1, zoomed = false, pinned = false;

    // The shade darkens everything but the active region (an even-odd path with a hole).
    const shade = el("path", { class: "shade", "fill-rule": "evenodd", d: "" }, svg);
    const badges = [];
    const groups = data.map((r, i) => {
      const [x, y, w, h] = r.box;
      const g = el("g", { class: `region ${r.style || "focus"}` }, svg);
      el("rect", { class: "frame", x, y, width: w, height: h, rx: 4 }, g);
      const badge = el("g", { class: "badge" }, g);
      const c = el("circle", {}, badge);
      const t = el("text", {}, badge);
      t.textContent = String(i + 1);
      badges.push({ badge, c, t, x, y });
      g.addEventListener("mouseenter", () => { if (wide.matches) setActive(i); });
      g.addEventListener("click", (e) => { e.stopPropagation(); setActive(i, { reveal: true }); if (wide.matches) toggleZoom(true); });
      return g;
    });

    // Badges keep a readable size whatever the screenshot is scaled to: about 24 CSS pixels across.
    function sizeBadges() {
      const k = W / Math.max(1, stage.clientWidth);
      const r = Math.max(11, 12 * k), fs = Math.max(13, 13 * k);
      const placed = [];
      for (const b of badges) {
        let bx = Math.min(Math.max(b.x, r + 2), W - r - 2);
        const by = Math.min(Math.max(b.y, r + 2), H - r - 2);
        // Badges of regions that start close together slide right along the top edge instead of overlapping.
        while (placed.some(([px, py]) => Math.hypot(px - bx, py - by) < 2 * r + 3) && bx < W - 3 * r) bx += 2 * r + 4;
        placed.push([bx, by]);
        b.badge.setAttribute("transform", `translate(${bx} ${by})`);
        b.c.setAttribute("r", r);
        b.t.setAttribute("font-size", fs);
      }
    }

    const cards = data.map((r, i) => {
      const li = document.createElement("li");
      li.className = r.style || "focus";
      const b = document.createElement("button");
      b.type = "button";
      b.className = "card";
      b.innerHTML = `<span class="crop" aria-hidden="true"></span><span class="num" aria-hidden="true">${i + 1}</span><span class="label"></span><span class="note"></span>`;
      b.querySelector(".label").textContent = r.label;
      b.querySelector(".note").innerHTML = r.noteHtml;
      b.setAttribute("aria-label", `${i + 1}. ${r.label}. ${r.noteText}`);
      // The magnified crop for narrow screens: the region with some context, from the same embedded image.
      // Wide regions (a full-width row) are cropped from their left edge, where a row's content starts, so the crop
      // stays magnified; narrower ones are centred.
      const [x, y, w, h] = r.box;
      const cw = Math.min(Math.max(w + 48, 420), 720), ch = Math.max(h + 48, cw * 0.42);
      const left = w + 48 > cw ? x - 24 : x + w / 2 - cw / 2;
      const cx = Math.min(Math.max(left, 0), Math.max(0, W - cw));
      const cy = Math.min(Math.max(y + h / 2 - ch / 2, 0), Math.max(0, H - ch));
      const crop = el("svg", { viewBox: `${cx} ${cy} ${Math.min(cw, W)} ${Math.min(ch, H)}`, preserveAspectRatio: "xMidYMid meet" }, b.querySelector(".crop"));
      el("image", { href: img.getAttribute("src"), x: 0, y: 0, width: W, height: H }, crop);
      el("rect", { class: "cf", x, y, width: w, height: h, rx: 4 }, crop);
      b.addEventListener("mouseenter", () => { if (wide.matches) { pinned = true; setActive(i); } });
      b.addEventListener("focus", () => setActive(i));
      b.addEventListener("click", () => { setActive(i); if (wide.matches) toggleZoom(!zoomed); });
      li.appendChild(b);
      list.appendChild(li);
      return b;
    });

    function setActive(i, opts = {}) {
      active = i;
      groups.forEach((g, j) => g.classList.toggle("active", j === i));
      cards.forEach((b, j) => b.classList.toggle("active", j === i));
      stage.classList.toggle("has-active", i >= 0);
      if (i >= 0) {
        const [x, y, w, h] = data[i].box;
        shade.setAttribute("d", `M0 0H${W}V${H}H0Z M${x} ${y}h${w}v${h}h${-w}Z`);
        if (pos) pos.textContent = `${i + 1} of ${data.length}`;
        if (opts.reveal && !wide.matches) {
          const li = cards[i].parentElement;
          list.scrollTo({ left: li.offsetLeft - (list.clientWidth - li.clientWidth) / 2, behavior: reduced.matches ? "auto" : "smooth" });
        }
      }
      if (zoomed) applyZoom();
    }

    function applyZoom() {
      if (!zoomed || active < 0 || !wide.matches) { zoomer.style.transform = ""; hint.hidden = true; return; }
      const [x, y, w, h] = data[active].box;
      const s = Math.max(1, Math.min(2.6, Math.min((W * 0.86) / w, (H * 0.6) / h)));
      const tx = Math.min(0, Math.max(W - W * s, W / 2 - (x + w / 2) * s));
      const ty = Math.min(0, Math.max(H - H * s, H / 2 - (y + h / 2) * s));
      zoomer.style.transform = `translate(${(tx / W) * 100}%, ${(ty / H) * 100}%) scale(${s})`;
      hint.hidden = false;
    }
    function toggleZoom(on) { zoomed = on; if (on) zoomedFigures.add(toggleZoom); else zoomedFigures.delete(toggleZoom); applyZoom(); }

    // Wide: a note crossing the middle of the window lights its region (unless the pointer is on a note).
    const middle = new IntersectionObserver((entries) => {
      if (!wide.matches || pinned || zoomed) return;
      for (const e of entries) if (e.isIntersecting) setActive(cards.indexOf(e.target));
    }, { rootMargin: "-45% 0px -45% 0px" });
    // Narrow: the card in view lights its region on the overview.
    const strip = new IntersectionObserver((entries) => {
      if (wide.matches) return;
      for (const e of entries) if (e.isIntersecting) setActive(cards.indexOf(e.target));
    }, { root: list, threshold: 0.6 });
    cards.forEach((c) => { middle.observe(c); strip.observe(c); });

    list.addEventListener("mouseleave", () => { pinned = false; });
    stage.addEventListener("click", () => { if (zoomed) toggleZoom(false); });
    stage.addEventListener("keydown", (e) => {
      if (e.key === "ArrowRight" || e.key === "ArrowDown") { setActive((active + 1) % data.length, { reveal: true }); e.preventDefault(); }
      else if (e.key === "ArrowLeft" || e.key === "ArrowUp") { setActive((active - 1 + data.length) % data.length, { reveal: true }); e.preventDefault(); }
      else if (e.key === "Enter" || e.key === " ") { if (active < 0) setActive(0); toggleZoom(!zoomed); e.preventDefault(); }
      else if (e.key === "Escape") toggleZoom(false);
    });
    fig.querySelectorAll(".strip-nav button").forEach((b) => b.addEventListener("click", () => {
      const n = (Math.max(active, 0) + Number(b.dataset.step) + data.length) % data.length;
      setActive(n, { reveal: true });
    }));

    new ResizeObserver(() => { sizeBadges(); applyZoom(); }).observe(stage);
    wide.addEventListener("change", () => { toggleZoom(false); setActive(active); });
    sizeBadges();
    setActive(0);
  }

  document.querySelectorAll("figure.annotated").forEach(initFigure);
})();
"""


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--out", type=Path, default=HERE / "out" / "book.html")
    args = ap.parse_args()
    page = build()
    size = len(page.encode("utf-8"))
    if size > SIZE_LIMIT:
        raise SystemExit(f"the page is {size / 1e6:.1f} MB; an Artifact holds 16 MB at most")
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(page, encoding="utf-8")
    print(f"wrote {args.out} ({size / 1e6:.2f} MB)")


if __name__ == "__main__":
    main()
