#!/usr/bin/env python3
"""Validate an OpenWrt artifact set and upload only to a matching release draft."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def check_version(version):
    if not re.fullmatch(r"\d+\.\d+\.\d+", version):
        raise ValueError("Version must be X.Y.Z")
    make = (ROOT / "openwrt/Makefile").read_text(encoding="utf-8")
    installer = (ROOT / "openwrt/install.sh").read_text(encoding="utf-8")
    # Public documentation stays on the published release while this source is
    # built and reviewed as a draft. Only the package and installer must match.
    if (not re.search(r"^PKG_VERSION:=.*," + re.escape(version) + r"\)$", make, re.M)
            or "\nrelease=openwrt-v" + version + "\n" not in installer):
        raise ValueError("Release version differs from source or installer")


def validate_assets(directory, version, sha):
    check_version(version)
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("Release requires an exact commit SHA")
    if (directory / "SHA256SUMS").is_symlink():
        raise ValueError("Checksum list must be a regular file")
    assets = {}
    for line in (directory / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)", line)
        if not match or match[2] in assets or match[2] == "SHA256SUMS":
            raise ValueError("Malformed or duplicate checksum entry")
        assets[match[2]] = match[1]
    required = {"install.sh", "manifest.tsv", "SOURCE_COMMIT.txt", "SOURCE_SUBMODULES.txt", "daed-openwrt-source.tar.gz"}
    required |= {"nailink-sing-box-1.14.3-nailink.1-" + suffix + ".tar.gz" for suffix in ("x86_64", "arm64", "source")}
    required |= {"nailink-sing-box-extra-1.14.3-nailink.2-" + suffix + ".tar.gz" for suffix in ("x86_64", "arm64", "source")}
    required |= {"nailink-mihomo-1.19.33-" + suffix + ".tar.gz" for suffix in ("x86_64", "arm64", "source")}
    if not required <= assets.keys():
        raise ValueError("Missing installer, manifest or corresponding source")
    if {p.name for p in directory.iterdir()} != assets.keys() | {"SHA256SUMS"}:
        raise ValueError("Unexpected or missing release assets")
    for name, digest in assets.items():
        path = directory / name
        if path.is_symlink() or not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            raise ValueError("Asset checksum mismatch: " + name)
    if (directory / "SOURCE_COMMIT.txt").read_text().strip() != sha:
        raise ValueError("Source commit differs from release commit")
    if "\nrelease=openwrt-v" + version + "\n" not in (directory / "install.sh").read_text():
        raise ValueError("Installer points to another release")
    lines = (directory / "manifest.tsv").read_text().splitlines()
    if not lines or lines[0] != "# daed-openwrt-manifest-v1":
        raise ValueError("Invalid installer manifest")
    seen = set()
    listed = set()
    for line in lines[1:]:
        row = line.split("\t")
        if len(row) != 6 or row[5] != version or assets.get(row[3]) != row[4]:
            raise ValueError("Manifest differs from release assets")
        key = tuple(row[:3])
        if key in seen:
            raise ValueError("Duplicate manifest target")
        seen.add(key)
        listed.add(row[3])
    targets = json.loads((ROOT / "openwrt/sdk-matrix.json").read_text())
    allowed = listed | required | {"README.md", "VALIDATION.md"} | {target["name"] + ".tsv" for target in targets}
    if assets.keys() - allowed:
        raise ValueError("Unexpected unreviewed release assets")
    for target in targets:
        for arch in target["architectures"]:
            if (target["format"], arch, "daed-router") not in seen:
                raise ValueError("Missing runtime target: " + target["name"])
        if (target["format"], "all", "luci-app-daed-router") not in seen:
            raise ValueError("Missing LuCI target: " + target["name"])
        if ("support", target["cpu"], "btf-probe") not in seen:
            raise ValueError("Missing CPU probe: " + target["cpu"])
    if ("support", "all", "check") not in seen:
        raise ValueError("Missing compatibility checker")
    return [directory / name for name in sorted(assets)] + [directory / "SHA256SUMS"]


def gh(*args, missing_ok=False):
    result = subprocess.run(["gh", *args], capture_output=True, text=True, check=False)
    if result.returncode:
        if missing_ok and ("HTTP 404" in result.stderr or result.stderr.strip() == "release not found"):
            return None
        raise RuntimeError(result.stderr.strip() or "GitHub request failed")
    return result.stdout


def publish(directory, version, sha, repository):
    assets = validate_assets(directory, version, sha)
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("Invalid repository")
    tag = "openwrt-v" + version
    existing = gh("release", "view", tag, "--repo", repository, "--json", "isDraft,targetCommitish", missing_ok=True)
    if existing is not None:
        release = json.loads(existing)
        if not release["isDraft"] or release["targetCommitish"] != sha:
            raise ValueError("Refusing to change an existing published or unrelated release")
    # A missing name passed to /commits returns HTTP 422, not 404. Query the
    # exact Git ref first; only a genuine missing ref is safe to create.
    remote_ref = gh("api", "repos/" + repository + "/git/ref/tags/" + tag, "--jq", ".object.sha", missing_ok=True)
    if remote_ref is not None:
        # /commits dereferences both lightweight and annotated tags. A ref
        # which exists but cannot resolve must remain an error.
        remote_sha = gh("api", "repos/" + repository + "/commits/" + tag, "--jq", ".sha")
        if remote_sha.strip() != sha:
            raise ValueError("Existing release tag points to another commit")
    if existing is None:
        with tempfile.TemporaryDirectory() as temporary:
            notes = Path(temporary) / "notes.md"
            notes.write_text(f"NaiLink {version} 测试版草稿。\n\n源码提交：{sha}\n\n"
                             "全部构建与回归已通过；维护者检查安装包和验证边界后再发布。\n\n"
                             "ARMv7 / MIPS 仅实验构建，当前不推荐用于透明代理。\n", encoding="utf-8")
            gh("release", "create", tag, "--repo", repository, "--target", sha, "--draft", "--prerelease",
               "--title", "NaiLink " + version, "--notes-file", str(notes))
    # --clobber is restricted above to this commit's unpublished draft, for retries.
    gh("release", "upload", tag, "--repo", repository, "--clobber", *(str(path) for path in assets))
    print(f"Draft ready: https://github.com/{repository}/releases/tag/{tag}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    check = commands.add_parser("check-version")
    check.add_argument("version")
    release = commands.add_parser("publish")
    release.add_argument("directory", type=Path)
    release.add_argument("--version", required=True)
    release.add_argument("--sha", required=True)
    release.add_argument("--repository", required=True)
    args = parser.parse_args()
    if args.command == "check-version":
        check_version(args.version)
    else:
        publish(args.directory, args.version, args.sha, args.repository)
