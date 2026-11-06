import contextlib
import html
import importlib.util
import io
import json
import re
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.error import HTTPError

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("docs_builder", ROOT / "docs/site/build.py")
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class HandbookTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.output = Path(self.directory.name)
        with contextlib.redirect_stdout(io.StringIO()):
            builder.build(self.output)

    def test_public_output_and_cross_page_anchors(self):
        builder.validate(self.output)
        pages = list(self.output.glob("*.html"))
        config = json.loads((ROOT / "docs/site/site.json").read_text(encoding="utf-8"))
        expected = {page["id"] + ".html" for group in config["groups"] for page in group["pages"]} | {"404.html"}
        self.assertEqual({page.name for page in pages}, expected)
        self.assertTrue((self.output / "assets/nailink-logo.png").is_file())
        self.assertFalse((self.output / "build.py").exists())
        self.assertFalse((self.output / "BRAND.md").exists())
        index = json.loads((self.output / "search-index.json").read_text(encoding="utf-8"))
        self.assertTrue(any(i["url"] == "troubleshooting.html#test-timeout" for i in index))
        self.assertTrue(any(i["url"] == "groups.html#add" for i in index))
        self.assertTrue(any(i["url"] == "cores.html#switch" for i in index))
        self.assertGreater(len(index), 40)

    def test_broken_anchor_is_rejected(self):
        with (self.output / "index.html").open("a", encoding="utf-8") as stream:
            stream.write('<a href="groups.html#nonexistent">Broken</a>')
        with self.assertRaisesRegex(ValueError, "Missing anchor"):
            builder.validate(self.output)

    def test_unknown_output_is_not_published(self):
        (self.output / "private-config.db").write_text("not-public", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "Unexpected or missing published files"):
            with contextlib.redirect_stdout(io.StringIO()):
                builder.build(self.output)

    def test_public_version_and_latest_install_entry(self):
        config = json.loads((ROOT / "docs/site/site.json").read_text(encoding="utf-8"))
        self.assertEqual(config["release"], "openwrt-v" + config["version"])
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        self.assertIn("/releases/tag/" + config["release"], readme)
        openwrt_readme = (ROOT / "openwrt/README.md").read_text(encoding="utf-8")
        installation = (self.output / "installation.html").read_text(encoding="utf-8")
        for text in (readme, installation):
            self.assertNotIn("--release " + config["release"], text)
            self.assertIn("ARMv7", text)
            self.assertIn("暂不推荐", text)
        command = html.unescape(re.search(r'<code class="language-sh">(.*?)</code>', installation, re.S).group(1))
        self.assertEqual(command, f'nl=$(uclient-fetch -O- {config["url"]}install.sh) && sh -c "${{nl:?下载为空}}" -- --menu')
        for text in (readme, openwrt_readme):
            self.assertIn(f"```sh\n{command}\n```", text)
        header, body = (ROOT / "openwrt/install.sh").read_bytes().split(b"\n", 1)
        entry = (self.output / "install.sh").read_bytes()
        self.assertTrue(entry.startswith(header + b'\n[ "$#" -gt 0 ] || set -- --menu\nset -- --latest "$@"\n'))
        self.assertTrue(entry.endswith(body))
        self.assertNotIn("$release", installation)
        self.assertNotIn("$version", installation)

    def test_pinned_release_links_and_recovery_command_match(self):
        config = json.loads((ROOT / "docs/site/site.json").read_text(encoding="utf-8"))
        documents = [ROOT / "README.md", ROOT / "openwrt/README.md", *self.output.glob("*.html")]
        for path in documents:
            text = html.unescape(path.read_text(encoding="utf-8"))
            with self.subTest(document=path.name):
                tags = re.findall(r"https://github.com/slobys/NaiLink/releases/(?:download|tag)/([^/\s\"<)]+)", text)
                if tags:
                    self.assertEqual(set(tags), {config["release"]})
                arguments = re.findall(r"--release\s+([^\s`<]+)", text)
                if arguments:
                    self.assertEqual(set(arguments), {config["release"]})
        troubleshooting = (self.output / "troubleshooting.html").read_text(encoding="utf-8")
        self.assertIn("contents/openwrt/install.sh?ref=" + config["release"], troubleshooting)
        self.assertIn("--install --update-only --release " + config["release"], troubleshooting)
        self.assertNotIn("install.sh?ref=main", troubleshooting)

    def test_mismatched_or_invalid_public_version_is_rejected(self):
        for version, tag in (("1.0.0", "openwrt-v1.0.1"), ("invalid", "openwrt-vinvalid")):
            with self.subTest(version=version, tag=tag), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                (root / "site.json").write_text(json.dumps({"version": version, "release": tag}))
                with patch.object(builder, "ROOT", root):
                    with self.assertRaisesRegex(ValueError, "version and release tag must match"):
                        builder.build(root / "output")
                self.assertFalse((root / "output").exists())

    def test_public_installer_download_is_anonymous_and_version_pinned(self):
        response = io.BytesIO(b"#!/bin/sh\nrelease=openwrt-v1.2.3\n")
        response.status = 200
        with patch.object(builder, "urlopen", return_value=response) as fetch, contextlib.redirect_stdout(io.StringIO()):
            builder.check_public_installer("openwrt-v1.2.3")
        request = fetch.call_args.args[0]
        self.assertEqual(request.full_url, "https://github.com/slobys/NaiLink/releases/download/openwrt-v1.2.3/install.sh")
        self.assertIsNone(request.get_header("Authorization"))
        self.assertIsNone(request.get_header("Cookie"))
        self.assertEqual(fetch.call_args.kwargs["timeout"], 30)

    def test_unavailable_public_installer_stops_build_before_writing(self):
        output = self.output / "not-built"
        unavailable = HTTPError("https://github.com/slobys/NaiLink/releases/download/draft/install.sh", 404, "Not Found", {}, None)
        with patch.object(builder, "urlopen", side_effect=unavailable):
            with self.assertRaises(HTTPError):
                builder.build(output, check_release=True)
        self.assertFalse(output.exists())

    def test_invalid_public_installer_is_rejected(self):
        valid = b"#!/bin/sh\nrelease=openwrt-v1.2.3\n"
        for status, body in (
            (206, valid),
            (200, b"<html>Not Found</html>"),
            (200, b"#!/bin/sh\nrelease=openwrt-v1.2.4\n"),
            (200, valid + b"release=openwrt-v1.2.3\n"),
            (200, valid + b"x" * (1024 * 1024)),
        ):
            with self.subTest(status=status, prefix=body[:64]):
                response = io.BytesIO(body)
                response.status = status
                with patch.object(builder, "urlopen", return_value=response):
                    with self.assertRaises(ValueError):
                        builder.check_public_installer("openwrt-v1.2.3")


if __name__ == "__main__":
    unittest.main()
