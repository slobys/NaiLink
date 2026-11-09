#!/usr/bin/env python3
"""Fetch a checksum-pinned OpenWrt SDK or test kernel for CI."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
OPENWRT_ORIGIN = "https://downloads.openwrt.org/"
OPENWRT_MIRRORS = ("https://mirror.sjtu.edu.cn/openwrt/", "https://downloads.staging.openwrt.org/")


def download_openwrt_file(target, archive):
    primary = target["url"]
    if not primary.startswith(OPENWRT_ORIGIN):
        raise ValueError("Expected a pinned OpenWrt download URL")
    relative = primary[len(OPENWRT_ORIGIN):]
    mirrors = [origin + relative for origin in OPENWRT_MIRRORS]
    with tempfile.TemporaryDirectory(prefix=".openwrt-download-", dir=archive.parent) as temporary:
        partial = Path(temporary) / archive.name
        for url in (primary, *mirrors):
            try:
                subprocess.run(["curl", "--fail", "--location", "--proto", "=https",
                                "--silent", "--show-error", "--connect-timeout", "15",
                                "--max-time", "240", "--speed-limit", "65536", "--speed-time", "30",
                                "--output", str(partial), url], check=True)
                with partial.open("rb") as stream:
                    digest = hashlib.file_digest(stream, "sha256").hexdigest()
                if digest != target["sha256"]:
                    raise ValueError("OpenWrt download checksum mismatch")
            except (subprocess.CalledProcessError, ValueError) as error:
                print(f"OpenWrt download failed for {url}: {error}", flush=True)
                partial.unlink(missing_ok=True)
                continue
            partial.replace(archive)
            return
    raise ValueError("Failed to download the checksum-pinned OpenWrt file")


def main():
    matrix = {row["name"]: row for row in json.loads((ROOT / "openwrt/sdk-matrix.json").read_text())}
    kernels = json.loads((ROOT / "tests/openwrt/kernel-vm-matrix.json").read_text())
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--target", choices=matrix)
    source.add_argument("--kernel", choices=kernels)
    parser.add_argument("--work", type=Path, required=True)
    args = parser.parse_args()
    args.work.mkdir(parents=True, exist_ok=True)
    if args.kernel:
        download_openwrt_file(kernels[args.kernel], args.work / "kernel")
    else:
        download_openwrt_file(matrix[args.target], args.work / (args.target + ".tar.zst"))


if __name__ == "__main__":
    main()
