#!/usr/bin/env python3
"""Assemble a portable NaiLink installer from verified release artifacts."""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile


ROOT = Path(__file__).resolve().parent
RELEASE = "https://github.com/slobys/NaiLink/releases/download"
CPUS = {"x86_64", "arm64"}
MANIFEST_HEADER = "# daed-openwrt-manifest-v1"
README = """NaiLink 通用安装包

适用于兼容的 x86_64 / ARM64 OpenWrt、iStoreOS、ImmortalWrt。
上传到软路由并解压，进入解压后的目录，以 root 执行 sh install.sh 打开中文菜单。
安装时从本包读取 NaiLink 和可选内核。缺少系统依赖时仍使用当前固件自己的软件源。
升级其他版本时，把原版的 manifest.tsv 和本机对应的运行包、LuCI 包放进 assets/rollback。
已有版本与本包相同时可直接重装。安装后仍可通过 LuCI 使用原来的在线更新。

使用说明 https://slobys.github.io/NaiLink/installation.html#portable
"""


def sha256(path):
    with path.open("rb") as reader:
        return hashlib.file_digest(reader, "sha256").hexdigest()


def release_files(directory, version):
    targets = json.loads((ROOT / "sdk-matrix.json").read_text())
    expected = {("support", "all", "check")}
    for target in targets:
        if target["cpu"] in CPUS:
            fmt = target["format"]
            expected.add(("support", target["cpu"], "btf-probe"))
            expected.add((fmt, "all", "luci-app-daed-router"))
            expected.update((fmt, arch, "daed-router") for arch in target["architectures"])

    lines = (directory / "manifest.tsv").read_text().splitlines()
    if not lines or lines[0] != MANIFEST_HEADER:
        raise ValueError("Invalid release manifest")
    assets = {}
    rows = []
    seen = set()
    for line in lines[1:]:
        if not line or (line.startswith("#") and not line.startswith("# kernel-btf\t")):
            continue
        fmt, arch, package, name, checksum, release_version = line.split("\t")
        key = (fmt, arch, package)
        if fmt == "# kernel-btf":
            if package not in CPUS:
                continue
            if (not re.fullmatch(r"[a-f0-9]{64}", arch)
                    or not re.fullmatch(r"nailink-btf-[A-Za-z0-9._-]+\.btf", name)):
                raise ValueError("Invalid fallback kernel BTF entry")
        elif key not in expected:
            continue
        if key in seen or name in assets or release_version != version or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", name):
            raise ValueError("Unexpected release manifest entry")
        asset = directory / name
        if asset.is_symlink() or sha256(asset) != checksum:
            raise ValueError(f"Release checksum mismatch for {name}")
        seen.add(key)
        rows.append(line)
        assets[name] = asset
    if not expected <= seen:
        raise ValueError(f"Missing runtime targets {sorted(expected - seen)}")
    installer = directory / "install.sh"
    if f"\nrelease=openwrt-v{version}\n" not in installer.read_text(encoding="utf-8"):
        raise ValueError("Installer and manifest versions differ")
    manifest = "\n".join(
        [MANIFEST_HEADER]
        + [f"# size\t{name}\t{asset.stat().st_size}" for name, asset in sorted(assets.items())]
        + sorted(rows)
    ) + "\n"
    return {"install.sh": installer, **assets}, manifest


def pinned_cores():
    for engine, path, release_const, map_name in (
        ("sing-box", "state.go", "SingBoxRelease", "Releases"),
        ("mihomo", "mihomo.go", "ReviewedMihomoRelease", "ReviewedMihomoReleases"),
    ):
        source = (ROOT / "corelib" / path).read_text()
        release = re.search(r'const ' + release_const + r' = "([A-Za-z0-9.-]+)"', source)[1]
        rows = re.search(r"(?ms)^var " + map_name + r" = map\[string\]Release\{\n(.*?)^}", source)[1]
        found = re.findall(r'\{"(x86_64|arm64)", "([A-Za-z0-9._-]+)", "([a-f0-9]{64})"\}', rows)
        if {cpu for cpu, _, _ in found} != CPUS or len(found) != len(CPUS):
            raise ValueError(f"Missing or duplicate reviewed {engine} archives")
        for cpu, filename, digest in found:
            yield engine, cpu, release, filename, digest


def core_files(directory, cache, download):
    assets = {}
    sums = []
    for engine, cpu, release, filename, checksum in pinned_cores():
        asset = directory / filename
        if not asset.is_file():
            asset = cache / filename
            if not download:
                raise FileNotFoundError(filename)
            subprocess.run(
                ["curl", "-fLsS", "--proto", "=https", "--retry", "2", "--max-time", "180",
                 "-o", str(asset), f"{RELEASE}/{release}/{filename}"],
                check=True,
            )
        if asset.is_symlink() or asset.stat().st_size > 128 << 20 or sha256(asset) != checksum:
            raise ValueError(f"Compatibility core checksum mismatch for {filename}")
        name = f"{engine}-{cpu}.tar.gz"
        assets[f"assets/cores/{name}"] = asset
        sums.append(f"{checksum}  {name}\n")
    return assets, "".join(sorted(sums))


def write_bundle(directory, output, version, core_dir, download):
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version):
        raise ValueError("Version must be X.Y.Z")
    assets, manifest = release_files(directory, version)
    top = f"NaiLink-{version}"
    with tempfile.TemporaryDirectory(dir=output.parent) as temp:
        cache = Path(temp)
        cores, sums = core_files(core_dir, cache, download)
        files = {f"assets/{name}": asset for name, asset in assets.items()}
        files.update(cores)
        files["install.sh"] = ROOT / "universal-install.sh"
        text = {"README.txt": README, "assets/manifest.tsv": manifest, "assets/cores/SHA256SUMS": sums}
        archive = cache / output.name
        with archive.open("wb") as compressed, gzip.GzipFile(filename="", mode="wb", fileobj=compressed, mtime=0) as gz:
            with tarfile.open(fileobj=gz, mode="w|", format=tarfile.USTAR_FORMAT) as tar:
                for name, item in sorted(files.items()):
                    member = tarfile.TarInfo(f"{top}/{name}")
                    member.size = item.stat().st_size
                    member.mode = 0o644
                    with item.open("rb") as stream:
                        tar.addfile(member, stream)
                for name, content in sorted(text.items()):
                    data = content.encode("utf-8")
                    member = tarfile.TarInfo(f"{top}/{name}")
                    member.size = len(data)
                    member.mode = 0o644
                    tar.addfile(member, io.BytesIO(data))
        archive.replace(output)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--core-dir", type=Path)
    parser.add_argument("--download-reviewed-cores", action="store_true")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    write_bundle(args.directory, args.output, args.version, args.core_dir or args.directory, args.download_reviewed_cores)
