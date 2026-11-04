"""Build the NaiLink handbook using only the Python standard library."""

import argparse
import html
import json
import shutil
from html.parser import HTMLParser
from pathlib import Path
from string import Template
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parent


def build_installer(output):
    source = ROOT.parents[1] / "openwrt/install.sh"
    header, body = source.read_bytes().split(b"\n", 1)
    entry = header + b'\n[ "$#" -gt 0 ] || set -- --install\nset -- --latest "$@"\n' + body
    # The one-line command passes the downloaded script as one argument to ash.
    # Linux limits each argument to 32 pages, or 128 KiB with 4 KiB pages.
    if len(entry) >= 128 * 1024:
        raise ValueError("Installer exceeds the one-line command argument limit")
    (output / "install.sh").write_bytes(entry)


class Document(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.ids = set()
        self.links = []
        self.headings = []
        self.text = []
        self.heading = None

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if attrs.get("id"):
            if attrs["id"] in self.ids:
                raise ValueError(f"Duplicate id: {attrs['id']}")
            self.ids.add(attrs["id"])
        for key in ("href", "src"):
            if attrs.get(key):
                self.links.append(attrs[key])
        if tag in ("h2", "h3"):
            if not attrs.get("id"):
                raise ValueError("Every section heading needs a stable id")
            self.heading = [tag, attrs["id"], ""]

    def handle_data(self, data):
        self.text.append(data)
        if self.heading is not None:
            self.heading[2] += data

    def handle_endtag(self, tag):
        if self.heading and tag == self.heading[0]:
            self.headings.append(self.heading)
            self.heading = None


def validate(output):
    documents = {}
    for path in output.glob("*.html"):
        doc = Document()
        doc.feed(path.read_text(encoding="utf-8"))
        documents[path.name] = doc
    for name, doc in documents.items():
        for link in doc.links:
            url = urlsplit(link)
            if url.scheme or url.netloc:
                continue
            target = unquote(url.path) or name
            if target.startswith("/") or ".." in Path(target).parts:
                raise ValueError(f"Use portable site-relative links: {name}: {link}")
            if not (output / target).is_file():
                raise ValueError(f"Missing target: {name}: {link}")
            if url.fragment and target in documents:
                if unquote(url.fragment) not in documents[target].ids:
                    raise ValueError(f"Missing anchor: {name}: {link}")
    index = json.loads((output / "search-index.json").read_text(encoding="utf-8"))
    for item in index:
        target, _, anchor = item["url"].partition("#")
        if target not in documents or (anchor and anchor not in documents[target].ids):
            raise ValueError(f"Invalid search result: {item['url']}")


def build(output):
    config = json.loads((ROOT / "site.json").read_text(encoding="utf-8"))
    pages = [page for group in config["groups"] for page in group["pages"]]
    template = Template((ROOT / "template.html").read_text(encoding="utf-8"))
    output.mkdir(parents=True, exist_ok=True)
    shutil.copytree(ROOT / "assets", output / "assets", dirs_exist_ok=True)
    for name in ("site.css", "site.js"):
        shutil.copyfile(ROOT / name, output / name)
    build_installer(output)
    search = []
    for number, page in enumerate(pages):
        source = ROOT / "content" / f"{page['id']}.html"
        content = Template(source.read_text(encoding="utf-8")).substitute(config)
        doc = Document()
        doc.feed(content)
        navigation = []
        for group in config["groups"]:
            navigation.append(f'<p class="nav-label">{html.escape(group["title"])}</p>')
            for entry in group["pages"]:
                current = ' aria-current="page"' if entry["id"] == page["id"] else ""
                navigation.append(f'<a href="{entry["id"]}.html"{current}>{html.escape(entry["title"])}</a>')
        toc = "".join(f'<a class="toc-{tag}" href="#{anchor}">{html.escape(title)}</a>' for tag, anchor, title in doc.headings)
        pager = []
        for offset, label in ((-1, "上一篇"), (1, "下一篇")):
            position = number + offset
            if 0 <= position < len(pages):
                entry = pages[position]
                pager.append(f'<a href="{entry["id"]}.html"><small>{label}</small>{html.escape(entry["title"])}</a>')
            else:
                pager.append("<span></span>")
        title = "NaiLink 使用文档" if page["id"] == "index" else f'{page["title"]} · NaiLink'
        result = template.substitute(
            title=html.escape(title), description=html.escape(page["description"], quote=True),
            canonical=config["url"] + ("" if page["id"] == "index" else page["id"] + ".html"),
            navigation="".join(navigation), toc=toc, content=content,
            page_title=html.escape(page["title"]), page_description=html.escape(page["description"]),
            page_id=page["id"], version=config["version"], pager="".join(pager),
        )
        (output / f'{page["id"]}.html').write_text(result, encoding="utf-8")
        # Index each section separately, making searches land at the relevant step.
        sections = content.split("<h2 ")
        for section_number, section in enumerate(sections):
            parsed = Document()
            parsed.feed(section if section_number == 0 else "<h2 " + section)
            if not "".join(parsed.text).strip():
                continue
            heading = parsed.headings[0] if parsed.headings else None
            search.append({
                "title": page["title"], "section": heading[2] if heading else page["description"],
                "text": " ".join(" ".join(parsed.text).split()),
                "url": page["id"] + ".html" + ("#" + heading[1] if heading else ""),
            })
    (output / "search-index.json").write_text(json.dumps(search, ensure_ascii=False), encoding="utf-8")
    not_found = (output / "index.html").read_text(encoding="utf-8")
    # An absolute base is needed for assets/navigation when a missing nested URL is visited.
    not_found = not_found.replace('<head>', f'<head><base href="{config["url"]}">')
    start = not_found.index('<article class="article">')
    end = not_found.index('</article>', start) + len('</article>')
    not_found = not_found[:start] + '<article class="article"><p class="eyebrow">404</p><h1>页面未找到</h1><p>请使用文档目录或搜索，找到你需要的内容。</p><a class="button primary" href="index.html">返回首页</a></article>' + not_found[end:]
    # The 404 page has its own content, so omit the home page table of contents.
    toc_start = not_found.index('<aside class="toc"')
    toc_end = not_found.index('</aside>', toc_start) + len('</aside>')
    not_found = not_found[:toc_start] + not_found[toc_end:]
    not_found = not_found.replace('<title>NaiLink 使用文档</title>', '<title>页面未找到 · NaiLink</title>')
    (output / "404.html").write_text(not_found, encoding="utf-8")
    (output / ".nojekyll").touch()
    (output / "robots.txt").write_text(f'User-agent: *\nAllow: /\nSitemap: {config["url"]}sitemap.xml\n', encoding="utf-8")
    urls = "".join(f'<url><loc>{config["url"]}{p["id"]}.html</loc></url>' for p in pages)
    (output / "sitemap.xml").write_text(f'<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">{urls}</urlset>', encoding="utf-8")
    expected = {f'{p["id"]}.html' for p in pages} | {
        "404.html", "site.css", "site.js", "install.sh", "search-index.json", ".nojekyll", "robots.txt", "sitemap.xml"
    }
    expected |= {"assets/" + str(p.relative_to(ROOT / "assets")).replace("\\", "/") for p in (ROOT / "assets").rglob("*") if p.is_file()}
    actual = {str(p.relative_to(output)).replace("\\", "/") for p in output.rglob("*") if p.is_file()}
    if actual != expected:
        raise ValueError(f"Unexpected or missing published files: {actual ^ expected}. Use a clean build directory.")
    validate(output)
    print(f"Built and checked {len(pages)} pages, {len(search)} search sections: {output}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=ROOT.parents[1] / ".build" / "docs-site")
    args = parser.parse_args()
    build(args.output.resolve())
