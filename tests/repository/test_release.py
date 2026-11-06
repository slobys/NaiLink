import contextlib
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("publish_openwrt", ROOT / "scripts/publish-openwrt.py")
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)
VERSION = re.search(r"PKG_VERSION:=.*,(\d+\.\d+\.\d+)\)", (ROOT / "openwrt/Makefile").read_text())[1]
SHA = "a" * 40


class ReleaseSafetyTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.rows = []
        self.calls = []
        self.existing = None
        self.tag_sha = None
        seen = set()
        for target in json.loads((ROOT / "openwrt/sdk-matrix.json").read_text()):
            for arch, package in [(arch, "daed-router") for arch in target["architectures"]] + [("all", "luci-app-daed-router")]:
                key = (target["format"], arch, package)
                if key not in seen:
                    seen.add(key)
                    self.asset(*key, f"{package}_{arch}.{target['format']}")
            key = ("support", target["cpu"], "btf-probe")
            if key not in seen:
                seen.add(key)
                self.asset(*key, "btf-probe-" + target["cpu"])
        self.asset("support", "all", "check", "daed-check")
        (self.directory / "SOURCE_COMMIT.txt").write_text(SHA + "\n")
        (self.directory / "SOURCE_SUBMODULES.txt").write_text("fixed submodules\n")
        (self.directory / "daed-openwrt-source.tar.gz").write_bytes(b"test source")
        for suffix in ("x86_64", "arm64", "source"):
            (self.directory / ("nailink-sing-box-1.14.3-nailink.1-" + suffix + ".tar.gz")).write_bytes(b"core fixture")
        for suffix in ("x86_64", "arm64", "source"):
            (self.directory / ("nailink-sing-box-extra-1.14.3-nailink.2-" + suffix + ".tar.gz")).write_bytes(b"extension fixture")
        for suffix in ("x86_64", "arm64", "source"):
            (self.directory / ("nailink-mihomo-1.19.33-" + suffix + ".tar.gz")).write_bytes(b"mihomo fixture")
        (self.directory / "install.sh").write_text("#!/bin/sh\nrelease=openwrt-v" + VERSION + "\n")
        self.manifest()

    def asset(self, fmt, arch, package, name):
        data = name.encode()
        (self.directory / name).write_bytes(data)
        self.rows.append([fmt, arch, package, name, hashlib.sha256(data).hexdigest(), VERSION])

    def manifest(self):
        (self.directory / "manifest.tsv").write_text("# daed-openwrt-manifest-v1\n" + "".join("\t".join(row) + "\n" for row in self.rows))
        self.checksums()

    def checksums(self):
        (self.directory / "SHA256SUMS").write_text("".join(
            hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.name + "\n"
            for p in sorted(self.directory.iterdir()) if p.name != "SHA256SUMS"))

    def fake_gh(self, args, **kwargs):
        self.calls.append(args)
        if args[1:3] == ["release", "view"]:
            return subprocess.CompletedProcess(args, 0 if self.existing else 1,
                                               json.dumps(self.existing) if self.existing else "", "" if self.existing else "release not found")
        if args[1] == "api":
            missing = "No commit found for SHA (HTTP 422)" if "/commits/" in args[2] else "HTTP 404"
            return subprocess.CompletedProcess(args, 0 if self.tag_sha else 1, self.tag_sha or "", "" if self.tag_sha else missing)
        return subprocess.CompletedProcess(args, 0, "", "")

    def publish(self):
        with patch.object(release.subprocess, "run", self.fake_gh), contextlib.redirect_stdout(io.StringIO()):
            release.publish(self.directory, VERSION, SHA, "slobys/NaiLink")

    def mutations(self):
        return [c for c in self.calls if c[1:3] in (["release", "create"], ["release", "upload"])]

    def test_valid_complete_artifact_set(self):
        self.assertEqual(len(release.validate_assets(self.directory, VERSION, SHA)), len(list(self.directory.iterdir())))

    def test_missing_optional_core_source_blocks_release(self):
        (self.directory / 'nailink-sing-box-1.14.3-nailink.1-source.tar.gz').unlink()
        self.checksums()
        with self.assertRaises(ValueError):
            self.publish()
        self.assertEqual(self.mutations(), [])

    def test_version_must_match_source(self):
        with self.assertRaises(ValueError):
            release.check_version("999.999.999")

    def test_draft_version_can_be_ahead_of_public_documentation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "docs/site").mkdir(parents=True)
            (root / "docs/site/site.json").write_text(json.dumps({"version": "1.0.0", "release": "openwrt-v1.0.0"}))
            (root / "openwrt").mkdir()
            (root / "openwrt/Makefile").write_text("PKG_VERSION:=$(or $(DAED_VERSION),1.0.1)\n")
            installer = root / "openwrt/install.sh"
            installer.write_text("#!/bin/sh\nrelease=openwrt-v1.0.1\n")
            with patch.object(release, "ROOT", root):
                release.check_version("1.0.1")
                with self.assertRaisesRegex(ValueError, "source or installer"):
                    release.check_version("1.0.0")
                installer.write_text("#!/bin/sh\nrelease=openwrt-v1.0.0\n")
                with self.assertRaisesRegex(ValueError, "source or installer"):
                    release.check_version("1.0.1")

    def test_invalid_version(self):
        with self.assertRaises(ValueError):
            release.check_version("0.6.29; echo invalid")

    def test_corruption_prevents_any_github_call(self):
        (self.directory / "install.sh").write_text("corrupted")
        with self.assertRaisesRegex(ValueError, "checksum"):
            self.publish()
        self.assertEqual(self.calls, [])

    def test_wrong_source_commit(self):
        (self.directory / "SOURCE_COMMIT.txt").write_text("b" * 40)
        self.checksums()
        with self.assertRaisesRegex(ValueError, "Source commit"):
            self.publish()
        self.assertEqual(self.calls, [])

    def test_wrong_installer_release(self):
        (self.directory / "install.sh").write_text("#!/bin/sh\nrelease=openwrt-v1.0.0\n")
        self.checksums()
        with self.assertRaisesRegex(ValueError, "Installer"):
            self.publish()

    def test_missing_architecture_even_with_valid_checksums(self):
        removed = self.rows.pop(0)
        (self.directory / removed[3]).unlink()
        self.manifest()
        with self.assertRaisesRegex(ValueError, "Missing runtime target"):
            self.publish()

    def test_duplicate_target(self):
        self.rows.append(self.rows[0])
        self.manifest()
        with self.assertRaisesRegex(ValueError, "Duplicate manifest"):
            self.publish()

    def test_extra_unreviewed_file(self):
        (self.directory / "private.db").write_bytes(b"private")
        with self.assertRaisesRegex(ValueError, "Unexpected"):
            self.publish()

    def test_even_checksummed_private_file_is_not_uploaded(self):
        (self.directory / "private.db").write_bytes(b"private")
        self.checksums()
        with self.assertRaisesRegex(ValueError, "unreviewed"):
            self.publish()
        self.assertEqual(self.calls, [])

    def test_checksum_path_traversal(self):
        with (self.directory / "SHA256SUMS").open("a") as stream:
            stream.write("a" * 64 + "  ../private.db\n")
        with self.assertRaisesRegex(ValueError, "Malformed"):
            self.publish()

    def test_published_release_is_immutable(self):
        self.existing = {"isDraft": False, "targetCommitish": SHA}
        with self.assertRaisesRegex(ValueError, "existing published"):
            self.publish()
        self.assertEqual(self.mutations(), [])

    def test_other_commit_draft_is_immutable(self):
        self.existing = {"isDraft": True, "targetCommitish": "b" * 40}
        with self.assertRaises(ValueError):
            self.publish()
        self.assertEqual(self.mutations(), [])

    def test_other_commit_tag_is_immutable(self):
        self.tag_sha = "b" * 40
        with self.assertRaisesRegex(ValueError, "tag points"):
            self.publish()
        self.assertEqual(self.mutations(), [])

    def test_create_draft_only_then_upload_all_assets(self):
        self.publish()
        created, uploaded = self.mutations()
        self.assertIn("--draft", created)
        self.assertIn("--prerelease", created)
        self.assertEqual(created[created.index("--target") + 1], SHA)
        self.assertEqual(set(uploaded[7:]), {str(p) for p in self.directory.iterdir()})

    def test_new_release_checks_missing_ref_instead_of_invalid_commit_name(self):
        self.publish()
        queries = [call[2] for call in self.calls if call[1] == "api"]
        self.assertEqual(queries, ["repos/slobys/NaiLink/git/ref/tags/openwrt-v" + VERSION])

    def test_annotated_tag_is_compared_using_its_resolved_commit(self):
        def responses(args, **kwargs):
            if args[1] == "api":
                self.calls.append(args)
                value = SHA if "/commits/" in args[2] else "d" * 40
                return subprocess.CompletedProcess(args, 0, value, "")
            return self.fake_gh(args, **kwargs)
        with patch.object(release.subprocess, "run", responses), contextlib.redirect_stdout(io.StringIO()):
            release.publish(self.directory, VERSION, SHA, "slobys/NaiLink")
        self.assertEqual(len(self.mutations()), 2)

    def test_retry_only_same_commit_draft(self):
        self.existing = {"isDraft": True, "targetCommitish": SHA}
        self.publish()
        self.assertEqual([c[1:3] for c in self.mutations()], [["release", "upload"]])

    def test_permission_or_network_error_is_not_missing_release(self):
        failed = subprocess.CompletedProcess([], 1, "", "HTTP 403: denied")
        with patch.object(release.subprocess, "run", return_value=failed):
            with self.assertRaisesRegex(RuntimeError, "403"):
                release.publish(self.directory, VERSION, SHA, "slobys/NaiLink")
