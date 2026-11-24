"""Portable bundles include only the verified compatibility matrix and core archives."""
import hashlib
import json
from pathlib import Path
import runpy
import tarfile
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2] / "openwrt"
BUILDER = runpy.run_path(str(ROOT / "build-universal.py"))


class UniversalPackageTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.assets = Path(temp.name)
        self.archive = self.assets / "NaiLink-0.1.0-universal.tar.gz"
        self.rows = {}
        for target in json.loads((ROOT / "sdk-matrix.json").read_text()):
            cpu, fmt = target["cpu"], target["format"]
            self.add("support", cpu, "btf-probe", "btf-probe-" + cpu)
            self.add(fmt, "all", "luci-app-daed-router", "luci." + fmt)
            for arch in target["architectures"]:
                self.add(fmt, arch, "daed-router", f"runtime-{arch}.{fmt}")
        self.add("support", "all", "check", "daed-check")
        (self.assets / "install.sh").write_text("#!/bin/sh\nrelease=openwrt-v0.1.0\n")
        self.cores = []
        for engine, cpu, release, filename, _ in BUILDER["pinned_cores"]():
            data = filename.encode()
            (self.assets / filename).write_bytes(data)
            self.cores.append((engine, cpu, release, filename, hashlib.sha256(data).hexdigest()))
        self.manifest()

    def add(self, fmt, arch, package, name):
        data = name.encode()
        (self.assets / name).write_bytes(data)
        self.rows[fmt, arch, package] = "\t".join(
            (fmt, arch, package, name, hashlib.sha256(data).hexdigest(), "0.1.0")
        )

    def manifest(self):
        (self.assets / "manifest.tsv").write_text(
            "# daed-openwrt-manifest-v1\n" + "\n".join(self.rows.values()) + "\n"
        )

    def build(self):
        with patch.dict(BUILDER["core_files"].__globals__, pinned_cores=lambda: self.cores):
            BUILDER["write_bundle"](self.assets, self.archive, "0.1.0", self.assets, False)

    def test_complete_bundle_is_reproducible_and_preserves_package_bytes(self):
        self.build()
        original = self.archive.read_bytes()
        self.build()
        self.assertEqual(self.archive.read_bytes(), original)
        with tarfile.open(self.archive) as bundle:
            self.assertTrue(all(member.isfile() for member in bundle.getmembers()))
            prefix = "NaiLink-0.1.0/"
            manifest = bundle.extractfile(prefix + "assets/manifest.tsv").read().decode()
            for filename in ("runtime-aarch64_generic.apk", "runtime-x86_64.ipk"):
                self.assertIn(filename, manifest)
                self.assertEqual(bundle.extractfile(prefix + "assets/" + filename).read(), filename.encode())
            self.assertNotIn("mips", manifest)
            self.assertIn(prefix + "assets/cores/mihomo-arm64.tar.gz", bundle.getnames())
            self.assertIn(prefix + "install.sh", bundle.getnames())

    def test_incomplete_or_corrupt_assets_prevent_publication(self):
        key = ("apk", "aarch64_generic", "daed-router")
        record = self.rows.pop(key)
        self.manifest()
        with self.assertRaisesRegex(ValueError, "Missing runtime targets"):
            self.build()
        self.rows[key] = record
        self.manifest()
        (self.assets / self.cores[0][3]).write_bytes(b"bad archive")
        with self.assertRaisesRegex(ValueError, "Compatibility core checksum mismatch"):
            self.build()
        self.assertFalse(self.archive.exists())
