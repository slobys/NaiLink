"""Installer/RPC failure-path tests in a disposable OpenWrt chroot.

Runs real BusyBox ash, UCI, jshn and jsonfilter. Kernel, network, service manager
and package managers are fixtures; this is not a router or eBPF traffic test.
"""
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

PROJECT = Path(__file__).resolve().parents[2]
OPENWRT = PROJECT / "openwrt"


class RouterTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        location = os.environ.get("DAED_TEST_ROOTFS")
        if not location or os.geteuid() != 0:
            raise unittest.SkipTest("Requires root and DAED_TEST_ROOTFS pointing to the checksum-verified OpenWrt rootfs")
        cls.base = Path(location)

    def write(self, name, data, executable=False):
        path = self.root / name.lstrip("/")
        path.parent.mkdir(parents=True, exist_ok=True)
        if path.is_symlink():
            path.unlink()
        if isinstance(data, bytes):
            path.write_bytes(data)
        else:
            path.write_text(data)
        if executable:
            path.chmod(0o755)
        return path

    def script(self, name, body):
        return self.write(name, "#!/bin/sh\n" + body + "\n", True)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="daed-router-test-")
        self.root = Path(self.temp.name) / "root"
        shutil.copytree(self.base, self.root, symlinks=True)
        (self.root / "tmp").mkdir(exist_ok=True)
        self.write("/dev/null", "")
        self.write("/www/luci-static/resources/luci.js", "// fixture\n")
        self.write("/etc/openwrt_release", "DISTRIB_ID='OpenWrt'\nDISTRIB_RELEASE='test'\n")
        self.write("/tmp/machine", "x86_64\n")
        self.write("/tmp/kernel", "6.6.73\n")
        self.write("/tmp/arch", "x86_64\n")
        self.write("/tmp/services", "{}\n")
        self.write("/sys/kernel/btf/vmlinux", "fixture only\n")
        options = "BPF BPF_SYSCALL BPF_JIT CGROUPS KPROBES NET_INGRESS NET_EGRESS NET_SCH_INGRESS NET_CLS_BPF NET_CLS_ACT BPF_STREAM_PARSER KPROBE_EVENTS BPF_EVENTS"
        self.write("/proc/config.gz", gzip.compress("".join("CONFIG_" + option + "=y\n" for option in options.split()).encode()))
        self.write("/etc/config/daed", (OPENWRT / "files/daed.config").read_bytes())
        self.script("/bin/uname", 'if [ "$1" = -m ]; then cat /tmp/machine; else cat /tmp/kernel; fi')
        self.script("/bin/ubus", '''case "$2" in
network.interface.*) echo '{"ipv4-address":[{"address":"192.168.8.1"}]}' ;;
service) cat /tmp/services ;;
*) exit 1 ;;
esac''')
        self.script("/bin/pidof", '[ -e /tmp/daemon-running ]')
        self.script("/bin/uclient-fetch", '''[ ! -e /tmp/http-fail ] || exit 1
case "$*" in *'/health/ready'*)
 if [ -e /tmp/readiness ]; then cat /tmp/readiness; else echo '{"ready":true,"phase":"inactive"}'; fi ;;
esac''')
        self.script("/etc/init.d/rpcd", 'echo "rpcd $*" >> /tmp/service-calls')
        self.script("/fixtures/daed-init", '''echo "$*" >> /tmp/service-calls
case "$1" in
start|restart)
 if [ "$(uci -q get daed.main.enabled)" = 1 ]; then
  touch /tmp/daemon-running
  echo '{"daed":{"instances":{"main":{"running":true}}}}' > /tmp/services
 fi ;;
stop) rm -f /tmp/daemon-running; echo '{}' > /tmp/services ;;
enabled) [ -f /tmp/autostart ] ;;
enable) touch /tmp/autostart ;;
disable) rm -f /tmp/autostart ;;
esac''')
        self.write("/fixtures/check", (OPENWRT / "files/daed-check").read_bytes(), True)
        self.script("/fixtures/btf-probe", '''[ ! -e /tmp/btf-bad ] || { echo "invalid BTF fixture" >&2; exit 1; }
case " $* " in *' --kernel '*)
 echo "$*" >> /tmp/kernel-probes
 [ ! -e /tmp/kernel-bad ] || { echo 'kernel callback unsupported' >&2; exit 1; } ;;
esac''')
        self.write("/installer/install.sh", (OPENWRT / "install.sh").read_bytes(), True)
        self.write("/fixtures/nailink", (OPENWRT / "files/nailink").read_bytes(), True)
        self.script("/fixtures/install", '''[ ! -e /tmp/pm-fail ] || exit 17
mkdir -p /usr/libexec/daed /usr/share/daed-router /etc/daed
mkdir -p /www/cgi-bin
cp /fixtures/btf-probe /usr/libexec/daed/luci-proxy
cp /fixtures/btf-probe /www/cgi-bin/daed-router
cp /fixtures/check /usr/libexec/daed/check
cp /fixtures/btf-probe /usr/libexec/daed/btf-probe
cp /installer/install.sh /usr/sbin/daed-router-setup
cp /fixtures/nailink /usr/sbin/nailink
cp /fixtures/daed-init /etc/init.d/daed
echo '#!/bin/sh' > /usr/bin/daed
chmod +x /usr/bin/daed
echo 0.1.0-1 > /usr/share/daed-router/version
touch /tmp/installed-daed-router /tmp/installed-luci-app-daed-router''')
        with open(self.root / "fixtures/install", "a") as stream:
            stream.write('''
if [ ! -e /tmp/no-geodata ]; then
 mkdir -p /usr/share/daed /usr/share/v2ray
 for asset in geosite.dat geoip.dat; do
  echo 'package data fixture; parsing tested separately' > "/usr/share/v2ray/$asset"
  ln -sf "../v2ray/$asset" "/usr/share/daed/$asset"
 done
fi
''')
        self.script("/bin/opkg", '''echo "$*" >> /tmp/pm-calls
case "$1" in
print-architecture) echo 'arch all 1'; printf 'arch %s 10\\n' "$(cat /tmp/arch)" ;;
status) [ -f "/tmp/installed-$2" ] && echo 'Status: install ok installed' ;;
list-installed) for p in /tmp/installed-*; do [ ! -f "$p" ] || echo "${p##*/installed-} - 1"; done ;;
remove)
 shift
 for p in "$@"; do
  case "$p" in
   daed) [ ! -f /tmp/installed-luci-app-daed ] || exit 19 ;;
   daed-router) [ ! -f /tmp/installed-luci-app-daed-router ] || exit 19 ;;
  esac
  /fixtures/remove "$p" || exit "$?"
 done ;;
update) [ ! -e /tmp/feed-fail ] ;;
install) /fixtures/install ;;
*) exit 1 ;;
esac''')
        self.script("/fixtures/remove", '''[ ! -e /tmp/remove-fail ] || exit 17
for package in "$@"; do rm -f "/tmp/installed-$package"; done''')
        # The checked-in default config would be treated as an unmanaged install.
        (self.root / "etc/config/daed").unlink()
        self.write("/fixtures/daed.config", (OPENWRT / "files/daed.config").read_bytes())
        with open(self.root / "fixtures/install", "a") as stream:
            stream.write('\n[ -e /etc/config/daed ] || cp /fixtures/daed.config /etc/config/daed\n')
        self.assets()

    def tearDown(self):
        self.temp.cleanup()

    def assets(self, fmt="ipk", arch="x86_64", version="0.1.0"):
        records = []
        cpu = next((cpu for prefix, cpu in (("aarch64", "arm64"), ("arm_", "armv7"), ("mipsel_", "mipsle"), ("mips_", "mips")) if arch.startswith(prefix)), "x86_64")
        for package, architecture, filename, content in [
            ("daed-router", arch, "runtime." + fmt, b"package manager fixture, not a real runtime"),
            ("luci-app-daed-router", "all", "luci." + fmt, b"LuCI package fixture"),
            ("check", "all", "daed-check", (OPENWRT / "files/daed-check").read_bytes()),
            ("btf-probe", cpu, "btf-probe-" + cpu, (self.root / "fixtures/btf-probe").read_bytes()),
        ]:
            self.write("/assets/" + filename, content)
            records.append("\t".join(["support" if package in ("check", "btf-probe") else fmt, architecture, package, filename, hashlib.sha256(content).hexdigest(), version]))
        self.write("/assets/manifest.tsv", "# daed-openwrt-manifest-v1\n" + "\n".join(records) + "\n")

    def run_router(self, *args, input=None):
        return subprocess.run(["chroot", str(self.root), *args], input=input, capture_output=True, text=True, env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "DAED_BTF_PROBE": "/fixtures/btf-probe"}, timeout=60)

    def install(self, *args):
        return self.run_router("/bin/sh", "/installer/install.sh", "--asset-dir", "/assets", *args)

    def test_concurrent_installer_and_rpc_leave_running_service_untouched(self):
        self.managed()
        self.write("/usr/libexec/rpcd/daed-router", (OPENWRT / "files/rpcd-daed").read_bytes(), True)
        holder = subprocess.Popen(["chroot", str(self.root), "/bin/sh", "-c", "mkdir -p /var/lock; exec 9>/var/lock/daed-install.lock; flock -n 9 || exit 1; echo ready; read -r wait"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
        try:
            self.assertEqual(holder.stdout.readline().strip(), "ready")
            self.write("/tmp/pm-calls", "")
            before = (self.root / "tmp/pm-calls").read_bytes()
            for action in ("--install", "--uninstall", "--reinstall"):
                result = self.install(action)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn("Another NaiLink", result.stderr)
            rpc = self.run_router("/usr/libexec/rpcd/daed-router", "call", "action", input='{"action":"stop"}')
            self.assertFalse(json.loads(rpc.stdout)["success"])
            self.assertTrue((self.root / "tmp/daemon-running").exists())
            self.assertEqual((self.root / "tmp/pm-calls").read_bytes(), before)
        finally:
            holder.kill()
            holder.communicate(timeout=3)
        # The persistent lock file is harmless after an interrupted installer.
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)

    def check(self, *args):
        return self.run_router("/bin/sh", "/fixtures/check", *args)

    def use_apk(self, machine="aarch64", architecture="aarch64_cortex-a53"):
        self.write("/tmp/machine", machine + "\n")
        self.write("/tmp/arch", architecture + "\n")
        self.script("/bin/apk", '''echo "$*" >> /tmp/pm-calls
case "$1" in
--version) if [ -e /tmp/apk-version ]; then cat /tmp/apk-version; else echo 'apk-tools 3.0.5'; fi ;;
--print-arch) cat /tmp/arch ;;
info)
 if [ "$#" -gt 1 ]; then [ -f "/tmp/installed-$3" ]; else
  for p in /tmp/installed-*; do [ ! -f "$p" ] || echo "${p##*/installed-}"; done
 fi ;;
update) [ ! -e /tmp/feed-fail ] ;;
add)
 case "$2" in
 --simulate) [ ! -e /tmp/preflight-fail ] ;;
 --no-network)
  case "$*" in
  *--simulate*) [ ! -e /tmp/preflight-fail ] && [ ! -e /tmp/offline-preflight-fail ] ;;
  *.apk*) /fixtures/install ;;
  *) exit 0 ;;
  esac ;;
 *) /fixtures/install ;;
 esac ;;
del) shift; /fixtures/remove "$@" ;;
*) exit 1 ;;
esac''')
        self.assets("apk", architecture)

    def managed(self, running=True):
        self.run_router("/fixtures/install")
        self.run_router("/sbin/uci", "set", "daed.main.enabled=1")
        self.run_router("/sbin/uci", "commit", "daed")
        if running:
            self.run_router("/etc/init.d/daed", "start")

    def test_all_firmware_ids(self):
        for firmware in ("OpenWrt", "ImmortalWrt", "iStoreOS"):
            self.write("/etc/openwrt_release", "DISTRIB_ID='" + firmware + "'\nDISTRIB_RELEASE='test'\n")
            result = self.check("--check")
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            self.assertEqual(json.loads(result.stdout)["firmware"], firmware)

    def test_old_kernel_and_missing_btf_are_rejected(self):
        self.write("/tmp/kernel", "5.15.100\n")
        self.assertNotEqual(self.install().returncode, 0)
        self.write("/tmp/kernel", "6.6.73\n")
        (self.root / "sys/kernel/btf/vmlinux").unlink()
        result = self.install("--dry-run")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("update", (self.root / "tmp/pm-calls").read_text())

    def test_missing_btf_can_be_repaired_only_from_matching_firmware_package(self):
        for fmt in ("ipk", "apk"):
            with self.subTest(format=fmt):
                if fmt == "apk": self.use_apk()
                (self.root / "sys/kernel/btf/vmlinux").unlink(missing_ok=True)
                pm = "/bin/apk" if fmt == "apk" else "/bin/opkg"
                original = (self.root / pm.lstrip("/")).read_text()
                # Native manager stand-in: real ash/jshn/installer exercise the
                # exact-version selection; it is not a real kernel package.
                self.write(pm, original.replace("#!/bin/sh\n", """#!/bin/sh
case "$*" in
"search -x vmlinux-btf") echo "vmlinux-btf-6.6.73-r6 x86_64"; exit 0 ;;
"info vmlinux-btf") echo "Version: 6.6.73-6"; exit 0 ;;
"install vmlinux-btf"|"add vmlinux-btf=6.6.73-r6")
 echo "BTF repaired" >> /tmp/pm-calls
 mkdir -p /usr/lib/debug/boot
 echo fixture > /usr/lib/debug/boot/vmlinux-6.6.73
 exit 0 ;;
esac
""", 1), True)
                result = self.install()
                self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
                self.assertIn("BTF repaired", (self.root / "tmp/pm-calls").read_text())
                self.assertTrue(json.loads(self.check("--check").stdout)["compatible"])
                # Restore only fixture-owned state between managers.
                self.write(pm, original, True)
                (self.root / "usr/lib/debug/boot/vmlinux-6.6.73").unlink()

    def test_missing_btf_mismatch_and_readonly_modes_preserve_service(self):
        (self.root / "sys/kernel/btf/vmlinux").unlink()
        original = (self.root / "bin/opkg").read_text()
        self.write("/bin/opkg", original.replace("#!/bin/sh\n", """#!/bin/sh
if [ "$*" = "info vmlinux-btf" ]; then echo "Version: 6.6.72-6"; exit 0; fi
""", 1), True)
        self.managed()
        self.write("/etc/daed/wing.db", "preserved")
        for args in (("--check",), ("--dry-run",), ()):
            self.write("/tmp/pm-calls", "")
            result = self.install(*args)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("BTF", result.stdout + result.stderr)
            self.assertEqual((self.root / "etc/daed/wing.db").read_text(), "preserved")
            calls = (self.root / "tmp/pm-calls").read_text()
            self.assertNotIn("install vmlinux-btf", calls)
            self.assertNotIn("stop", (self.root / "tmp/service-calls").read_text() if (self.root / "tmp/service-calls").exists() else "")
            if args: self.assertNotIn("update", calls)

    def test_missing_btf_ambiguous_versions_do_not_install_any_package(self):
        (self.root / "sys/kernel/btf/vmlinux").unlink()
        original = (self.root / "bin/opkg").read_text()
        self.write("/bin/opkg", original.replace("#!/bin/sh\n", """#!/bin/sh
if [ "$*" = "info vmlinux-btf" ]; then
 printf 'Version: 6.6.73-6\\nVersion: 6.6.73-7\\n'
 exit 0
fi
""", 1), True)
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("install vmlinux-btf", (self.root / "tmp/pm-calls").read_text())
        self.assertFalse((self.root / "tmp/installed-daed-router").exists())

    def test_missing_kernel_config_is_an_explicit_warning(self):
        (self.root / "proc/config.gz").unlink()
        data = json.loads(self.check().stdout)
        self.assertTrue(data["compatible"])
        self.assertIn("warning", [row["status"] for row in data["checks"]])

    def test_missing_required_kernel_option_fails(self):
        self.write("/proc/config.gz", gzip.compress(b"CONFIG_BPF=y\n"))
        self.assertNotEqual(self.check("--check").returncode, 0)

    def test_unmanaged_installation_is_preserved(self):
        path = self.write("/usr/bin/daed", "existing binary")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(path.read_text(), "existing binary")
        self.assertIn("unmanaged", result.stderr)

    def test_bad_checksum_stops_before_package_changes(self):
        self.write("/assets/runtime.ipk", "tampered")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Checksum mismatch", result.stderr)
        self.assertNotIn("update", (self.root / "tmp/pm-calls").read_text())

    def test_architecture_mismatch_has_no_forced_fallback(self):
        self.write("/tmp/arch", "aarch64_cortex-a53\n")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("No ipk runtime", result.stderr)

    def test_duplicate_and_traversal_manifest_records_fail(self):
        manifest = self.root / "assets/manifest.tsv"
        original = manifest.read_text()
        manifest.write_text(original + original.splitlines()[1] + "\n")
        self.assertNotEqual(self.install().returncode, 0)
        manifest.write_text(original.replace("runtime.ipk", "../runtime.ipk"))
        self.assertNotEqual(self.install().returncode, 0)

    def test_mismatched_versions_fail(self):
        manifest = self.root / "assets/manifest.tsv"
        manifest.write_text(manifest.read_text().replace("luci.ipk\t", "luci.ipk\t").replace("\t0.1.0\n", "\t0.2.0\n", 1))
        self.assertNotEqual(self.install().returncode, 0)

    def test_dry_run_never_installs_or_starts(self):
        result = self.install("--dry-run")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertNotIn("update", (self.root / "tmp/pm-calls").read_text())
        self.assertFalse((self.root / "tmp/daemon-running").exists())

    def test_source_check_works_before_a_release_and_cleans_staging(self):
        self.write("/installer/files/daed-check", (OPENWRT / "files/daed-check").read_bytes())
        result = self.install("--check")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads(result.stdout)["compatible"])
        self.assertNotIn("update", (self.root / "tmp/pm-calls").read_text())
        self.assertFalse(list((self.root / "tmp").glob("daed-install.*")))

    def test_legacy_listen_settings_cannot_expose_management_port(self):
        self.managed(False)
        self.run_router("/sbin/uci", "set", "daed.main.listen_host=0.0.0.0")
        self.run_router("/sbin/uci", "commit", "daed")
        self.assertEqual(self.check("--listen").stdout.strip(), "127.0.0.1:2023")

    def test_installer_ipk_and_apk_paths(self):
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertTrue((self.root / "tmp/daemon-running").exists())
        self.assertTrue((self.root / "tmp/autostart").exists())
        self.use_apk()
        result = self.install("--dry-run")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertIn("aarch64 / apk / aarch64_cortex-a53", result.stdout)

    def test_old_apk_format_is_rejected_before_changes(self):
        self.use_apk()
        self.write("/tmp/apk-version", "apk-tools 2.14.4\n")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("apk-tools 3.x", result.stderr)
        self.assertNotIn("update", (self.root / "tmp/pm-calls").read_text())

    def test_apk_database_architectures_override_compile_time_default(self):
        self.use_apk(architecture="aarch64_generic")
        self.write("/tmp/arch", "aarch64\n")
        self.write("/etc/apk/arch", "  aarch64_generic  \n\n")
        result = self.install("--dry-run")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertIn("aarch64 / apk / aarch64_generic", result.stdout)
        self.assertNotIn("--print-arch", (self.root / "tmp/pm-calls").read_text())

    def test_apk_empty_arch_file_uses_default_but_unknown_arch_never_does(self):
        self.use_apk()
        self.write("/etc/apk/arch", "\n")
        self.assertEqual(self.install("--dry-run").returncode, 0)
        self.write("/etc/apk/arch", "aarch64_unknown\n")
        result = self.install("--dry-run")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("No apk runtime", result.stderr)

    def test_apk_accepts_supported_secondary_architecture_in_order(self):
        self.use_apk()
        self.write("/etc/apk/arch", "aarch64_unknown\naarch64_cortex-a53\n")
        result = self.install("--dry-run")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertIn("aarch64 / apk / aarch64_cortex-a53", result.stdout)

    def test_preflight_failure_preserves_old_service_packages_and_data(self):
        self.use_apk()
        self.legacy()
        self.write("/tmp/preflight-fail", "1")
        result = self.install("--reinstall")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Package preflight failed", result.stderr)
        self.assertTrue((self.root / "tmp/daemon-running").exists())
        self.assertTrue((self.root / "tmp/installed-daed").exists())
        self.assertEqual((self.root / "etc/daed/wing.db-wal").read_text(), "old WAL")
        self.assertFalse((self.root / "root/daed-backups").exists())

    def test_failed_dependency_dry_run_does_not_refresh_or_stop(self):
        self.use_apk()
        self.legacy()
        self.write("/tmp/preflight-fail", "1")
        result = self.install("--reinstall", "--dry-run")
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((self.root / "tmp/daemon-running").exists())
        calls = (self.root / "tmp/pm-calls").read_text()
        self.assertNotIn("update", calls)
        self.assertNotIn("del ", calls)

    def test_upgrade_stops_before_consistent_backup_and_preserves_boot_choice(self):
        self.managed()
        self.write("/etc/daed/wing.db", "database fixture")
        self.write("/etc/daed/wing.db-wal", "WAL fixture")
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        backups = list((self.root / "root/daed-backups").glob("*.tar.gz"))
        self.assertEqual(len(backups), 1)
        with tarfile.open(backups[0]) as archive:
            self.assertEqual(archive.extractfile("etc/daed/wing.db-wal").read(), b"WAL fixture")
        self.assertEqual(backups[0].stat().st_mode & 0o777, 0o600)
        self.assertFalse((self.root / "tmp/autostart").exists())

    def test_package_failure_leaves_service_stopped_and_backup_available(self):
        self.managed()
        self.write("/tmp/pm-fail", "1")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / "tmp/daemon-running").exists())
        self.assertTrue(list((self.root / "root/daed-backups").glob("*.tar.gz")))

    def test_upgrade_waits_for_proxy_readiness_after_management_http(self):
        self.managed()
        self.write("/etc/daed/wing.db", "preserve my nodes and group policy")
        self.write("/tmp/readiness", '{"ready":false,"phase":"dns_starting"}')
        self.script("/bin/sleep", '''echo waiting >> /tmp/readiness-waits
echo '{"ready":true,"phase":"ready"}' > /tmp/readiness''')
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("缓存路由恢复已就绪", result.stdout)
        self.assertTrue((self.root / "tmp/readiness-waits").exists())
        self.assertEqual((self.root / "etc/daed/wing.db").read_text(), "preserve my nodes and group policy")
        self.assertTrue((self.root / "tmp/daemon-running").exists())

    def test_upgrade_dns_restore_failure_keeps_new_service_and_existing_data(self):
        self.managed()
        self.write("/etc/daed/wing.db", "unchanged database")
        config = (self.root / "etc/config/daed").read_bytes()
        self.write("/tmp/readiness", '{"ready":false,"phase":"dns_route_restore_failed"}')
        self.script("/bin/sleep", "exit 0")
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("dns_route_restore_failed", result.stderr)
        self.assertIn("服务继续运行", result.stderr)
        self.assertTrue((self.root / "tmp/daemon-running").exists())
        self.assertEqual((self.root / "etc/config/daed").read_bytes(), config)
        self.assertEqual((self.root / "etc/daed/wing.db").read_text(), "unchanged database")

    def test_rpc_rejects_shell_injection_and_handles_no_newline(self):
        self.managed()
        self.write("/usr/libexec/rpcd/daed-router", (OPENWRT / "files/rpcd-daed").read_bytes(), True)
        result = self.run_router("/usr/libexec/rpcd/daed-router", "call", "action", input=json.dumps({"action": "start; touch /tmp/pwned"}))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(json.loads(result.stdout)["success"])
        self.assertFalse((self.root / "tmp/pwned").exists())
        result = self.run_router("/usr/libexec/rpcd/daed-router", "call", "action", input='{"action":"stop"}')
        self.assertTrue(json.loads(result.stdout)["success"])
        self.assertFalse((self.root / "tmp/daemon-running").exists())

    def test_sensitive_log_links_are_masked(self):
        self.managed(False)
        self.write("/usr/libexec/rpcd/daed-router", (OPENWRT / "files/rpcd-daed").read_bytes(), True)
        self.write("/var/log/daed/daed.log", "https://user:secret@host/sub?token=secret password=abc ss://private-node\n")
        result = self.run_router("/usr/libexec/rpcd/daed-router", "call", "logs")
        logs = json.loads(result.stdout)["logs"]
        for secret in ("secret", "abc", "private-node"):
            self.assertNotIn(secret, logs)

    def test_rpc_log_read_is_bounded_for_a_giant_single_line(self):
        self.managed(False)
        self.write("/usr/libexec/rpcd/daed-router", (OPENWRT / "files/rpcd-daed").read_bytes(), True)
        self.write("/var/log/daed/daed.log", "x" * (1024 * 1024))
        result = self.run_router("/usr/libexec/rpcd/daed-router", "call", "logs")
        self.assertLessEqual(len(json.loads(result.stdout)["logs"]), 32768)

    def legacy(self):
        self.write("/tmp/installed-daed", "1")
        self.write("/tmp/installed-luci-app-daed", "1")
        self.write("/tmp/installed-luci-i18n-daed-zh-cn", "1")
        self.write("/tmp/installed-vmlinux-btf", "1")
        self.write("/etc/init.d/daed", (self.root / "fixtures/daed-init").read_bytes(), True)
        self.write("/usr/bin/daed", "old binary fixture", True)
        self.write("/etc/config/daed", "config daed 'config'\n option enabled '1'\n")
        self.write("/etc/daed/wing.db", "old database")
        self.write("/etc/daed/wing.db-wal", "old WAL")
        self.write("/tmp/daemon-running", "1")
        self.write("/tmp/autostart", "1")

    def test_external_btf_symlink_matches_current_kernel_and_parser_is_required(self):
        (self.root / "sys/kernel/btf/vmlinux").unlink()
        self.write("/tmp/kernel", "6.12.94\n")
        path = self.write("/usr/lib/debug/boot/vmlinux", "external parser fixture")
        (path.parent / "vmlinux-6.12.94").symlink_to("vmlinux")
        result = self.check("--check")
        self.assertEqual(result.returncode, 0, result.stderr)
        btf_check = next(row for row in json.loads(result.stdout)["checks"] if row["name"] == "btf")
        self.assertIn("/usr/lib/debug/boot/vmlinux-6.12.94", btf_check["message"])
        self.write("/tmp/btf-bad", "1")
        self.assertNotEqual(self.check("--check").returncode, 0)
        (self.root / "tmp/btf-bad").unlink()
        self.write("/tmp/kernel", "6.12.95\n")
        self.assertNotEqual(self.check("--check").returncode, 0)

    def test_btf_parser_hash_is_checked_before_execution(self):
        path = self.root / "assets/btf-probe-x86_64"
        path.write_text('#!/bin/sh\ntouch /tmp/executed-bad-parser\n')
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Checksum mismatch", result.stderr)
        self.assertFalse((self.root / "tmp/executed-bad-parser").exists())

    def test_uninstall_ipk_and_apk_back_up_wal_and_retain_btf(self):
        for fmt in ("ipk", "apk"):
            if fmt == "apk": self.use_apk()
            self.legacy()
            (self.root / "sys/kernel/btf/vmlinux").unlink(missing_ok=True)
            result = self.install("--uninstall")
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            self.assertFalse((self.root / "etc/daed").exists())
            self.assertFalse((self.root / "etc/config/daed").exists())
            self.assertFalse((self.root / "tmp/installed-daed").exists())
            self.assertFalse((self.root / "tmp/installed-luci-i18n-daed-zh-cn").exists())
            self.assertTrue((self.root / "tmp/installed-vmlinux-btf").exists())
            self.assertFalse((self.root / "tmp/daemon-running").exists())
            backups = sorted((self.root / "root/daed-backups").glob("*.tar.gz"))
            with tarfile.open(backups[-1]) as archive:
                self.assertEqual(archive.extractfile("etc/daed/wing.db-wal").read(), b"old WAL")
                self.assertEqual(archive.extractfile("usr/bin/daed").read(), b"old binary fixture")
            self.assertEqual(backups[-1].stat().st_mode & 0o777, 0o600)

    def test_reinstall_verifies_assets_before_removing_legacy_and_starts_fresh(self):
        self.legacy()
        original = (self.root / "assets/runtime.ipk").read_bytes()
        self.write("/assets/runtime.ipk", "bad download")
        result = self.install("--reinstall")
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((self.root / "tmp/daemon-running").exists())
        self.assertEqual((self.root / "etc/daed/wing.db").read_text(), "old database")
        self.write("/assets/runtime.ipk", original)
        result = self.install("--reinstall")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertTrue((self.root / "tmp/installed-daed-router").exists())
        self.assertFalse((self.root / "etc/daed/wing.db").exists())
        self.assertEqual(self.run_router("uci", "-q", "get", "daed.main.enabled").stdout.strip(), "1")

    def test_uninstall_dry_run_and_repeated_uninstall_are_safe(self):
        self.legacy()
        result = self.install("--uninstall", "--dry-run")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.root / "tmp/daemon-running").exists())
        self.assertFalse((self.root / "root/daed-backups").exists())
        self.assertEqual(self.install("--uninstall").returncode, 0)
        count = len(list((self.root / "root/daed-backups").glob("*.tar.gz")))
        self.assertEqual(self.install("--uninstall").returncode, 0)
        self.assertEqual(len(list((self.root / "root/daed-backups").glob("*.tar.gz"))), count)

    def test_uninstall_failure_retains_data_and_symlinked_data_is_refused(self):
        self.legacy()
        self.write("/tmp/remove-fail", "1")
        result = self.install("--uninstall")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.root / "etc/daed/wing.db").read_text(), "old database")
        self.assertTrue(list((self.root / "root/daed-backups").glob("*.tar.gz")))
        shutil.move(self.root / "etc/daed", self.root / "etc/daed-outside")
        (self.root / "etc/daed").symlink_to("daed-outside")
        result = self.install("--uninstall")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("symlinked", result.stderr)
        self.assertTrue((self.root / "etc/daed-outside/wing.db").exists())

    def test_failed_backup_never_removes_packages_or_data(self):
        self.legacy()
        self.script("/bin/tar", "exit 17")
        result = self.install("--uninstall")
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((self.root / "tmp/installed-daed").exists())
        self.assertEqual((self.root / "etc/daed/wing.db").read_text(), "old database")

    def test_installed_setup_can_uninstall_itself(self):
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        result = self.run_router("/bin/sh", "/usr/sbin/daed-router-setup", "--uninstall")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertFalse((self.root / "usr/sbin/daed-router-setup").exists())
        self.assertFalse((self.root / "tmp/installed-daed-router").exists())

    def test_luci_postinst_clears_legacy_and_ucode_caches(self):
        recipe = (OPENWRT / "Makefile").read_text()
        script = recipe.split("define Package/luci-app-daed-router/postinst\n", 1)[1].split("\nendef", 1)[0].replace("$$", "$")
        self.write("/postinst", script)
        for name in ("luci-indexcache", "luci-indexcache.1234abcd.json", "luci-modulecache/old-view"):
            self.write("/tmp/" + name, "cached old page")
        self.write("/tmp/unrelated.json", "keep")
        result = self.run_router("/bin/sh", "/postinst")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(list((self.root / "tmp").glob("luci-indexcache*")))
        self.assertFalse((self.root / "tmp/luci-modulecache").exists())
        self.assertEqual((self.root / "tmp/unrelated.json").read_text(), "keep")


if __name__ == "__main__":
    unittest.main()
