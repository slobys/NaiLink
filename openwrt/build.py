#!/usr/bin/env python3
"""Package a static Daed binary using a checksum-pinned OpenWrt SDK (Linux x86_64)."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import struct
import subprocess
import tarfile
import tempfile
import urllib.request

HERE = Path(__file__).resolve().parent
ARCHITECTURES = {
    "x86_64": {"goarch": "amd64", "elf": (2, 1, 62), "env": {"GOAMD64": "v1"}},
    "arm64": {"goarch": "arm64", "elf": (2, 1, 183), "env": {"GOARM64": "v8.0"}},
    "armv7": {"goarch": "arm", "elf": (1, 1, 40), "env": {"GOARM": "7,softfloat"}},
    "mips": {"goarch": "mips", "elf": (1, 2, 8), "env": {"GOMIPS": "softfloat"}, "cgo": True},
    "mipsle": {"goarch": "mipsle", "elf": (1, 1, 8), "env": {"GOMIPS": "softfloat"}, "cgo": True},
}


def pin_base_feed(feeds, commit):
    # Newer SDKs add options such as --root=package before the feed name.
    # Preserve those options while changing only the base feed revision.
    pattern = r"^src-git(?:-full)?((?:[ \t]+--[^ \t\r\n]+)*)[ \t]+base[ \t]+[^\r\n]*$"
    result, count = re.subn(pattern, lambda match: "src-git" + match[1] + " base https://github.com/openwrt/openwrt.git^" + commit, feeds, flags=re.MULTILINE)
    if count != 1:
        raise ValueError("SDK base feed cannot be pinned unambiguously")
    return result


def verify_elf(path, cpu):
    elf_class, encoding, expected_machine = ARCHITECTURES[cpu]["elf"]
    endian = "<" if encoding == 1 else ">"
    with open(path, "rb") as stream:
        header = stream.read(64)
        if len(header) != 64 or header[:6] != b"\x7fELF" + bytes((elf_class, encoding)):
            raise ValueError("Runtime ELF bitness/byte order does not match the SDK matrix")
        machine = struct.unpack_from(endian + "H", header, 18)[0]
        if machine != expected_machine:
            raise ValueError("Runtime ELF architecture does not match the SDK matrix")
        offset = struct.unpack_from(endian + ("Q" if elf_class == 2 else "I"), header, 32 if elf_class == 2 else 28)[0]
        size, count = struct.unpack_from(endian + "HH", header, 54 if elf_class == 2 else 42)
        if size < (56 if elf_class == 2 else 32) or not count:
            raise ValueError("Runtime has no valid program headers")
        for index in range(count):
            stream.seek(offset + size * index)
            entry = stream.read(size)
            if len(entry) != size:
                raise ValueError("Truncated ELF")
            if struct.unpack_from(endian + "I", entry)[0] == 3:
                raise ValueError("Runtime has a dynamic interpreter; a fully static binary is required")


def prepare_sdk(target, work):
    """Fetch the pinned SDK before building CGO architectures or native packages."""
    work = Path(work).resolve()
    work.mkdir(parents=True, exist_ok=True)
    archive = work / (target["name"] + ".tar.zst")
    if not archive.exists():
        partial = archive.with_suffix(".download")
        urllib.request.urlretrieve(target["url"], partial)
        if sha256(partial) != target["sha256"]:
            partial.unlink()
            raise ValueError("SDK checksum mismatch")
        partial.replace(archive)
    if sha256(archive) != target["sha256"]:
        raise ValueError("SDK checksum mismatch; remove the failed download and retry")
    sdk = work / target["name"]
    if not sdk.exists():
        with tempfile.TemporaryDirectory(prefix=".sdk-unpack-", dir=work) as temporary:
            run("tar", "--zstd", "-xf", str(archive), "--strip-components=1", "-C", temporary, cwd=work)
            Path(temporary).rename(sdk)
    return sdk


def sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run(*args, cwd, env=None):
    subprocess.run(args, cwd=cwd, env=env, check=True)


def verify_package(path, architecture, package, cpu, sdk, version):
    """Check the native metadata and the final payload, after SDK stripping."""
    with tempfile.TemporaryDirectory(prefix="daed-package-verify-") as temporary:
        root = Path(temporary)
        if path.suffix == ".ipk":
            with tarfile.open(path) as outer:
                control_name = next(name for name in outer.getnames() if name.endswith("control.tar.gz"))
                with tarfile.open(fileobj=io.BytesIO(outer.extractfile(control_name).read())) as control:
                    name = next(name for name in control.getnames() if name.lstrip("./") == "control")
                    metadata = dict(line.split(": ", 1) for line in control.extractfile(name).read().decode().splitlines() if ": " in line)
                if metadata.get("Architecture") != architecture or metadata.get("Package") != package:
                    raise ValueError("IPK metadata does not match the release record")
                if metadata.get("Version") != version + "-r1":
                    raise ValueError("IPK version mismatch")
                data_name = next(name for name in outer.getnames() if name.endswith("data.tar.gz"))
                with tarfile.open(fileobj=io.BytesIO(outer.extractfile(data_name).read())) as data:
                    for member in data.getmembers():
                        if not member.isfile() and not member.issym():
                            continue
                        relative = member.name.removeprefix("./")
                        destination = (root / relative).resolve()
                        destination.relative_to(root)
                        destination.parent.mkdir(parents=True, exist_ok=True)
                        if member.issym():
                            (destination.parent / member.linkname).resolve().relative_to(root)
                            destination.symlink_to(member.linkname)
                            continue
                        destination.write_bytes(data.extractfile(member).read())
                        destination.chmod(member.mode)
        else:
            apk = sdk / "staging_dir/host/bin/apk"
            metadata = subprocess.check_output([str(apk), "adbdump", str(path)], text=True)
            for key, expected in (("arch", "noarch" if architecture == "all" else architecture), ("name", package), ("version", version + "-r1")):
                match = re.search(r"^  " + key + r": (.+)$", metadata, re.MULTILINE)
                if not match or match[1] != expected:
                    raise ValueError("APK metadata does not match the release record: " + key)
            subprocess.run([str(apk), "verify", "--allow-untrusted", str(path)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run([str(apk), "extract", "--allow-untrusted", "--no-chown", "--destination", str(root), str(path)], check=True, stdout=subprocess.DEVNULL)
        if package == "daed-router":
            for name in ("geosite.dat", "geoip.dat"):
                link = root / "usr/share/daed" / name
                if not link.is_symlink() or link.readlink().as_posix() != "../v2ray/" + name:
                    raise ValueError("Missing or incorrect packaged geodata link: " + name)
            verify_elf(root / "usr/bin/daed", cpu)
            expected = {"etc/init.d/daed": "files/daed.init", "usr/libexec/daed/check": "files/daed-check", "lib/upgrade/keep.d/daed-router": "files/keep-daed"}
            expected["usr/libexec/daed/subscription-proxy"] = "files/subscription-proxy"
            expected["usr/sbin/nailink"] = "files/nailink"
            expected["usr/libexec/daed/network-check"] = "network-check.sh"
            verify_elf(root / "usr/libexec/daed/btf-probe", cpu)
            verify_elf(root / "usr/libexec/daed/luci-proxy", cpu)
            executables = ["usr/bin/daed", "etc/init.d/daed", "usr/libexec/daed/check", "usr/libexec/daed/btf-probe", "usr/libexec/daed/luci-proxy"]
            executables.append("usr/libexec/daed/subscription-proxy")
            installer = (HERE / "install.sh").read_text()
            installer = re.sub(r"^release=openwrt-v\d+\.\d+\.\d+$", "release=openwrt-v" + version, installer, flags=re.MULTILINE)
            if (root / "usr/sbin/daed-router-setup").read_text() != installer:
                raise ValueError("Packaged setup script differs from the versioned installer")
            executables.append("usr/sbin/daed-router-setup")
            executables.append("usr/sbin/nailink")
            executables.append("usr/libexec/daed/network-check")
            expected["usr/libexec/daed/nailink-maintenance"] = "files/maintenance.init"
            executables.append("usr/libexec/daed/nailink-maintenance")
        else:
            expected = {"usr/share/luci/menu.d/luci-app-daed-router.json": "files/menu.json", "usr/share/rpcd/acl.d/luci-app-daed-router.json": "files/acl.json", "usr/libexec/rpcd/daed-router": "files/rpcd-daed"}
            expected.update({"www/luci-static/resources/view/daed-router/" + path.name: "htdocs/" + path.name for path in (HERE / "htdocs").glob("*.js")})
            executables = ["usr/libexec/rpcd/daed-router"]
            expected["www/cgi-bin/daed-router"] = "files/luci-cgi"
            executables.append("www/cgi-bin/daed-router")
            menu = json.loads((root / "usr/share/luci/menu.d/luci-app-daed-router.json").read_text())
            for entry in menu.values():
                action = entry.get("action", {})
                if action.get("type") == "view":
                    module = root / "www/luci-static/resources/view" / (action["path"] + ".js")
                    if not module.is_file():
                        raise ValueError("LuCI menu references a missing view: " + action["path"])
            for obsolete in ("overview.js", "settings.js", "dashboard.js"):
                if (root / "www/luci-static/resources/view/daed-router" / obsolete).exists():
                    raise ValueError("Obsolete LuCI page remains in package")
        for installed, source in expected.items():
            if (root / installed).read_bytes() != (HERE / source).read_bytes():
                raise ValueError("Package payload differs from its source: " + installed)
        for installed in executables:
            if not (root / installed).stat().st_mode & 0o111:
                raise ValueError("Required executable permission is missing: " + installed)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--btf-probe", type=Path, required=True)
    parser.add_argument("--luci-proxy", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--work", type=Path, required=True, help="Dedicated SDK/cache directory")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"\d+\.\d+\.\d+", args.version):
        parser.error("version must be X.Y.Z")
    matrix = json.loads((HERE / "sdk-matrix.json").read_text())
    target = next((row for row in matrix if row["name"] == args.target), None)
    if not target:
        parser.error("unknown SDK target")
    args.binary = args.binary.resolve(strict=True)
    verify_elf(args.binary, target["cpu"])
    args.btf_probe = args.btf_probe.resolve(strict=True)
    verify_elf(args.btf_probe, target["cpu"])
    args.luci_proxy = args.luci_proxy.resolve(strict=True)
    verify_elf(args.luci_proxy, target["cpu"])
    args.work = args.work.resolve()
    args.output = args.output.resolve()
    args.work.mkdir(parents=True, exist_ok=True)
    args.output.mkdir(parents=True, exist_ok=True)
    sdk = prepare_sdk(target, args.work)
    package = sdk / "package" / "daed-router"
    package.mkdir(parents=True, exist_ok=True)
    shutil.copy2(HERE / "Makefile", package)
    shutil.copy2(HERE / "network-check.sh", package)
    installer = (HERE / "install.sh").read_text()
    installer = re.sub(r"^release=openwrt-v\d+\.\d+\.\d+$", "release=openwrt-v" + args.version, installer, flags=re.MULTILINE)
    (package / "install.sh").write_text(installer)
    for directory in ("files", "htdocs"):
        if (package / directory).exists():
            shutil.rmtree(package / directory)
        shutil.copytree(HERE / directory, package / directory, dirs_exist_ok=True)
    env = os.environ.copy()
    env.update(DAED_BINARY=str(args.binary), DAED_BTF_PROBE=str(args.btf_probe), DAED_LUCI_PROXY=str(args.luci_proxy), DAED_VERSION=args.version)
    # The SDK pins packages/LuCI, but its base feed follows a branch. Pin that
    # feed to the matching firmware release as well, using a shallow checkout.
    feeds = (sdk / "feeds.conf.default").read_text()
    (sdk / "feeds.conf").write_text(pin_base_feed(feeds, target["base_commit"]))
    run("./scripts/feeds", "update", "base", "packages", "luci", cwd=sdk, env=env)
    run("./scripts/feeds", "install", "ca-bundle", "jshn", "jsonfilter", "uclient-fetch", "rpcd", "luci-base", "v2ray-geoip", "v2ray-geosite", cwd=sdk, env=env)
    with open(sdk / ".config", "a") as stream:
        stream.write("\n# CONFIG_ALL is not set\n# CONFIG_ALL_NONSHARED is not set\n# CONFIG_ALL_KMODS is not set\nCONFIG_PACKAGE_daed-router=m\nCONFIG_PACKAGE_luci-app-daed-router=m\n# CONFIG_SIGNED_PACKAGES is not set\n")
    run("make", "defconfig", cwd=sdk, env=env)
    records = []
    for architecture in target["architectures"]:
        if not re.fullmatch(r"[a-zA-Z0-9_-]+", architecture):
            raise ValueError("Invalid package architecture")
        env["DAED_PKGARCH"] = architecture
        # Both packages contain prebuilt binaries/static JS. Invoke this package
        # directly so the SDK does not rebuild all of its runtime dependencies.
        # Dependency declarations still go into the native package unchanged.
        run("make", "-C", str(package), "TOPDIR=" + str(sdk), "clean", "V=s", cwd=sdk, env=env)
        run("make", "-C", str(package), "TOPDIR=" + str(sdk), "compile", "V=s", "-j2", cwd=sdk, env=env)
        runtime = list((sdk / "bin").rglob(f"daed-router*.{target['format']}"))
        if target['format'] == 'ipk':
            runtime = [path for path in runtime if path.name.endswith(f"_{architecture}.ipk")]
        if len(runtime) != 1:
            raise ValueError(f"Expected one runtime for {architecture}, found {runtime}")
        verify_package(runtime[0], architecture, "daed-router", target["cpu"], sdk, args.version)
        name = f"daed-router-{args.version}-{architecture}.{target['format']}"
        output = args.output / name
        shutil.copy2(runtime[0], output)
        records.append([target["format"], architecture, "daed-router", name, sha256(output), args.version])
        # APK filenames do not carry an architecture. Remove only our already
        # copied package so the next iteration cannot select a stale artifact.
        for path in runtime:
            path.resolve().relative_to(sdk.resolve())
            path.unlink()
        if target["cpu"] == "x86_64":
            plugin = list((sdk / "bin").rglob(f"luci-app-daed-router*.{target['format']}"))
            if len(plugin) != 1:
                raise ValueError(f"Expected one LuCI package, found {plugin}")
            verify_package(plugin[0], "all", "luci-app-daed-router", target["cpu"], sdk, args.version)
            name = f"luci-app-daed-router-{args.version}.{target['format']}"
            output = args.output / name
            shutil.copy2(plugin[0], output)
            records.append([target["format"], "all", "luci-app-daed-router", name, sha256(output), args.version])
    (args.output / (args.target + ".tsv")).write_text("".join("\t".join(row) + "\n" for row in records))
    probe_output = args.output / ("btf-probe-" + target["cpu"])
    temporary_probe = args.output / (".btf-probe-" + args.target + ".tmp")
    shutil.copyfile(args.btf_probe, temporary_probe)
    temporary_probe.replace(probe_output)


if __name__ == "__main__":
    main()
