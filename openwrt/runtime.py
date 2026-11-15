#!/usr/bin/env python3
"""Build a versioned static runtime and helpers for one reviewed CPU profile."""
import argparse
import json
import gzip
import hashlib
import os
from pathlib import Path
import re
import subprocess

from build import ARCHITECTURES, HERE, prepare_sdk, verify_elf


def verify_web_bundle(web, bundled):
    """Reject make/copy failures that otherwise silently embed a stale UI."""
    expected = {p.relative_to(web).as_posix(): p for p in web.rglob("*") if p.is_file()}
    used = set()
    for name, source in expected.items():
        target = bundled / name
        if target.is_file():
            content = target.read_bytes()
        elif not name.endswith(".gz") and target.with_name(target.name + ".gz").is_file():
            target = target.with_name(target.name + ".gz")
            content = gzip.decompress(target.read_bytes())
        else:
            raise ValueError("Bundled frontend missing: " + name)
        if hashlib.sha256(content).digest() != hashlib.sha256(source.read_bytes()).digest():
            raise ValueError("Bundled frontend differs from current build: " + name)
        used.add(target.relative_to(bundled).as_posix())
    actual = {p.relative_to(bundled).as_posix() for p in bundled.rglob("*") if p.is_file()}
    if actual != used or "index.html" not in expected:
        raise ValueError("Unexpected or incomplete bundled frontend; refuse stale assets")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cpu", choices=ARCHITECTURES, required=True)
    parser.add_argument("--wing", type=Path, required=True)
    parser.add_argument("--web", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"\d+\.\d+\.\d+", args.version):
        parser.error("version must be X.Y.Z")
    wing, web = args.wing.resolve(strict=True), args.web.resolve(strict=True)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    profile = ARCHITECTURES[args.cpu]
    env = os.environ.copy()
    # Avoid carrying an incompatible ABI selection from an earlier build.
    for key in ("GOARM", "GOARM64", "GOAMD64", "GOMIPS", "GOMIPS64", "CC", "CGO_CFLAGS", "CGO_LDFLAGS", "GO_LDFLAGS"):
        env.pop(key, None)
    env.update(GOOS="linux", GOARCH=profile["goarch"], CGO_ENABLED="0")
    env.update(profile["env"])
    if profile.get("cgo"):
        matrix = json.loads((HERE / "sdk-matrix.json").read_text())
        sdk = prepare_sdk(next(row for row in matrix if row["name"] == "ipk-" + args.cpu), args.work)
        compilers = list(sdk.glob("staging_dir/toolchain-*/bin/*-openwrt-linux-musl-gcc"))
        if len(compilers) != 1:
            raise ValueError("Pinned SDK must contain one musl cross compiler")
        # MIPS uses the existing CGO SQLite driver. CGO=0 compiles a stub which
        # exits at database initialization; external static linking is required.
        env.update(CGO_ENABLED="1", CC=str(compilers[0]), STAGING_DIR=str(sdk / "staging_dir"))
        # The pinned SQLite amalgamation requests the legacy *64 POSIX names;
        # musl exposes these 64-bit aliases only with this feature macro.
        env["CGO_CFLAGS"] = "-O2 -D_LARGEFILE64_SOURCE"
        env["PATH"] = str(compilers[0].parent) + os.pathsep + env.get("PATH", "")
        env["GO_LDFLAGS"] = "-linkmode=external -extldflags=-static"
    subprocess.run(["python3", str(HERE / "prepare-wing.py"), str(wing)], check=True)
    subprocess.run(["make", "APPNAME=daed", "VERSION=" + args.version, "WEB_DIST=" + str(web),
                    "OUTPUT=" + str(output / "daed"), "bundle"], cwd=wing, env=env, check=True)
    verify_web_bundle(web, wing / "webrender/web")
    # The helpers do not use SQLite and remain pure Go on every architecture.
    env["CGO_ENABLED"] = "0"
    for name in ("btf-probe", "luci-proxy"):
        sources = [str(HERE / (name + ".go"))]
        if name == "luci-proxy":
            sources += [str(HERE / "maintenance.go"), str(HERE / "maintenance-geodata.go"), str(HERE / "maintenance-core.go"), str(HERE / "maintenance-core-updates.go")]
        subprocess.run(["go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(output / name),
                        *sources], cwd=wing, env=env, check=True)
    for name in ("daed", "btf-probe", "luci-proxy"):
        verify_elf(output / name, args.cpu)


if __name__ == "__main__":
    main()
