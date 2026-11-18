"""Serve real LuCI/rpcd and old/new API-only Daed in a disposable rootfs.

Requires root. Board/procd metadata are fixtures; no eBPF or LAN traffic test.
The browser test communicates through JSON files in --control.
"""
import argparse
import base64
import hashlib
import http.server
import json
import os
import re
from pathlib import Path
import shutil
import sqlite3
import stat
import subprocess
import threading
import time
from urllib.parse import quote
import urllib.request

from socks_fixture import serve_socks


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("rootfs", "old-runtime", "runtime", "proxy", "workspace", "control"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--old-menu", type=Path, help="Use a real previous-release LuCI menu until upgrade")
    parser.add_argument("--old-proxy", type=Path, help="Use the previous gateway until upgrade for cache/performance comparisons")
    parser.add_argument("--maintenance-fixture", action="store_true", help="Use private trusted HTTPS/procd fixtures for online-update tests")
    parser.add_argument("--geodata-probe", type=Path, help="Test-only geodata fixture generator")
    parser.add_argument("--singbox-package", type=Path, help="Pinned official static package; run API-only daemon inside the disposable rootfs")
    parser.add_argument("--mihomo-package",type=Path)
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error("Run as root with an isolated, checksum-verified OpenWrt rootfs")
    project = Path(__file__).resolve().parents[2]
    if args.maintenance_fixture and not args.geodata_probe:
        parser.error("--maintenance-fixture requires --geodata-probe")
    root = args.workspace / "root"
    if args.workspace.exists():
        parser.error("Workspace already exists; select a new dedicated directory")
    args.workspace.mkdir(parents=True)
    args.control.mkdir(parents=True, exist_ok=True)
    command = args.control / "command.json"
    result = args.control / "result.json"
    command.unlink(missing_ok=True)
    result.unlink(missing_ok=True)
    shutil.copytree(args.rootfs, root, symlinks=True)
    for relative in ("tmp", "dev", "etc/daed", "www/cgi-bin", "usr/libexec/daed", "var/run/ubus"):
        (root / relative).mkdir(parents=True, exist_ok=True)
    for name, minor in (("null", 3), ("random", 8), ("urandom", 9)):
        device = root / "dev" / name
        device.unlink(missing_ok=True)
        os.mknod(device, stat.S_IFCHR | 0o666, os.makedev(1, minor))
    if args.singbox_package:
        (root / "dev/net").mkdir(exist_ok=True)
        os.mknod(root / "dev/net/tun",stat.S_IFCHR|0o600,os.makedev(10,200))
    subprocess.run([str(args.runtime), "prepare-luci", "--config", str(root / "etc/daed")], check=True)
    files = {
        "www/cgi-bin/daed-router": project / "openwrt/files/luci-cgi",
        "usr/libexec/rpcd/daed-router": project / "openwrt/files/rpcd-daed",
        "usr/libexec/daed/luci-proxy": args.old_proxy or args.proxy,
        "usr/libexec/daed/subscription-proxy": project / "openwrt/files/subscription-proxy",
        "usr/share/rpcd/acl.d/luci-app-daed-router.json": project / "openwrt/files/acl.json",
        "usr/share/luci/menu.d/luci-app-daed-router.json": project / "openwrt/files/menu.json",
    }
    for relative, source in files.items():
        destination = root / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, destination)
        destination.chmod(0o755 if relative.startswith(("www/cgi-bin", "usr/libexec")) else 0o644)
    if args.old_menu:
        shutil.copyfile(args.old_menu, root / "usr/share/luci/menu.d/luci-app-daed-router.json")
    views = root / "www/luci-static/resources/view/daed-router"
    views.mkdir(parents=True)
    for source in (project / "openwrt/htdocs").glob("*.js"):
        shutil.copy2(source, views / source.name)
    (root / "etc/config/daed").write_text("config daed 'main'\n option listen_port '12025'\n")
    checker = root / "usr/libexec/daed/check"
    checker.write_text("#!/bin/sh\necho '{\"running\":true,\"listen\":\"127.0.0.1:12025\",\"checks\":[]}'\n")
    checker.chmod(0o755)
    def runtime_version(binary):
        text = subprocess.check_output([str(binary), "--version"], text=True)
        match = re.search(r"[0-9]+\.[0-9]+\.[0-9]+", text)
        return match[0] + "-1" if match else "unknown"
    marker = root / "usr/share/daed-router/version"
    marker.parent.mkdir(parents=True, exist_ok=True)
    marker.write_text(runtime_version(args.old_runtime) + "\n")
    board = root / "usr/libexec/rpcd/system"
    board.write_text("#!/bin/sh\ncase \"$1\" in list) echo '{\"board\":{}}';; call) echo '{\"hostname\":\"OpenWrt UI test\",\"rootfs_type\":\"squashfs\",\"release\":{\"description\":\"Isolated OpenWrt 24.10.4\"}}';; esac\n")
    board.chmod(0o755)
    shadow = root / "etc/shadow"
    password_hash = subprocess.check_output(["openssl", "passwd", "-6", "-salt", "paneltest", "fixture-password"], text=True).strip()
    shadow.write_text("\n".join("root:" + password_hash + ":0:0:99999:7:::" if line.startswith("root:") else line for line in shadow.read_text().splitlines()) + "\n")
    rpcd_config = root / "etc/config/rpcd"
    rpcd_config.write_text(rpcd_config.read_text().replace("option timeout 30", "option timeout 3600"))
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=localhost", "-keyout", str(root / "etc/smoke.key"), "-out", str(root / "etc/smoke.crt"), "-days", "1"], check=True, capture_output=True)

    state = {"fail": False, "proxy_down": False, "proxy_requests": 0}

    class SubscriptionHandler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path.startswith("/requires-socks") and self.server.server_port == 18081:
                body = b'{"code":"AUTH_FAIL","error":"private-fixture-token"}'
                self.send_response(403)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                return
            if state["fail"] and self.path.startswith("/sub-a"):
                self.send_error(503)
                return
            links = ["vless://11111111-1111-4111-8111-111111111111@192.0.2.10:443?encryption=none&security=tls&type=tcp#Subscription-HK", "vless://22222222-2222-4222-8222-222222222222@192.0.2.11:443?encryption=none&security=tls&type=tcp#Subscription-JP"]
            if self.path.startswith("/sub-probes"):
                links = [f"socks5://127.0.0.1:{socks.server_address[1]}#Picker-sub-{name}" for name in ("HK", "JP")]
            if self.path.startswith("/sub-large"):
                links = [f"vless://44444444-4444-4444-8444-{index:012d}@192.0.2.12:443?encryption=none&security=tls&type=tcp#Workspace-{index:04d}-Long-node-name-for-layout-testing" for index in range(1000)]
            if self.path.startswith("/sub-layout"):
                regions = ["香港", "狮城", "台湾", "日本", "美国", "英国"]
                names = [f"{regions[index % len(regions)]}{index + 1:03d} · 中继 BGP" for index in range(72)]
                names[0] += " · " + "完整名称查看验证 LongNodeName " * 5
                links = [f"trojan://fixture-{index}@192.0.2.44:443?security=tls&sni=example.invalid#{quote(name)}" for index, name in enumerate(names)]
            if self.path.startswith("/sub-recovery"):
                label = "A" if self.path.startswith("/sub-recovery-a") else "B"
                names = [f"Recovery-{label}-{index:03d}" for index in range(66)]
                names[0] += " · " + "完整名称 LongNodeName " * 7
                links = [f"trojan://recovery-{label}-{index}@192.0.2.44:443?security=tls#{quote(name)}" for index, name in enumerate(names)]
            body = base64.b64encode("\n".join(links).encode())
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            if self.path.startswith("/sub-named"):
                self.send_header("Profile-Title", "base64:" + base64.b64encode("自动订阅名称".encode()).decode())
                self.send_header("Content-Disposition", 'attachment; filename="fallback.yaml"')
            elif self.path.startswith("/sub-filename"):
                self.send_header("Content-Disposition", "inline; filename*=UTF-8''%E6%96%87%E4%BB%B6%E5%90%8D%E7%A7%B0.yaml")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *unused):
            pass

    feed = http.server.ThreadingHTTPServer(("127.0.0.1", 18081), SubscriptionHandler)
    feed_thread = threading.Thread(target=feed.serve_forever, daemon=True)
    feed_thread.start()
    proxy_feed = http.server.ThreadingHTTPServer(("127.0.0.1", 18082), SubscriptionHandler)
    threading.Thread(target=proxy_feed.serve_forever, daemon=True).start()
    socks = serve_socks(18082, state)
    (root / "tmp/passwall-socks").write_text("127.0.0.1:" + str(socks.server_address[1]))
    lua = root / "bin/lua"
    lua.unlink(missing_ok=True)
    lua.write_text('#!/bin/sh\ncase "$2" in *GLOBAL_SOCKS_server*) cat /tmp/passwall-socks ;; *) echo 26.10.1 ;; esac\n')
    lua.chmod(0o755)
    helper = args.workspace / "discover-proxy"
    helper.write_text('#!/bin/sh\nexec chroot "' + str(root) + '" /usr/libexec/daed/subscription-proxy\n')
    helper.chmod(0o755)
    streams = [(args.workspace / name).open("w") for name in ("ubusd.log", "rpcd.log", "daemon.log", "uhttpd.log")]
    processes = []
    daemon = None

    def start_daemon(binary):
        env = dict(os.environ, DAED_SUBSCRIPTION_PROXY_HELPER=str(helper))
        command=[str(binary), "run", "--api-only", "--config", str(root / "etc/daed"), "--listen", "127.0.0.1:12025"]
        if args.singbox_package:
            installed=root/"usr/bin/fixture-daed";staging=root/"usr/bin/.fixture-daed";shutil.copy2(binary,staging);staging.replace(installed)
            command=["chroot",str(root),"/usr/bin/fixture-daed","run","--api-only","--config","/etc/daed","--listen","127.0.0.1:12025"]
            env['DAED_SUBSCRIPTION_PROXY_HELPER']='/usr/libexec/daed/subscription-proxy'
        process = subprocess.Popen(command, env=env, stdout=streams[2], stderr=subprocess.STDOUT)
        for _ in range(100):
            try:
                with urllib.request.urlopen("http://127.0.0.1:12025/", timeout=1) as response:
                    if response.status == 200:
                        return process
            except OSError:
                time.sleep(0.1)
            if process.poll() is not None:
                raise RuntimeError("API-only daemon exited")
        process.terminate()
        process.wait(timeout=10)
        raise RuntimeError("API-only daemon did not become ready")

    def snapshot():
        database = root / "etc/daed/wing.db"
        with sqlite3.connect("file:" + str(database) + "?mode=ro", uri=True) as connection:
            connection.execute("BEGIN")
            tables = [row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != 'systems'")]
            return {name: hashlib.sha256(repr(sorted(connection.execute('SELECT * FROM "' + name.replace('"', '""') + '"').fetchall(), key=repr)).encode()).hexdigest() for name in tables}

    baseline = None
    maintenance = None
    try:
        processes.append(subprocess.Popen(["chroot", str(root), "/sbin/ubusd"], stdout=streams[0], stderr=subprocess.STDOUT))
        for _ in range(50):
            if (root / "var/run/ubus/ubus.sock").exists():
                break
            time.sleep(0.05)
        if args.maintenance_fixture:
            from maintenance_fixture import MaintenanceFixture
            maintenance = MaintenanceFixture(root, project, args.geodata_probe, args.singbox_package, args.mihomo_package)
            namespace = maintenance.procd()
            namespace.maintenance_namespace = True
            processes.append(namespace)
        processes.append(subprocess.Popen(["chroot", str(root), "/sbin/rpcd"], stdout=streams[1], stderr=subprocess.STDOUT))
        daemon = start_daemon(args.old_runtime)
        processes.append(subprocess.Popen(["chroot", str(root), "/usr/sbin/uhttpd", "-f", "-p", "127.0.0.1:18080", "-s", "127.0.0.1:18443", "-C", "/etc/smoke.crt", "-K", "/etc/smoke.key", "-h", "/www", "-x", "/cgi-bin", "-u", "/ubus", "-n", "8", "-t", "60"], stdout=streams[3], stderr=subprocess.STDOUT))
        print("Native LuCI/rpcd + old API-only Daed ready on 18080 / 18443; seed data before upgrade.", flush=True)
        while True:
            if any(process.poll() is not None for process in processes) or daemon.poll() is not None:
                raise RuntimeError("A native smoke service exited")
            if command.exists():
                request = json.loads(command.read_text())
                command.unlink()
                answer = {"id": request["id"]}
                action = request["action"]
                if action == "stop":
                    result.write_text(json.dumps(answer))
                    break
                if action == "core_fault" and args.singbox_package:
                    engine=request.get("engine","sing-box");assert engine in ("sing-box","mihomo")
                    managed=root/("usr/libexec/daed/cores/"+engine);backup=managed.with_name(engine+".real")
                    state_file=root/"etc/daed/cores/state.json"
                    if state_file.exists() and json.loads(state_file.read_text()).get('current')!='dae':raise ValueError('Fault injection requires inactive optional core')
                    if request.get('fail'):
                        if backup.exists():raise ValueError('Fault already installed')
                        managed.rename(backup);managed.write_text('#!/bin/sh\ncase "$1" in '+('version|check' if engine=='sing-box' else '-v|-t')+') exec /usr/libexec/daed/cores/'+engine+'.real "$@" ;; *) exit 77 ;; esac\n');managed.chmod(0o755)
                    else:backup.replace(managed)
                elif action == "compare":
                    answer["unchanged"]=snapshot()==baseline
                elif action == "snapshot":
                    baseline = snapshot()
                    answer["tables"] = len(baseline)
                elif action == "upgrade":
                    if baseline is None:
                        raise RuntimeError("Take a seeded database snapshot before upgrading")
                    daemon.terminate()
                    daemon.wait(timeout=10)
                    shutil.copyfile(args.proxy, root / "usr/libexec/daed/luci-proxy")
                    shutil.copyfile(project / "openwrt/files/menu.json", root / "usr/share/luci/menu.d/luci-app-daed-router.json")
                    # Model the installed LuCI package's postinst invalidation.
                    # The browser session menu intentionally remains untouched.
                    for cache in (root / "tmp").glob("luci-indexcache*"):
                        if cache.is_file():
                            cache.unlink()
                    subprocess.run([str(args.runtime), "prepare-luci", "--config", str(root / "etc/daed")], check=True)
                    daemon = start_daemon(args.runtime)
                    marker.write_text(runtime_version(args.runtime) + "\n")
                    after = snapshot()
                    answer["unchanged"] = baseline == after
                    answer["changed_tables"] = sorted(name for name in set(baseline) | set(after) if baseline.get(name) != after.get(name))
                elif action == "subscription_fail":
                    state["fail"] = request["enabled"]
                elif action == "logs":
                    # Synthetic records in this disposable rootfs, consumed by
                    # the real authenticated LuCI RPC and its bounded tail.
                    logfile = root / "var/log/daed/daed.log"
                    if not logfile.resolve().is_relative_to(root.resolve()):
                        raise ValueError("Fixture log path escapes disposable rootfs")
                    logfile.parent.mkdir(parents=True, exist_ok=True)
                    logfile.write_text(request["text"], encoding="utf-8")
                elif action == "revoke_session":
                    session = request.get("session", "")
                    if len(session) != 32 or any(c not in "0123456789abcdef" for c in session):
                        raise ValueError("Invalid fixture session")
                    subprocess.run(["chroot", str(root), "ubus", "call", "session", "destroy", json.dumps({"ubus_rpc_session": session})], check=True)
                elif action == "proxy_state":
                    if "down" in request:
                        state["proxy_down"] = request["down"]
                    answer["requests"] = state["proxy_requests"]
                    answer["address"] = "127.0.0.1:" + str(socks.server_address[1])
                elif action == "maintenance" and maintenance:
                    if "next" in request:
                        if not re.fullmatch(r"0\.6\.(99|100|101)",request['next']): raise ValueError('Invalid future fixture version')
                        maintenance.state['next'] = request['next']
                    if "fail" in request:
                        if request["fail"] not in ("", "download", "checksum", "invalid_geodata", "install", "core_checksum", "metadata"): raise ValueError("Invalid fixture failure mode")
                        maintenance.state["fail"] = request["fail"]
                    if request.get("clear_geodata_check"):
                        (root / "var/run/daed-maintenance/geodata-latest.json").unlink(missing_ok=True)
                    if request.get("bump"):
                        maintenance.state["geo"] = str(int(maintenance.state["geo"]) + 1)
                    if "hold" in request:
                        hold = root / "tmp/fixture-update-hold"
                        if request['hold']: hold.touch()
                        else: hold.unlink(missing_ok=True)
                    if "custom" in request:
                        custom = root / "etc/daed/geoip.dat"
                        if request["custom"]:
                            if custom.exists(): raise ValueError("Custom fixture already exists")
                            custom.write_text("native maintenance custom fixture")
                        elif custom.exists() and custom.read_text() == "native maintenance custom fixture":
                            custom.unlink()
                    pointer = root / "etc/daed/geodata/current"
                    answer["current"] = os.readlink(pointer) if pointer.is_symlink() else ""
                    answer["requests"] = maintenance.state["requests"]
                elif action == "geodata_crash" and maintenance:
                    directory = root/'etc/daed/geodata'
                    pointer = directory/'current'
                    previous = os.readlink(pointer)
                    candidate = 'data-999999999'
                    shutil.copytree(directory/previous,directory/candidate)
                    receipt = directory/'activation.json'
                    receipt.write_text(json.dumps({'target':candidate,'previous':previous,'state':'applying'}))
                    receipt.chmod(0o600)
                    pointer.unlink(); pointer.symlink_to(candidate)
                    job_file = root/'var/run/daed-maintenance/job.json'
                    job = json.loads(job_file.read_text()); job.update(id='f'*24,state='running',code='',batch=candidate)
                    job_file.write_text(json.dumps(job)); job_file.chmod(0o600)
                    # Crash after pointer activation but before readiness/ack.
                    daemon.kill(); daemon.wait(timeout=5); daemon = start_daemon(args.runtime)
                    answer.update(current=os.readlink(pointer),previous=previous,receipt=json.loads(receipt.read_text()),unchanged=snapshot()==baseline)
                elif action == 'stop_maintenance' and maintenance:
                    subprocess.run(['chroot',str(root),'/usr/libexec/daed/nailink-maintenance','stop'],check=True,timeout=10)
                result.write_text(json.dumps(answer))
            time.sleep(0.1)
    finally:
        if daemon is not None:
            daemon.terminate()
            daemon.wait(timeout=10)
        for process in reversed(processes):
            if getattr(process, "maintenance_namespace", False): process.kill()
            else: process.terminate()
        for process in reversed(processes):
            process.wait(timeout=10)
        feed.shutdown()
        feed_thread.join()
        feed.server_close()
        socks.shutdown()
        socks.server_close()
        proxy_feed.shutdown()
        proxy_feed.server_close()
        for stream in streams:
            stream.close()
        if maintenance:
            maintenance.close()


if __name__ == "__main__":
    main()
