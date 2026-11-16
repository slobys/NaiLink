#!/usr/bin/env python3
"""Test real proxy handshakes against an independent loopback sing-box server.

Requires Go, OpenSSL and a separately installed sing-box (tested with 1.14.3).
Run prepare-wing.py first. No provider credentials or router access are used.
"""
import argparse
import base64
import contextlib
import http.server
import json
import os
from pathlib import Path
import secrets
import socket
import socketserver
import ssl
import struct
import subprocess
import tempfile
import threading
import time
import urllib.parse


class HTTP(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def do_HEAD(self):
        self.send_response(204)
        self.send_header('Content-Length', '0')
        self.end_headers()

    def do_GET(self):
        data = b"protocol-payload" * 4096 if self.path in ('/payload', '/stream') else b""
        self.send_response(200 if data else 204)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        for offset in range(0, len(data), 8192):
            self.wfile.write(data[offset:offset+8192])
            if self.path == '/stream':
                self.wfile.flush()
                time.sleep(0.03)

    def log_message(self, *args):
        pass


class UDP(socketserver.BaseRequestHandler):
    def handle(self):
        data, sock = self.request
        if len(data) > 16 and struct.unpack('!HH', data[2:6]) == (0x100, 1):
            end = data.find(b'\x00', 12) + 5
            if end > 16 and end <= len(data):
                answer = b'\xc0\x0c' + struct.pack('!HHIH', 1, 1, 60, 4) + socket.inet_aton('192.0.2.1')
                sock.sendto(data[:2] + struct.pack('!HHHHH', 0x8180, 1, 1, 0, 0) + data[12:end] + answer, self.client_address)
                return
        sock.sendto(data, self.client_address)


class HTTPServer(http.server.ThreadingHTTPServer):
    # Native probes race both address families, and the bulk test exercises
    # eight workers. The stdlib's queue of five can drop loopback SYN bursts.
    request_queue_size = 128
    daemon_threads = True


_reserved_ports = {}


def release_port(number):
    for sock in _reserved_ports.pop(number, []):
        sock.close()


def port(udp=False):
    # SS and several other inbounds bind both transports. Keep both ports
    # reserved until the server starts, including while fixture code compiles.
    for _ in range(64):
        tcp = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        udp_socket = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        try:
            # The child must rebind after reservations are released. Keep its
            # ports outside the standard Linux ephemeral range so unrelated
            # outbound sockets cannot claim them in that handoff window.
            number = 10000 + secrets.randbelow(20000)
            tcp.bind(('127.0.0.1', number))
            udp_socket.bind(('127.0.0.1', number))
        except OSError:
            tcp.close(); udp_socket.close()
            continue
        _reserved_ports[number] = [tcp, udp_socket]
        return number
    raise RuntimeError('Cannot reserve unique protocol fixture ports')


def run(args):
    with HTTPServer((args.fixture_address, 0), HTTP) as http_server, socketserver.UDPServer((args.fixture_address, 0), UDP) as udp:
        for server in (http_server, udp):
            threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            return run_fixture(args, http_server.server_port, udp.server_address[1])
        finally:
            http_server.shutdown()
            udp.shutdown()


def run_fixture(args, http_port, udp_port):
    with tempfile.TemporaryDirectory(prefix="daed-protocol-") as temp, contextlib.ExitStack() as cleanup:
        work = Path(temp)
        cleanup.callback(lambda: [release_port(n) for n in list(_reserved_ports)])
        subprocess.run([
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(work / "key.pem"),
            "-out", str(work / "cert.pem"), "-subj", "/CN=fixture.test", "-addext", "subjectAltName=DNS:fixture.test,IP:127.0.0.1", "-days", "1",
        ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        tls = {"enabled": True, "certificate_path": str(work / "cert.pem"), "key_path": str(work / "key.pem")}
        # Trust only a separate local VLESS test CA. Other protocol fixtures keep
        # their untrusted certificate so negative TLS checks remain meaningful.
        subprocess.run([
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(work / "vless-key.pem"),
            "-out", str(work / "vless-cert.pem"), "-subj", "/CN=vless.fixture.test", "-addext", "subjectAltName=DNS:vless.fixture.test", "-days", "1",
        ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        vless_tls = {"enabled": True, "certificate_path": str(work / "vless-cert.pem"), "key_path": str(work / "vless-key.pem")}
        cover = HTTPServer(('127.0.0.1', 0), HTTP)
        cover_tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        cover_tls.minimum_version = ssl.TLSVersion.TLSv1_3
        cover_tls.load_cert_chain(work / 'vless-cert.pem', work / 'vless-key.pem')
        cover_tls.set_alpn_protocols(['h2', 'http/1.1'])
        cover.socket = cover_tls.wrap_socket(cover.socket, server_side=True)
        threading.Thread(target=cover.serve_forever, daemon=True).start()
        cleanup.callback(cover.server_close)
        cleanup.callback(cover.shutdown)
        pair = subprocess.check_output([str(args.sing_box), 'generate', 'reality-keypair'], text=True)
        reality_keys = dict(line.split(': ', 1) for line in pair.strip().splitlines())
        inbounds, cases, native_cases = [], [], []
        endpoints = []
        def native_case(name, value, family, tcp_only=False):
            encoded = base64.urlsafe_b64encode(json.dumps(value).encode()).decode().rstrip('=')
            native_cases.append({'Name': name, 'Link': 'singbox://' + encoded, 'Family': family, 'TCPOnly': tcp_only})
        if args.client_extra:
            naive_port = port()
            inbounds.append({'type':'naive','tag':'naive','listen':'127.0.0.1','listen_port':naive_port,'network':'tcp',
                             'users':[{'username':'fixture','password':'fixture'}],'tls':vless_tls})
            for uot in (False,True):
                node={'type':'naive','server':'127.0.0.1','server_port':naive_port,'username':'fixture','password':'fixture',
                      'tls':{'enabled':True,'server_name':'vless.fixture.test','certificate':[(work/'vless-cert.pem').read_text()]}}
                if uot: node['udp_over_tcp']={'enabled':True,'version':2}
                native_case('naive-cronet'+('-uot' if uot else ''),node,'naive',not uot)
            node['tls']['server_name']='wrong.fixture.invalid'
            native_case('naive-reject-certificate',node,'naive-negative',True)
            native_cases[-1]['Failure']='tls_failed'
            keys=[]
            for _ in range(2):
                pair=subprocess.check_output([str(args.sing_box),'generate','wg-keypair'],text=True)
                keys.append(dict(line.split(': ',1) for line in pair.strip().splitlines()))
            wg_port=port(True)
            endpoints.append({'type':'wireguard','tag':'wireguard-server','system':False,'address':['10.66.66.1/32'],
                              'private_key':keys[0]['PrivateKey'],'listen_port':wg_port,'workers':1,
                              'peers':[{'public_key':keys[1]['PublicKey'],'allowed_ips':['10.66.66.2/32']}]})
            native_case('wireguard-endpoint',{'type':'wireguard','system':False,'private_key':keys[1]['PrivateKey'],
                        'address':['10.66.66.2/32'],'peers':[{'address':'127.0.0.1','port':wg_port,'public_key':keys[0]['PublicKey'],
                        'allowed_ips':['0.0.0.0/0']}]} ,'wireguard')
            native_cases[-1]['TargetAlt']=f'http://10.66.66.1:{http_port}'
            native_cases[-1]['UDPAlt']=f'10.66.66.1:{udp_port}'
        h1 = port(True)
        inbounds.append({'type': 'hysteria', 'tag': 'hysteria1', 'listen': '127.0.0.1', 'listen_port': h1,
                         'up_mbps': 20, 'down_mbps': 100, 'users': [{'auth_str': 'fixture'}], 'tls': vless_tls})
        native_cases.append({'Name': 'hysteria1-uri', 'Link': f'hysteria://fixture@127.0.0.1:{h1}?peer=vless.fixture.test&upmbps=20&downmbps=100'})
        snell = port()
        inbounds.append({'type': 'snell', 'tag': 'snell6', 'version': 6, 'listen': '127.0.0.1', 'listen_port': snell, 'psk': 'fixture-snell-password'})
        native_cases.append({'Name': 'snell6-uri', 'Link': f'snell://fixture-snell-password@127.0.0.1:{snell}?version=6'})
        inner, outer = port(), port()
        inbounds.extend([
            {'type': 'shadowsocks', 'tag': 'shadow-inner', 'listen': '127.0.0.1', 'listen_port': inner, 'method': 'aes-128-gcm', 'password': 'fixture'},
            {'type': 'shadowtls', 'tag': 'shadow-outer', 'listen': '127.0.0.1', 'listen_port': outer, 'version': 3,
             'users': [{'password': 'fixture'}], 'handshake': {'server': '127.0.0.1', 'server_port': cover.server_port}, 'detour': 'shadow-inner'},
        ])
        native_case('shadowtls3-bundle', {'tag': 'inner', 'outbounds': [
            {'type': 'shadowsocks', 'tag': 'inner', 'server': '127.0.0.1', 'server_port': inner, 'method': 'aes-128-gcm', 'password': 'fixture', 'detour': 'outer'},
            {'type': 'shadowtls', 'tag': 'outer', 'server': '127.0.0.1', 'server_port': outer, 'version': 3, 'password': 'fixture', 'tls': {'enabled': True, 'server_name': 'vless.fixture.test'}},
        ]}, 'shadowtls', True)
        ssh_port = port()
        subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(work / 'ssh-key')], check=True)
        ssh_source = Path(__file__).with_name('ssh_fixture.go')
        subprocess.run(['go', 'build', '-o', str(work / 'ssh-fixture'), str(ssh_source)], cwd=args.wing, check=True)
        release_port(ssh_port)
        ssh_log = cleanup.enter_context((work / 'ssh.log').open('w', encoding='utf-8'))
        ssh_process = subprocess.Popen([str(work / 'ssh-fixture'), str(work / 'ssh-key'), str(ssh_port), str(http_port)], stdout=ssh_log, stderr=subprocess.STDOUT)
        cleanup.callback(ssh_process.wait, 5)
        cleanup.callback(ssh_process.terminate)
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if ssh_process.poll() is not None:
                raise RuntimeError('SSH fixture exited: ' + (work / 'ssh.log').read_text())
            try:
                with socket.create_connection(('127.0.0.1', ssh_port), timeout=0.2) as connection:
                    if connection.recv(256).startswith(b'SSH-'):
                        break
            except OSError:
                pass
            time.sleep(0.05)
        else:
            raise RuntimeError('SSH fixture was not ready: ' + (work / 'ssh.log').read_text())
        host_key = ' '.join((work / 'ssh-key.pub').read_text().split()[:2])
        native_cases.append({'Name': 'ssh-uri-host-key', 'Link': f'ssh://fixture:fixture@127.0.0.1:{ssh_port}?host_key=' + urllib.parse.quote(host_key, safe=''), 'TCPOnly': True})
        native_case('ssh-native-json', {'type': 'ssh', 'server': '127.0.0.1', 'server_port': ssh_port, 'user': 'fixture', 'password': 'fixture', 'host_key': [host_key]}, 'ssh-json', True)
        reality_port = port()
        reality_uuid = '00000000-0000-4000-8000-000000000002'
        inbounds.append({'type': 'vless', 'tag': 'reality-vision', 'listen': '127.0.0.1', 'listen_port': reality_port,
                         'users': [{'uuid': reality_uuid, 'flow': 'xtls-rprx-vision'}],
                         'tls': {'enabled': True, 'server_name': 'vless.fixture.test', 'reality': {'enabled': True,
                             'handshake': {'server': '127.0.0.1', 'server_port': cover.server_port},
                             'private_key': reality_keys['PrivateKey'], 'short_id': ['abcd']}}})
        reality_link = (f'vless://{reality_uuid}@127.0.0.1:{reality_port}?security=reality&sni=vless.fixture.test'
                        f'&pbk={reality_keys["PublicKey"]}&sid=abcd&fp=chrome&flow=xtls-rprx-vision&headerType=none&spx=%2F')
        cases.append({'Name': 'vless-reality-vision', 'Link': reality_link})
        native_cases.append({'Name': 'reality-exporter-hybrid-hint', 'Link': reality_link + '&support-x25519mlkem768=true'})
        cases.append({'Name': 'vless-reality-reject-wrong-short-id', 'Link': reality_link.replace('sid=abcd', 'sid=ffff'), 'Failure': 'unreachable'})
        for method in ["aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305"]:
            number = port()
            password = "fixture@pass:2026"
            if method.startswith("2022"):
                password = base64.b64encode(b"k" * (16 if method == "2022-blake3-aes-128-gcm" else 32)).decode()
            inbounds.append({"type": "shadowsocks", "tag": method, "listen": "127.0.0.1", "listen_port": number, "method": method, "password": password})
            user = base64.urlsafe_b64encode((method + ":" + password).encode()).decode().rstrip("=")
            cases.append({"Name": method, "Link": f"ss://{user}@127.0.0.1:{number}"})
            cases.append({'Name': 'clash-' + method, 'Clash': {'name': 'clash-ss', 'type': 'ss', 'server': '127.0.0.1', 'port': number, 'cipher': method, 'password': password, 'udp': True}})
            if method == 'chacha20-ietf-poly1305':
                native_cases.append({'Name': 'ss-exporter-plain-tcp', 'Link': f'ss://{user}@127.0.0.1:{number}?type=tcp&headerType=none'})
            if method.startswith('2022'):
                cases.append({"Name": method + '-sip002', "Link": f"ss://{method}:{urllib.parse.quote(password, safe='')}@127.0.0.1:{number}"})
        for kind in ['vmess', 'trojan']:
            for transport in ['tcp', 'ws']:
                number = port()
                uuid = '00000000-0000-4000-8000-000000000001'
                inbound = {'type': kind, 'tag': kind + '-' + transport, 'listen': '127.0.0.1', 'listen_port': number, 'tls': vless_tls}
                inbound['users'] = [{'uuid': uuid, 'alterId': 0}] if kind == 'vmess' else [{'password': 'fixture@pass:2026'}]
                if transport == 'ws':
                    inbound['transport'] = {'type': 'ws', 'path': '/fixture'}
                inbounds.append(inbound)
                if kind == 'vmess':
                    config = {'v': '2', 'ps': kind + '-' + transport, 'add': '127.0.0.1', 'port': str(number), 'id': uuid, 'aid': '0', 'scy': 'auto', 'net': transport, 'type': 'none', 'host': '', 'path': '/fixture' if transport == 'ws' else '', 'tls': 'tls', 'sni': 'vless.fixture.test'}
                    link = 'vmess://' + base64.b64encode(json.dumps(config).encode()).decode()
                    if transport == 'ws':
                        exported = {**config, 'class': 3, 'remark': 'export label', 'headerType': 'none', 'verify_cert': True}
                        native_cases.append({'Name': 'vmess-exporter-certificate-verified', 'Link': 'vmess://' + base64.b64encode(json.dumps(exported).encode()).decode()})
                        exported['sni'] = 'mismatch.invalid'
                        native_cases.append({'Name': 'vmess-exporter-certificate-rejected', 'Link': 'vmess://' + base64.b64encode(json.dumps(exported).encode()).decode(), 'Failure': 'tls_failed'})
                        exported['verify_cert'] = False
                        native_cases.append({'Name': 'vmess-exporter-explicit-insecure', 'Link': 'vmess://' + base64.b64encode(json.dumps(exported).encode()).decode()})
                else:
                    link = f"trojan://fixture%40pass%3A2026@127.0.0.1:{number}?sni=vless.fixture.test&type={transport}"
                    if transport == 'ws':
                        link += '&path=%2Ffixture'
                cases.append({'Name': kind + '-tls-' + transport, 'Link': link})
                clash = {'name': 'clash-' + kind, 'type': kind, 'server': '127.0.0.1', 'port': number, 'tls': True, 'skip-cert-verify': False, 'sni': 'vless.fixture.test', 'udp': True, 'network': transport}
                if kind == 'vmess':
                    clash.update({'uuid': uuid, 'alterId': 0, 'cipher': 'auto'})
                else:
                    clash['password'] = 'fixture@pass:2026'
                if transport == 'ws':
                    clash.update({'ws-opts': {'path': '/fixture', 'headers': {'Host': 'vless.fixture.test'}}, 'ws-path': '/fixture', 'ws-headers': {'Host': 'vless.fixture.test'}})
                cases.append({'Name': 'clash-' + kind + '-' + transport, 'Clash': clash})
                bad = {**clash, 'sni': 'mismatch.invalid'}
                cases.append({'Name': 'clash-' + kind + '-' + transport + '-certificate-rejected', 'Clash': bad, 'Failure': 'tls_failed'})
                cases.append({'Name': 'clash-' + kind + '-' + transport + '-explicit-insecure', 'Clash': {**bad, 'skip-cert-verify': True}})
                if kind == 'trojan' and transport == 'tcp':
                    cases.append({'Name': 'trojan-exporter-udp-enabled', 'Link': link + '&udp=1'})
        number = port(True)
        inbounds.append({'type': 'tuic', 'tag': 'tuic', 'listen': '127.0.0.1', 'listen_port': number,
                         'users': [{'uuid': uuid, 'password': 'fixture@pass:2026'}], 'congestion_control': 'cubic',
                         'tls': {**vless_tls, 'alpn': ['h3']}})
        cases.append({'Name': 'tuic-v5', 'Link': f'tuic://{uuid}:fixture%40pass%3A2026@127.0.0.1:{number}?sni=vless.fixture.test&alpn=h3&congestion_control=cubic&udp_relay_mode=native'})
        cases.append({'Name': 'clash-tuic-explicit-insecure', 'Clash': {'name': 'clash-tuic', 'type': 'tuic', 'server': '127.0.0.1', 'port': number, 'uuid': uuid, 'password': 'fixture@pass:2026', 'sni': 'mismatch.invalid', 'skip-cert-verify': True, 'alpn': ['h3']}})
        # Cover the user's VLESS path with independent TLS and WebSocket servers.
        for transport in ["tcp", "ws", "grpc"]:
            number = port()
            uuid = "00000000-0000-4000-8000-000000000001"
            inbound = {"type": "vless", "tag": "vless-" + transport, "listen": "127.0.0.1", "listen_port": number,
                       "users": [{"name": "fixture", "uuid": uuid}], "tls": vless_tls}
            if transport == "ws":
                inbound["transport"] = {"type": "ws", "path": "/fixture"}
            elif transport == "grpc":
                inbound["transport"] = {"type": "grpc", "service_name": "fixture"}
            inbounds.append(inbound)
            link = f"vless://{uuid}@127.0.0.1:{number}?security=tls&sni=vless.fixture.test&type={transport}"
            if transport == 'tcp':
                link += '&headerType=none'
            if transport == 'ws':
                link += '&path=%2Ffixture'
            elif transport == 'grpc':
                link += '&serviceName=fixture'
            cases.append({"Name": "vless-tls-" + transport, "Link": link})
            cases.append({"Name": "vless-tls-" + transport + "-certificate-rejected", "Link": link.replace("sni=vless.fixture.test", "sni=mismatch.invalid"), "Failure": "tls_failed"})
            clash_vless = {'name': 'clash-vless', 'type': 'vless', 'server': '127.0.0.1', 'port': number, 'uuid': uuid, 'tls': True, 'sni': 'mismatch.invalid', 'skip-cert-verify': True, 'network': transport}
            if transport == 'ws': clash_vless['ws-opts'] = {'path': '/fixture'}
            if transport == 'grpc': clash_vless['grpc-opts'] = {'grpc-service-name': 'fixture'}
            cases.append({'Name': 'clash-vless-' + transport + '-explicit-insecure', 'Clash': clash_vless})
        for kind in ["hysteria2", "anytls"]:
            number = port(kind == "hysteria2")
            password = "fixture:pass:2026" if kind == "hysteria2" else "fixture@pass:2026"
            inbounds.append({"type": kind, "tag": kind, "listen": "127.0.0.1", "listen_port": number, "users": [{"name": "fixture", "password": password}], "tls": tls})
            link = f"{kind}://{urllib.parse.quote(password, safe='')}@127.0.0.1:{number}?sni=fixture.test&insecure=1"
            cases.append({"Name": kind, "Link": link})
            if kind == 'anytls':
                cases.append({'Name': 'clash-anytls-explicit-insecure', 'Clash': {'name': 'clash-anytls', 'type': 'anytls', 'server': '127.0.0.1', 'port': number, 'password': password, 'sni': 'mismatch.invalid', 'skip-cert-verify': True}})
            if kind == "anytls":
                for option in ["insecure=true", "allowInsecure=true"]:
                    cases.append({"Name": f"anytls-{option}", "Link": link.replace("insecure=1", option)})
                cases.append({"Name": "anytls-certificate-rejected", "Link": link.replace("insecure=1", "insecure=0"), "Failure": "tls_failed"})
            else:
                cases.append({"Name": "hy2-user-password", "Link": link.replace(urllib.parse.quote(password, safe=""), "fixture:pass%3A2026")})
        number = port(True)
        inbounds.append({"type": "hysteria2", "tag": "hy2-obfs", "listen": "127.0.0.1", "listen_port": number, "users": [{"name": "fixture", "password": "fixture"}], "tls": tls, "obfs": {"type": "salamander", "password": "fixture-obfs"}})
        link = f"hy2://fixture@127.0.0.1:{number}?sni=fixture.test&insecure=1&obfs=salamander&obfs-password=fixture-obfs"
        cases.append({"Name": "hysteria2-salamander", "Link": link})
        native_cases.append({'Name': 'hy2-exporter-tls-marker', 'Link': link + '&security=tls'})
        cases.append({"Name": "hysteria2-obfs-alias-ports", "Link": link.replace("obfs-password", "obfsPassword").replace(f":{number}?", ":1?") + f"&ports={number}-{number}"})
        cases.append({"Name": "hysteria2-mport", "Link": link.replace(f":{number}?", ":1?") + f"&mport={number}"})
        # Exercise the exported base64 ECH list against a real independently
        # generated ECH server; merely accepting the config is insufficient.
        ech_pair = subprocess.check_output([str(args.sing_box), 'generate', 'ech-keypair', 'vless.fixture.test'], text=True)
        def pem_block(kind):
            return ech_pair.split('-----BEGIN ' + kind + '-----', 1)[1].split('-----END ' + kind + '-----', 1)[0]
        ech_encoded = ''.join(pem_block('ECH CONFIGS').split())
        ech_key = '-----BEGIN ECH KEYS-----' + pem_block('ECH KEYS') + '-----END ECH KEYS-----\n'
        number = port(True)
        inbounds.append({'type':'hysteria2','tag':'hy2-ech','listen':'127.0.0.1','listen_port':number,
                         'users':[{'password':'ech-fixture'}], 'tls':{**vless_tls,'ech':{'enabled':True,'key':[ech_key]}}})
        native_cases.append({'Name':'hy2-exporter-ech','Link':f'hy2://ech-fixture@127.0.0.1:{number}?security=tls&sni=vless.fixture.test&ech='+urllib.parse.quote(ech_encoded,safe='')})
        juicity_config = None
        if args.juicity_server:
            number = port(True)
            uuid = '00000000-0000-4000-8000-000000000001'
            juicity_config = work / 'juicity.json'
            juicity_config.write_text(json.dumps({'listen': f'127.0.0.1:{number}', 'users': {uuid: 'fixture@pass:2026'},
                'certificate': str(work / 'vless-cert.pem'), 'private_key': str(work / 'vless-key.pem'),
                'congestion_control': 'cubic', 'log_level': 'error'}), encoding='utf-8')
            cases.append({'Name': 'juicity', 'Link': f'juicity://{uuid}:fixture%40pass%3A2026@127.0.0.1:{number}?sni=vless.fixture.test&congestion_control=cubic'})
        config = work / "config.json"
        config.write_text(json.dumps({"log": {"level": "debug"}, "inbounds": inbounds, "endpoints": endpoints, "outbounds": [{"type": "direct", "tag": "direct"}]}), encoding="utf-8")
        casefile = work / "cases.json"
        casefile.write_text(json.dumps({"Target": f"http://{args.fixture_address}:{http_port}", "UDP": f"{args.fixture_address}:{udp_port}", "Cases": cases, "NativeCases":native_cases}), encoding="utf-8")
        subprocess.run([str(args.sing_box), "check", "-c", str(config)], check=True)
        with (work / "server.log").open("w", encoding="utf-8") as log:
            for number in list(_reserved_ports):
                release_port(number)
            process = subprocess.Popen([str(args.sing_box), "run", "-c", str(config)], stdout=log, stderr=subprocess.STDOUT)
            juicity = None
            try:
                if juicity_config:
                    juicity = subprocess.Popen([str(args.juicity_server), 'run', '-c', str(juicity_config)], stdout=log, stderr=subprocess.STDOUT)
                time.sleep(0.6)
                if process.poll() is not None or (juicity and juicity.poll() is not None):
                    raise RuntimeError("independent server exited: " + (work / "server.log").read_text())
                env = os.environ.copy()
                env["DAED_PROTOCOL_CASES"] = str(casefile)
                env["SSL_CERT_FILE"] = str(work / "vless-cert.pem")
                if args.client_singbox:
                    env['DAED_TEST_SINGBOX'] = str(args.client_singbox.resolve(strict=True))
                if args.client_extra:
                    env['DAED_TEST_SINGBOX_EXTRA'] = str(args.client_extra.resolve(strict=True))
                # These packages share the same independent loopback servers.
                # Run package processes in sequence to avoid cross-package
                # listener/startup contention; each package still exercises
                # its production node concurrency and bulk-probe assertions.
                result = 0 if args.mihomo_only else subprocess.run(["go", "test", "-p=1", "-count=1", "-timeout=180s", "-v", "./graphql/service/node", "./dae", "github.com/daeuniverse/dae/control", "-run", "TestRouterProtocolInteroperability$|TestRouterCoreProtocolInteroperability$|TestRouterCoreBulkProbe$|TestRouterHTTPProtocolDownload$|TestRouterConnectionProtocolFailover$"], cwd=args.wing, env=env).returncode
                if result == 0 and args.client_mihomo:
                    env['DAED_TEST_MIHOMO'] = str(args.client_mihomo.resolve(strict=True))
                    result = subprocess.run(['go', 'test', '-count=1', '-timeout=240s', '-v', './dae', '-run', '^TestRouterMihomoProtocolInteroperability$'], cwd=args.wing, env=env).returncode
                if result:
                    # These are generated local test identities, never provider
                    # configuration. Bound the failure trace and retain the gate.
                    lines=(work / "server.log").read_text(errors="replace").splitlines()
                    relevant=[line[:300] for line in lines if 'wireguard' in line.lower() or '10.66.66.' in line]
                    print("Independent WireGuard fixture trace:\n"+'\n'.join(relevant[-48:]),flush=True)
                    print("Independent SSH fixture trace:\n" + '\n'.join((work / 'ssh.log').read_text(errors='replace').splitlines()[-12:]), flush=True)
                return result
            finally:
                process.terminate()
                process.wait(timeout=5)
                if juicity:
                    juicity.terminate()
                    juicity.wait(timeout=5)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wing", type=Path, required=True)
    parser.add_argument("--sing-box", type=Path, required=True)
    parser.add_argument("--juicity-server", type=Path, help="Optional independent official Juicity server (tested with v0.5.0)")
    parser.add_argument('--client-singbox', type=Path, help='Also exercise the optional native sing-box adapter')
    parser.add_argument('--fixture-address', default='127.0.0.1', help='Non-loopback local address for userspace WireGuard tests')
    parser.add_argument('--mihomo-only', action='store_true', help='Run only Mihomo adapter tests while iterating')
    parser.add_argument('--client-mihomo', type=Path, help='Also exercise the optional Mihomo adapter')
    parser.add_argument('--client-extra', type=Path, help='Extracted optional extension bundle')
    raise SystemExit(run(parser.parse_args()))
