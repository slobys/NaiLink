import contextlib
import importlib.util
import io
import json
import re
import tempfile
import unittest
from pathlib import Path

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

    def test_version_and_install_entry_match_runtime(self):
        config = json.loads((ROOT / "docs/site/site.json").read_text(encoding="utf-8"))
        makefile = (ROOT / "openwrt/Makefile").read_text(encoding="utf-8")
        runtime_version = re.search(r"PKG_VERSION:=.*,(\d+\.\d+\.\d+)\)", makefile).group(1)
        self.assertEqual(config["version"], runtime_version)
        self.assertEqual(config["release"], "openwrt-v" + runtime_version)
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        installation = (self.output / "installation.html").read_text(encoding="utf-8")
        for text in (readme, installation):
            self.assertIn(config["release"] + "/install.sh", text)
            self.assertIn("ARMv7", text)
            self.assertIn("暂不推荐", text)
        self.assertNotIn("$release", installation)
        self.assertNotIn("$version", installation)


if __name__ == "__main__":
    unittest.main()
