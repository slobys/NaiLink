#!/usr/bin/env python3
"""Prefetch a checksum-pinned SDK into the OpenWrt CI build cache."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
SDK_ORIGIN = "https://downloads.openwrt.org/"
SDK_MIRROR = "https://downloads.staging.openwrt.org/"


def download_sdk(target, archive):
    primary = target["url"]
    if not primary.startswith(SDK_ORIGIN):
        raise ValueError("Expected a pinned OpenWrt SDK URL")
    mirror = SDK_MIRROR + primary[len(SDK_ORIGIN):]
    with tempfile.TemporaryDirectory(prefix=".sdk-download-", dir=archive.parent) as temporary:
        partial = Path(temporary) / archive.name
        for url in (primary, mirror):
            try:
                subprocess.run(["curl", "--fail", "--location", "--proto", "=https",
                                "--silent", "--show-error", "--connect-timeout", "15",
                                "--max-time", "240", "--speed-limit", "65536", "--speed-time", "30",
                                "--output", str(partial), url], check=True)
                with partial.open("rb") as stream:
                    digest = hashlib.file_digest(stream, "sha256").hexdigest()
                if digest != target["sha256"]:
                    raise ValueError("SDK checksum mismatch")
            except (subprocess.CalledProcessError, ValueError) as error:
                print(f"SDK download failed for {url}: {error}", flush=True)
                partial.unlink(missing_ok=True)
                continue
            partial.replace(archive)
            return
    raise ValueError("Failed to download the checksum-pinned OpenWrt SDK")


def main():
    matrix = {row["name"]: row for row in json.loads((ROOT / "openwrt/sdk-matrix.json").read_text())}
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", choices=matrix, required=True)
    parser.add_argument("--work", type=Path, required=True)
    args = parser.parse_args()
    args.work.mkdir(parents=True, exist_ok=True)
    archive = args.work / (args.target + ".tar.zst")
    download_sdk(matrix[args.target], archive)


if __name__ == "__main__":
    main()
