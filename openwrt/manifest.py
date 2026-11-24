#!/usr/bin/env python3
"""Validate all SDK artifacts and produce the installer manifest."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil

HERE = Path(__file__).resolve().parent


def kernel_btf_records(directory, version, targets):
    """Bind each fallback to one firmware, target and installed kernel ABI."""
    source = HERE / "kernel-btf"
    identities = set()
    filenames = set()
    supported = {
        (target["format"], arch): target["cpu"]
        for target in targets for arch in target["architectures"]
    }
    fields = ("firmware", "release", "revision", "target", "architecture",
              "package_format", "kernel", "kernel_package_version")
    for entry in json.loads((source / "index.json").read_text(encoding="utf-8")):
        identity = [entry[key] for key in fields]
        if any(not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9./_+~-]*", value) for value in identity):
            raise ValueError("Invalid firmware identity for kernel BTF")
        cpu = supported.get((entry["package_format"], entry["architecture"]))
        if cpu is None:
            raise ValueError("Kernel BTF does not match a supported package ABI")
        key = hashlib.sha256(("\n".join(identity) + "\n").encode("ascii")).hexdigest()
        name, checksum = entry["file"], entry["sha256"]
        if (key in identities or name in filenames
                or not re.fullmatch(r"nailink-btf-[A-Za-z0-9._-]+\.btf", name)):
            raise ValueError("Invalid or duplicate kernel BTF entry")
        file = source / name
        if file.is_symlink() or hashlib.sha256(file.read_bytes()).hexdigest() != checksum:
            raise ValueError("Kernel BTF checksum mismatch: " + name)
        shutil.copyfile(file, directory / name)
        identities.add(key)
        filenames.add(name)
        yield ["# kernel-btf", key, cpu, name, checksum, version]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"\d+\.\d+\.\d+", args.version):
        parser.error("version must be X.Y.Z")
    rows = []
    seen = set()
    for record in sorted(args.directory.glob("*.tsv")):
        if record.name == "manifest.tsv":
            continue
        for line in record.read_text().splitlines():
            row = line.split("\t")
            if len(row) != 6 or row[5] != args.version:
                raise ValueError("Malformed or mismatched SDK record")
            fmt, arch, package, name, digest, _ = row
            if fmt not in ("ipk", "apk") or package not in ("daed-router", "luci-app-daed-router"):
                raise ValueError("Unexpected package record")
            if not re.fullmatch(r"[a-zA-Z0-9_-]+", arch) or not re.fullmatch(r"[a-zA-Z0-9_-][a-zA-Z0-9._-]*", name):
                raise ValueError("Invalid package path or architecture")
            if not name.endswith("." + fmt):
                raise ValueError("Wrong package format")
            key = (fmt, arch, package)
            if key in seen:
                raise ValueError("Duplicate package record")
            seen.add(key)
            if hashlib.sha256((args.directory / name).read_bytes()).hexdigest() != digest:
                raise ValueError("Package checksum mismatch")
            rows.append(row)
    targets = json.loads((HERE / "sdk-matrix.json").read_text())
    for target in targets:
        expected = [(arch, "daed-router") for arch in target["architectures"]]
        expected.append(("all", "luci-app-daed-router"))
        for arch, package in expected:
            if (target["format"], arch, package) not in seen:
                raise ValueError(f"Missing mandatory package: {target['format']}/{arch}/{package}")
    checker = args.directory / "daed-check"
    shutil.copyfile(HERE / "files" / "daed-check", checker)
    rows.append(["support", "all", "check", checker.name, hashlib.sha256(checker.read_bytes()).hexdigest(), args.version])
    for cpu in sorted({target["cpu"] for target in targets}):
        probe = args.directory / ("btf-probe-" + cpu)
        rows.append(["support", cpu, "btf-probe", probe.name, hashlib.sha256(probe.read_bytes()).hexdigest(), args.version])
    rows.extend(kernel_btf_records(args.directory, args.version, targets))
    installer = (HERE / "install.sh").read_text(encoding="utf-8")
    installer, replacements = re.subn(r"^release=openwrt-v\d+\.\d+\.\d+$", "release=openwrt-v" + args.version, installer, flags=re.MULTILINE)
    if replacements != 1:
        raise ValueError("Installer release tag cannot be set unambiguously")
    (args.directory / "install.sh").write_text(installer, encoding="utf-8", newline="\n")
    # V1 readers skip comments, so older installers can still use the manifest.
    records = sorted(rows)
    sizes = sorted({row[3] for row in records})
    (args.directory / "manifest.tsv").write_text(
        "# daed-openwrt-manifest-v1\n"
        + "".join(f"# size\t{name}\t{(args.directory / name).stat().st_size}\n" for name in sizes)
        + "".join("\t".join(row) + "\n" for row in records)
    )
    with open(args.directory / "SHA256SUMS", "w") as sums:
        for path in sorted(args.directory.iterdir()):
            if path.is_file() and path.name != "SHA256SUMS":
                sums.write(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n")


if __name__ == "__main__":
    main()
