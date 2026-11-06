"""Release assembly checks using explicitly non-executable package fixtures."""
import hashlib
import importlib.util
import json
import re
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

OPENWRT = Path(__file__).resolve().parents[2] / "openwrt"
BUILD_SPEC = importlib.util.spec_from_file_location("daed_package_build", OPENWRT / "build.py")
BUILD = importlib.util.module_from_spec(BUILD_SPEC)
BUILD_SPEC.loader.exec_module(BUILD)


class ManifestTests(unittest.TestCase):
    def test_source_installer_default_matches_build_and_workflow(self):
        version = re.search(r"^release=openwrt-v(\d+\.\d+\.\d+)$", (OPENWRT / "install.sh").read_text(), re.MULTILINE).group(1)
        self.assertIn("," + version + ")", (OPENWRT / "Makefile").read_text())
        self.assertIn("default: '" + version + "'", (OPENWRT.parent / ".github/workflows/openwrt.yml").read_text())
        readme = (OPENWRT.parent / "README.md").read_text(encoding="utf-8")
        self.assertIn("/releases/tag/openwrt-v" + version, readme)
        self.assertIn('nl=$(uclient-fetch -O- https://slobys.github.io/NaiLink/install.sh) && sh -c "${nl:?下载为空}" -- --menu', readme)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.directory = Path(self.temp.name)
        self.rows = []
        for cpu in {target["cpu"] for target in json.loads((OPENWRT / "sdk-matrix.json").read_text())}:
            (self.directory / ("btf-probe-" + cpu)).write_bytes(b"non-executable parser fixture")
        for target in json.loads((OPENWRT / "sdk-matrix.json").read_text()):
            packages = [(arch, "daed-router") for arch in target["architectures"]]
            if target["cpu"] == "x86_64":
                packages.append(("all", "luci-app-daed-router"))
            for arch, package in packages:
                name = f"{package}-{arch}.{target['format']}"
                data = b"manifest test fixture; not an installable package"
                (self.directory / name).write_bytes(data)
                self.rows.append([target["format"], arch, package, name, hashlib.sha256(data).hexdigest(), "0.1.0"])
        self.save()

    def tearDown(self):
        self.temp.cleanup()

    def save(self):
        (self.directory / "fixture.tsv").write_text("".join("\t".join(row) + "\n" for row in self.rows))

    def assemble(self, version="0.1.0"):
        return subprocess.run([sys.executable, str(OPENWRT / "manifest.py"), str(self.directory), "--version", version], capture_output=True, text=True)

    def test_complete_matrix_and_checker_checksum(self):
        result = self.assemble()
        self.assertEqual(result.returncode, 0, result.stderr)
        manifest = (self.directory / "manifest.tsv").read_text()
        self.assertIn("support\tall\tcheck\tdaed-check", manifest)
        self.assertIn(hashlib.sha256((self.directory / "daed-check").read_bytes()).hexdigest(), manifest)

    def test_missing_architecture_is_not_silently_published(self):
        self.rows.pop()
        self.save()
        self.assertNotEqual(self.assemble().returncode, 0)

    def test_future_release_installer_fetches_its_own_version(self):
        for row in self.rows:
            row[5] = "0.2.0"
        self.save()
        result = self.assemble("0.2.0")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("release=openwrt-v0.2.0\n", (self.directory / "install.sh").read_text())

    def test_checksum_duplicate_and_mismatched_version_are_rejected(self):
        original = [row[:] for row in self.rows]
        for mutate in (
            lambda: self.rows[0].__setitem__(4, "0" * 64),
            lambda: self.rows.append(self.rows[0][:]),
            lambda: self.rows[0].__setitem__(5, "0.2.0"),
        ):
            self.rows = [row[:] for row in original]
            mutate()
            self.save()
            self.assertNotEqual(self.assemble().returncode, 0)

    def test_non_elf_runtime_is_rejected_before_sdk_download(self):
        binary = self.directory / "fake-daed"
        binary.write_bytes(b"this is not an executable")
        with self.assertRaisesRegex(ValueError, "ELF"):
            BUILD.verify_elf(binary, "x86_64")

    def test_sdk_base_feed_pin_preserves_root_options_and_other_feeds(self):
        for kind, options in (("src-git-full", ""), ("src-git", " --root=package")):
            other = "src-git packages https://example.test/packages.git^original\n"
            feeds = kind + options + " base https://example.test/base.git;branch\n" + other
            result = BUILD.pin_base_feed(feeds, "a" * 40)
            self.assertEqual(result, "src-git" + options + " base https://github.com/openwrt/openwrt.git^" + "a" * 40 + "\n" + other)

    def test_sdk_base_feed_pin_rejects_missing_and_duplicate_entries(self):
        entry = "src-git --root=package base https://example.test/base.git\n"
        for feeds in ("src-git packages https://example.test/packages.git\n", entry + entry):
            with self.assertRaisesRegex(ValueError, "unambiguously"):
                BUILD.pin_base_feed(feeds, "a" * 40)


if __name__ == "__main__":
    unittest.main()
