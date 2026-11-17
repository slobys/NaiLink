"""Private HTTPS GitHub fixture; trust/hosts overrides exist only in the chroot."""
import hashlib
import http.server
import json
from pathlib import Path
import ssl
import subprocess
import threading
from urllib.parse import urlsplit


class MaintenanceFixture:
    def __init__(self, root, project, probe, singbox_package=None, mihomo_package=None):
        self.root = root
        self.state = {"next": "0.6.99", "geo": "20261025", "fail": "", "requests": []}
        fixture = root / "tmp/maintenance-https"
        fixture.mkdir(parents=True)
        subprocess.run([str(probe), "--fixtures", str(fixture)], check=True, capture_output=True)
        cert, key = fixture / "certificate.pem", fixture / "key.pem"
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=NaiLink isolated fixture", "-addext", "subjectAltName=DNS:api.github.com,DNS:github.com", "-keyout", str(key), "-out", str(cert), "-days", "1"], check=True, capture_output=True)
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                path = urlsplit(self.path).path
                outer.state["requests"].append(path)
                if outer.state["fail"] == "metadata" and path == "/repos/Loyalsoldier/v2ray-rules-dat/releases/latest":
                    self.send_error(503)
                    return
                if outer.state["fail"] == "download":
                    self.send_error(503)
                    return
                if singbox_package and path == "/slobys/NaiLink/releases/download/openwrt-v0.6.35/nailink-sing-box-1.14.3-nailink.1-x86_64.tar.gz":
                    data=singbox_package.read_bytes()
                    self.send_response(200);self.send_header("Content-Length",str(len(data)));self.end_headers();self.wfile.write(data);return
                if mihomo_package and path == "/slobys/NaiLink/releases/download/nailink-cores-20261117/nailink-mihomo-1.19.34-nailink.1-x86_64.tar.gz":
                    data=mihomo_package.read_bytes()
                    if outer.state['fail']=='core_checksum':data=data[:-16]+b'corrupted-footer'
                    self.send_response(200);self.send_header("Content-Length",str(len(data)));self.end_headers();self.wfile.write(data);return
                version = outer.state["next"]
                tag = "openwrt-v" + version
                failure = "echo 'NaiLink: Update failed; previous installation and service state restored.' >&2\nexit 23\n" if outer.state['fail'] == 'install' else ''
                installer = ("#!/bin/sh\nrelease=" + tag + "\nset -e\n. /lib/functions.sh\nPKG_UPGRADE=1 pkgname=daed-router default_prerm\nwhile [ -e /tmp/fixture-update-hold ]; do sleep 1; done\nsleep 3\n" + failure + "PKG_UPGRADE=1 pkgname=daed-router default_postinst\nprintf '" + version + "-1\\n' > /usr/share/daed-router/version\n").encode()
                assets = {"install.sh": installer, "SHA256SUMS": (hashlib.sha256(installer).hexdigest() + "  install.sh\n").encode()}
                for name in ("geoip.dat", "geosite.dat"):
                    data = (fixture / name).read_bytes()
                    if outer.state["fail"] == "invalid_geodata" and name == "geosite.dat": data = b"<html>not protobuf</html>"
                    assets[name] = data
                    assets[name + ".sha256sum"] = (hashlib.sha256(data).hexdigest() + "  " + name + "\n").encode()
                if outer.state["fail"] == "checksum": assets["geosite.dat"] += b"corrupt"
                if path == "/repos/slobys/NaiLink/releases":
                    data = json.dumps([{"tag_name": tag, "draft": False, "assets": [{"name": name} for name in ("install.sh", "SHA256SUMS", "manifest.tsv")]}]).encode()
                elif path == "/repos/Loyalsoldier/v2ray-rules-dat/releases/latest":
                    data = json.dumps({"tag_name": outer.state["geo"], "draft": False, "assets": [{"name": name} for name in ("geoip.dat", "geosite.dat", "geoip.dat.sha256sum", "geosite.dat.sha256sum")]}).encode()
                elif path.startswith("/slobys/NaiLink/releases/download/" + tag + "/") or path.startswith("/Loyalsoldier/v2ray-rules-dat/releases/download/" + outer.state["geo"] + "/"):
                    data = assets.get(path.rsplit("/", 1)[-1])
                    if data is None: self.send_error(404); return
                else:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *args):
                pass

        # Do not replace or kill any listener. Pick a free loopback address.
        for suffix in range(2, 6):
            try:
                self.server = http.server.ThreadingHTTPServer((f"127.0.0.{suffix}", 443), Handler)
                break
            except OSError:
                continue
        else: raise RuntimeError("No free fixture HTTPS loopback endpoint")
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(cert, key)
        self.server.socket = tls.wrap_socket(self.server.socket, server_side=True)
        host = self.server.server_address[0]
        (root / "etc/hosts").write_text("127.0.0.1 localhost\n" + host + " api.github.com github.com\n")
        bundle = root / "etc/ssl/certs/ca-certificates.crt"
        bundle.parent.mkdir(parents=True, exist_ok=True)
        bundle.unlink(missing_ok=True)
        bundle.write_bytes(cert.read_bytes())
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        destination = root / "usr/libexec/daed/nailink-maintenance"
        destination.write_bytes((project / "openwrt/files/maintenance.init").read_bytes())
        destination.chmod(0o755)
        filelist = root / "usr/lib/opkg/info/daed-router.list"
        filelist.parent.mkdir(parents=True, exist_ok=True)
        filelist.write_text("/usr/libexec/daed/nailink-maintenance\n")

    def procd(self):
        (self.root / "var/lock").mkdir(parents=True, exist_ok=True)
        return subprocess.Popen(["unshare", "--user", "--map-root-user", "--mount", "--ipc", "--uts", "--pid", "--fork", "--kill-child=SIGKILL", "--mount-proc=proc", "chroot", ".", "/bin/sh", "-c", "/sbin/procd -S & wait"], cwd=self.root, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def close(self):
        self.server.shutdown()
        self.thread.join()
        self.server.server_close()
