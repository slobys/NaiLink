"""Run the documented local-install commands with inert, temporary assets."""
import hashlib
import html
import importlib.util
import io
import contextlib
import json
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("local_install_docs_builder", ROOT / "docs/site/build.py")
builder = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(builder)


@unittest.skipUnless(all(shutil.which(tool) for tool in ("sh", "awk", "sha256sum")),
                     "Documented router commands require sh, awk and sha256sum")
class LocalInstallationCommandsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.site = tempfile.TemporaryDirectory(prefix="nailink-docs-site-")
        cls.addClassCleanup(cls.site.cleanup)
        output = Path(cls.site.name)
        with contextlib.redirect_stdout(io.StringIO()):
            builder.build(output)
        cls.page = (output / "local-installation.html").read_text(encoding="utf-8")
        cls.config = json.loads((ROOT / "docs/site/site.json").read_text(encoding="utf-8"))

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="nailink docs assets ")
        self.addCleanup(temporary.cleanup)
        self.assets = Path(temporary.name) / ("nailink-" + self.config["version"])
        self.make_assets("ipk")

    def make_assets(self, fmt):
        if self.assets.exists():
            shutil.rmtree(self.assets)
        self.assets.mkdir()
        version = self.config["version"]
        self.names = [
            "install.sh", "manifest.tsv", "daed-check", "btf-probe-x86_64",
            f"daed-router-{version}-x86_64.{fmt}", f"luci-app-daed-router-{version}.{fmt}",
        ]
        for name in self.names:
            (self.assets / name).write_text("Inert documentation fixture: " + name + "\n")
        # This stand-in only records arguments. No real installer or package is run.
        (self.assets / "install.sh").write_text('#!/bin/sh\nprintf "%s\\n" "$@" > invocation.txt\n')
        sums = "".join(
            hashlib.sha256((self.assets / name).read_bytes()).hexdigest() + "  " + name + "\n"
            for name in self.names
        )
        (self.assets / "SHA256SUMS").write_text(sums)
        self.log = self.assets / "invocation.txt"

    def command(self, command_id):
        match = re.search(r'<code id="' + re.escape(command_id) + r'" class="language-sh">(.*?)</code>',
                          self.page, re.S)
        self.assertIsNotNone(match, command_id)
        command = html.unescape(match.group(1))
        return command.replace("/tmp/nailink-" + self.config["version"], shlex.quote(str(self.assets)))

    def run_command(self, command_id="local-verify"):
        return subprocess.run(["sh", "-c", self.command(command_id)], cwd=self.assets.parent,
                              text=True, capture_output=True, timeout=10)

    def test_verified_ipk_and_apk_assets_reach_dry_run(self):
        for fmt in ("ipk", "apk"):
            with self.subTest(format=fmt):
                self.make_assets(fmt)
                result = self.run_command()
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.log.read_text().splitlines(), [
                    "--install", "--release", self.config["release"],
                    "--asset-dir", str(self.assets), "--dry-run",
                ])
                self.assertEqual(result.stdout.count(": OK"), len(self.names))

    def test_missing_asset_or_checksum_list_never_runs_installer(self):
        for name in [*self.names, "SHA256SUMS"]:
            with self.subTest(missing=name):
                self.make_assets("ipk")
                (self.assets / name).unlink()
                result = self.run_command()
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.log.exists())

    def test_tampered_script_manifest_probe_or_packages_never_run(self):
        for name in self.names:
            with self.subTest(tampered=name):
                self.make_assets("ipk")
                with (self.assets / name).open("a") as stream:
                    stream.write("# changed after download\n")
                result = self.run_command()
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.log.exists())

    def test_missing_or_duplicate_checksum_record_never_runs(self):
        for duplicate in (False, True):
            with self.subTest(duplicate=duplicate):
                self.make_assets("ipk")
                sums = self.assets / "SHA256SUMS"
                lines = sums.read_text().splitlines(keepends=True)
                sums.write_text("".join(lines + [lines[0]] if duplicate else lines[1:]))
                result = self.run_command()
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.log.exists())

    def test_install_and_strict_update_pass_explicit_local_arguments(self):
        for command_id, action in (
            ("local-install", ["--install"]),
            ("local-update-command", ["--install", "--update-only"]),
        ):
            with self.subTest(command=command_id):
                result = self.run_command(command_id)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.log.read_text().splitlines(), [
                    *action, "--release", self.config["release"], "--asset-dir", str(self.assets),
                ])

    def test_missing_directory_stops_every_documented_install_command(self):
        shutil.rmtree(self.assets)
        for command_id in ("local-verify", "local-install", "local-update-command"):
            with self.subTest(command=command_id):
                result = self.run_command(command_id)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.log.exists())


if __name__ == "__main__":
    unittest.main()
