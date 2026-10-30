#!/usr/bin/env python3
"""Real dae/sing-box datapath in private mount/PID/network namespaces, no host rules.

Requires Linux root, iproute2, nft CLI, a verified sing-box and bundled runtime.
The environment check shim replaces only OpenWrt/UCI discovery inside this
Linux fixture; actual BPF, TUN, nftables, DNS, TCP and core processes are real.
"""
import argparse
import hashlib
import http.server
import json
import os
import re
from pathlib import Path
import shutil
import socket
import socketserver
import sqlite3
import struct
import subprocess
import sys
import threading
import time
import urllib.request

PROJECT = Path(__file__).resolve().parents[2]
UUID_PROXY = "22222222-2222-4222-8222-222222222222"
UUID_AI = "33333333-3333-4333-8333-333333333333"

def run(*args, **kwargs):
    try:return subprocess.check_output(list(map(str, args)), stderr=subprocess.STDOUT, **kwargs).decode()
    except subprocess.CalledProcessError as error:
        print(error.output.decode(errors="replace"),file=sys.stderr,flush=True);raise

def dns_answer(data):
    end = data.find(b"\0", 12) + 5
    if end < 17 or end > len(data):
        return b""
    labels, offset = [], 12
    while data[offset]:
        size = data[offset]; labels.append(data[offset+1:offset+1+size].decode()); offset += size+1
    name = ".".join(labels)
    kind = struct.unpack("!H", data[end-4:end-2])[0]
    address = "203.0.113.81" if name.startswith("ai") else "203.0.113.80"
    value=socket.inet_aton(address) if kind==1 else socket.inet_pton(socket.AF_INET6,"2001:db8:83::81" if name.startswith('ai') else "2001:db8:83::80") if kind==28 else b''
    header = data[:2]+struct.pack("!HHHHH", 0x8180, 1, 1 if value else 0, 0, 0)
    return header+data[12:end]+(b"\xc0\x0c"+struct.pack("!HHIH", kind, 1, 30, len(value))+value if value else b"")

def serve_wan(args):
    class HTTP(http.server.BaseHTTPRequestHandler):
        def do_HEAD(self):
            self.send_response(204); self.send_header("Content-Length", "0"); self.end_headers()
        def do_GET(self):
            body=json.dumps({"peer": self.client_address[0], "payload": "core-network-"*256}).encode()
            self.send_response(200); self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
        def log_message(self, *_): pass
    class UDP(socketserver.BaseRequestHandler):
        def handle(self):
            data, sock=self.request; reply=dns_answer(data)
            if reply: sock.sendto(reply, self.client_address)
    class TCP(socketserver.StreamRequestHandler):
        def handle(self):
            while header:=self.rfile.read(2):
                if len(header)!=2: return
                data=self.rfile.read(struct.unpack("!H",header)[0]);reply=dns_answer(data)
                if not reply:return
                self.wfile.write(struct.pack("!H",len(reply))+reply);self.wfile.flush()
    class Echo(socketserver.BaseRequestHandler):
        def handle(self):
            data,sock=self.request
            if data==b'nailink-transparent-udp':sock.sendto(json.dumps({'peer':self.client_address[0]}).encode(),self.client_address)
    servers=[http.server.ThreadingHTTPServer(("0.0.0.0",8080),HTTP),socketserver.ThreadingUDPServer(("198.51.100.53",53),UDP),socketserver.ThreadingTCPServer(("198.51.100.53",53),TCP),socketserver.ThreadingUDPServer(('0.0.0.0',9000),Echo)]
    if args.ipv6:
        class HTTP6(http.server.ThreadingHTTPServer):
            address_family=socket.AF_INET6
            def server_bind(self):
                self.socket.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1);super().server_bind()
        servers.append(HTTP6(("::",8080),HTTP))
        class Echo6(socketserver.ThreadingUDPServer):
            address_family=socket.AF_INET6
            def server_bind(self):
                self.socket.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1);super().server_bind()
        servers.append(Echo6(('::',9000),Echo))
    for server in servers:threading.Thread(target=server.serve_forever,daemon=True).start()
    config={"log":{"level":"info"},"inbounds":[{"type":"vless","tag":tag,"listen":"10.82.0.2","listen_port":port,"users":[{"uuid":uuid}]} for tag,port,uuid in [("proxy-in",3080,UUID_PROXY),("ai-in",3081,UUID_AI)]],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}
    path=args.workspace/"server.json";path.write_text(json.dumps(config))
    config["dns"]={"servers":[{"type":"udp","tag":"fixture","server":"198.51.100.53"}],"final":"fixture"}
    config["route"]["default_domain_resolver"]="fixture"
    path.write_text(json.dumps(config))
    with (args.workspace/"server.log").open("w") as log:
        process=subprocess.Popen([args.singbox,"run","--disable-color","-c",str(path)],stdout=log,stderr=log)
        try:process.wait()
        finally:
            process.terminate()
            for server in servers:server.shutdown()

def client_dns(address,name):
    qtype=28 if name in ('probe6.test','ai6.test') else 1
    question=b"".join(bytes([len(p)])+p.encode() for p in name.split("."))+b"\0"+struct.pack('!HH',qtype,1)
    payload=struct.pack("!HHHHHH",12345,0x100,1,0,0,0)+question
    with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as sock:
        sock.settimeout(8);sock.sendto(payload,(address,53));answer=sock.recv(8192)
    assert answer[:2]==payload[:2] and answer[3]&15==0 and struct.unpack("!H",answer[6:8])[0]>0,answer.hex()
    print(socket.inet_ntop(socket.AF_INET6,answer[-16:]) if qtype==28 else socket.inet_ntoa(answer[-4:]))

def digest_database(path):
    with sqlite3.connect(f"file:{path}?mode=ro",uri=True) as db:
        names=[row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
        rows={name:db.execute('SELECT * FROM "'+name+'" ORDER BY rowid').fetchall() for name in names}
    return hashlib.sha256(json.dumps(rows,sort_keys=True,default=str).encode()).hexdigest()

def inside(args):
    assert os.geteuid()==0 and os.readlink("/proc/self/ns/net")!=args.host_netns,"refusing host network"
    run("mount","--make-rprivate","/")
    # Shadow every global path used for BPF pins, named namespaces, optional
    # core binaries and runtime state before starting either core.
    for target,kind in [("/run","tmpfs"),("/usr/libexec","tmpfs"),("/sys/fs/bpf","bpf")]:run("mount","-t",kind,kind,target)
    etc={name:Path("/etc",name).read_bytes() for name in ("hosts","resolv.conf","nsswitch.conf","passwd","group") if Path("/etc",name).is_file()}
    run("mount","-t","tmpfs","tmpfs","/etc")
    for name,data in etc.items():Path("/etc",name).write_bytes(data)
    Path("/etc/nftables.d").mkdir()
    assert args.cgroup_root and Path(args.cgroup_root).name.startswith("nailink-core-test-")
    daemon_cgroup=Path(args.cgroup_root)/"daemon";daemon_cgroup.mkdir()
    run("mount","--bind",daemon_cgroup,"/sys/fs/cgroup")
    private=Path("/usr/libexec/daed");(private/"cores").mkdir(parents=True,mode=0o700)
    shutil.copy2(args.singbox,private/"cores/sing-box");os.chmod(private/"cores/sing-box",0o755)
    if args.mihomo:
        shutil.copy2(args.mihomo,private/"cores/mihomo");os.chmod(private/"cores/mihomo",0o755)
    shutil.copy2(args.probe,private/"btf-probe")
    # Keep real production TPROXY capability syntax in the discovery shim;
    # only the firmware/UCI discovery is replaced in this Linux namespace.
    check_source=(PROJECT/'openwrt/files/daed-check').read_text()
    tproxy_probe=re.search(r"if (printf 'table inet nailink_probe_.*?\| nft --check -f -) >/dev/null",check_source)
    assert tproxy_probe, 'Missing production TPROXY environment probe'
    (private/"check").write_text('#!/bin/sh\nset -e\ncase "$2" in\n dae) exec /usr/libexec/daed/btf-probe --kernel ;;\n sing-box|mihomo) if [ "$4" = tproxy ]; then '+tproxy_probe.group(1)+'; else test -c /dev/net/tun; fi; exec nft -j list tables ;;\n *) exit 1 ;;\nesac\n')
    os.chmod(private/"check",0o755)
    run("nft","add","table","inet","unrelated_sentinel")
    if args.fw4:
        (private/"bin").mkdir(); fw4=private/"bin/fw4"
        fw4.write_text('''#!/usr/bin/python3
import pathlib,subprocess
body='delete table inet fw4\\ntable inet fw4 { chain input { type filter hook input priority 0; policy drop; iifname "lo" accept; iifname "lan0" accept; ct state established,related accept; } chain forward { type filter hook forward priority 0; policy drop; ct state established,related accept; iifname "lan0" oifname "wan0" accept; iifname "wan0" oifname "lan0" accept; } '
body=body.replace('} chain','}\\n chain').replace('} ','}\\n ')
body+=''.join(p.read_text() for p in pathlib.Path('/etc/nftables.d').glob('*.nft'))+'}\\n'
subprocess.run(['nft','-f','-'],input=body.encode(),check=True)
''');fw4.chmod(0o755);os.environ['PATH']=str(private/'bin')+':'+os.environ['PATH']
        run("nft","add","table","inet","fw4");run("fw4","reload")
    for name in ("core-client","core-wan"):run("ip","netns","add",name)
    run("ip","link","set","lo","up")
    for link,peer,space,local,remote in [("lan0","lan-peer","core-client","10.81.0.1/24","10.81.0.2/24"),("wan0","wan-peer","core-wan","10.82.0.1/24","10.82.0.2/24")]:
        run("ip","link","add",link,"type","veth","peer","name",peer);run("ip","link","set",peer,"netns",space)
        physical=link
        if link=='lan0' and args.bridge:
            physical='lan-port';run('ip','link','set',link,'name',physical);run('ip','link','add',link,'type','bridge');run('ip','link','set',physical,'master',link);run('ip','link','set',physical,'up')
        run("ip","address","add",local,"dev",link);run("ip","link","set",link,"up")
        run("ip","netns","exec",space,"ip","link","set","lo","up");run("ip","netns","exec",space,"ip","address","add",remote,"dev",peer);run("ip","netns","exec",space,"ip","link","set",peer,"up")
        run("ip","netns","exec",space,"ip","route","add","default","via",local.split('/')[0])
        run("ethtool","-K",physical,"rx","off","tx","off")
        run("ip","netns","exec",space,"ethtool","-K",peer,"rx","off","tx","off")
    run("ip","route","add","default","via","10.82.0.2","dev","wan0")
    if args.bridge:
        assert Path('/proc/sys/net/bridge/bridge-nf-call-iptables').exists(), 'Bridge netfilter is required for this fixture'
        run('sysctl','-w','net.bridge.bridge-nf-call-iptables=1','net.bridge.bridge-nf-call-ip6tables=1')
        run('nft','add','table','bridge','unrelated_bridge_sentinel')
    if args.ipv6:
        for iface,space,local,remote in [('lan0','core-client','fd81:81::1/64','fd81:81::2/64'),('wan0','core-wan','fd81:82::1/64','fd81:82::2/64')]:
            peer='lan-peer' if iface=='lan0' else 'wan-peer';run('ip','-6','addr','add',local,'dev',iface,'nodad');run('ip','netns','exec',space,'ip','-6','addr','add',remote,'dev',peer,'nodad');run('ip','netns','exec',space,'ip','-6','route','add','default','via',local.split('/')[0])
        run('ip','-6','route','add','default','via','fd81:82::2','dev','wan0');run('sysctl','-w','net.ipv6.conf.all.forwarding=1')
        for address in ('2001:db8:83::80/128','2001:db8:83::81/128'):run('ip','netns','exec','core-wan','ip','-6','addr','add',address,'dev','lo','nodad')
    for addr in ("203.0.113.80/32","203.0.113.81/32","198.51.100.53/32"):run("ip","netns","exec","core-wan","ip","address","add",addr,"dev","lo")
    run("sysctl","-w","net.ipv4.ip_forward=1")
    run("sysctl","-w","net.ipv4.conf.all.rp_filter=0","net.ipv4.conf.default.rp_filter=0")
    child_args=[sys.executable,str(Path(__file__).resolve()),"--wan","--workspace",str(args.workspace),"--singbox",args.singbox]
    if args.ipv6:child_args.append('--ipv6')
    wan=subprocess.Popen(["ip","netns","exec","core-wan",*child_args])
    time.sleep(.4)
    print("Fixture DNS:",run("ip","netns","exec","core-client",sys.executable,Path(__file__).resolve(),"--client-dns","198.51.100.53","probe.test").strip(),flush=True)
    config=args.workspace/"config";config.mkdir(mode=0o700)
    output=(args.workspace/"daemon.log").open("w")
    if args.bridge:
        run('nft','-f','-',input=b'table bridge nailink_test_trace {\n chain ingress {\n type filter hook prerouting priority -400; policy accept;\n meta l4proto tcp tcp dport 8080 meta nftrace set 1\n }\n}\n')
        trace_output=(args.workspace/'trace.log').open('w')
        trace_process=subprocess.Popen(['nft','monitor','trace'],stdout=trace_output,stderr=trace_output)
    def enter_daemon_cgroup():Path("/sys/fs/cgroup/cgroup.procs").write_text(str(os.getpid()))
    daemon=subprocess.Popen([args.runtime,"run","--config",str(config),"--listen","127.0.0.1:12025"],stdout=output,stderr=output,preexec_fn=enter_daemon_cgroup)
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}));token=""
    def gql(query, *, explicit_mode=False):
        if not explicit_mode:
            query=query.replace('corePlan(', 'corePlan(mode:'+json.dumps(args.mode)+',').replace('switchCore(', 'switchCore(mode:'+json.dumps(args.mode)+',')
        request=urllib.request.Request("http://127.0.0.1:12025/graphql",json.dumps({"query":query}).encode(),{"Content-Type":"application/json","Authorization":"Bearer "+token})
        with opener.open(request,timeout=160) as response:data=json.load(response)
        if data.get("errors"):raise RuntimeError(json.dumps(data["errors"]))
        return data["data"]
    def wait_ready():
        for _ in range(300):
            if daemon.poll() is not None:raise RuntimeError("daemon exited: "+(args.workspace/"daemon.log").read_text()[-6000:])
            try:
                with opener.open("http://127.0.0.1:12025/health/ready",timeout=2) as response:
                    if json.load(response).get("ready"):return
            except OSError:pass
            time.sleep(.1)
        raise TimeoutError("core readiness")
    try:
        wait_ready();token=gql('mutation{createUser(username:"fixture",password:"fixture-password123")}')["createUser"]
        global_input='lanInterface:["lan0"],wanInterface:["wan0"],tcpCheckUrl:["http://probe.test:8080/generate_204","203.0.113.80"],udpCheckDns:["198.51.100.53:53","198.51.100.53"],bootstrapResolver:"198.51.100.53:53",fallbackResolver:"198.51.100.53:53",disableWaitingNetwork:true,autoConfigKernelParameter:true,checkInterval:"600s",logLevel:"debug"'
        if args.target_core=='mihomo':global_input=global_input.replace('wanInterface:["wan0"]','wanInterface:["auto"]')
        cid=gql('mutation{createConfig(name:"Core fixture",global:{'+global_input+'}){id}}')["createConfig"]["id"]
        dns="upstream { test: 'udp://198.51.100.53:53' }\nrouting { request { fallback: test } }"
        if args.target_core=='mihomo':dns="upstream { domestic: 'udp://198.51.100.53:53'\n foreignTcp: 'tcp://198.51.100.53:53'\n foreignUdp: 'udp://198.51.100.53:53' }\nrouting { request { qname(full:ai.test,full:ai6.test) -> foreignTcp\nqname(full:probe.test,full:probe6.test) -> foreignUdp\nfallback: domestic } }"
        did=gql('mutation{createDns(name:"Core DNS",dns:'+json.dumps(dns)+'){id}}')["createDns"]["id"]
        routing="dip(10.82.0.0/24) -> direct\ndomain(full:ai.test,full:ai6.test) -> AI\nfallback: proxy"
        rid=gql('mutation{createRouting(name:"Core route",routing:'+json.dumps(routing)+'){id}}')["createRouting"]["id"]
        gql('mutation{selectConfig(id:'+json.dumps(cid)+') selectDns(id:'+json.dumps(did)+') selectRouting(id:'+json.dumps(rid)+')}')
        node_ids=[]
        for name,uuid,port in [("proxy",UUID_PROXY,3080),("AI",UUID_AI,3081)]:
            node=gql('mutation{importNodes(rollbackError:true,args:[{tag:'+json.dumps(name+"-fixture")+',link:'+json.dumps(f"vless://{uuid}@10.82.0.2:{port}?encryption=none&security=none&type=tcp#fixture")+'}]){error node{id}}}')["importNodes"][0]
            assert not node["error"],node
            node_ids.append(node['node']['id'])
            group=gql('mutation{createGroup(name:'+json.dumps(name)+',policy:fixed,policyParams:[{key:"",val:"0"}]){id}}')["createGroup"]["id"]
            gql('mutation{groupAddNodes(id:'+json.dumps(group)+',nodeIDs:['+json.dumps(node["node"]["id"])+'])}')
        gql('mutation{run(dry:false)}');wait_ready()
        daemon.terminate();daemon.wait(timeout=35)
        daemon=subprocess.Popen([args.runtime,"run","--config",str(config),"--listen","127.0.0.1:12025"],stdout=output,stderr=output,preexec_fn=enter_daemon_cgroup)
        wait_ready()
        run("ethtool","-K","dae0","rx","off","tx","off","gro","off","gso","off","tso","off")
        run("ip","netns","exec","daens","ethtool","-K","dae0peer","rx","off","tx","off","gro","off","gso","off","tso","off")
        run("ip","netns","exec","daens","sysctl","-w","net.ipv4.conf.all.rp_filter=0","net.ipv4.conf.dae0peer.rp_filter=0")
        capture=(args.workspace/"packets.log").open("w")
        tcpdump=subprocess.Popen(["tcpdump","-Z","root","-l","-n","-i","any","udp","or","tcp"],stdout=capture,stderr=capture)
        capture_ns=(args.workspace/"packets-daens.log").open("w")
        tcpdump_ns=subprocess.Popen(["ip","netns","exec","daens","tcpdump","-Z","root","-l","-n","-i","any","udp","or","tcp"],stdout=capture_ns,stderr=capture_ns)
        def traffic(label):
            server_offset=(args.workspace/'server.log').stat().st_size
            destinations=[("probe.test","203.0.113.80"),("ai.test","203.0.113.81")]
            if args.ipv6:destinations.extend([('probe6.test','2001:db8:83::80'),('ai6.test','2001:db8:83::81')])
            for name,expected in destinations:
                got=run("ip","netns","exec","core-client",sys.executable,Path(__file__).resolve(),"--client-dns","198.51.100.53",name).strip();assert got==expected,(label,name,got)
                resolved='['+expected+']' if ':' in expected else expected
                body=run("ip","netns","exec","core-client","curl","--noproxy","*","--resolve",f"{name}:8080:{resolved}","-fsS","-m","10",f"http://{name}:8080/identity")
                assert json.loads(body)["peer"] not in ("10.81.0.2","10.82.0.1",'fd81:81::2','fd81:82::1'),(label,"traffic bypassed proxy",body)
            # A real non-DNS UDP payload must traverse transparent ingress.
            for address in (['203.0.113.80','2001:db8:83::80'] if args.ipv6 else ['203.0.113.80']):
                code='import socket,json;address='+repr(address)+';s=socket.socket(socket.AF_INET6 if ":" in address else socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(10);s.sendto(b"nailink-transparent-udp",(address,9000));print(s.recv(4096).decode())'
                data=json.loads(run('ip','netns','exec','core-client',sys.executable,'-c',code))
                assert data['peer'] not in ('10.81.0.2','10.82.0.1','fd81:81::2','fd81:82::1'),(label,'UDP bypassed proxy',data)
            # Simulate an old/poisoned client DNS cache. Domain dial mode must
            # route by the real HTTP host and let the proxy resolve it, without
            # requiring clients to flush caches after switching cores.
            body=run("ip","netns","exec","core-client","curl","--noproxy","*","--resolve","probe.test:8080:203.0.113.250","-fsS","-m","10","http://probe.test:8080/identity")
            assert json.loads(body)["peer"] not in ("10.81.0.2","10.82.0.1"),(label,"stale DNS cache bypassed proxy",body)
            records=(args.workspace/'server.log').read_text()[server_offset:]
            assert 'inbound/vless[ai-in]' in records,(label,'AI rule did not use its independent inbound',records)
            print("PASS actual LAN DNS, TCP and AI routing:",label,flush=True)
        traffic("dae before switching")
        gql('mutation{testNodeLatencies(ids:'+json.dumps(node_ids)+'){alive message}}')
        gql('mutation{updateConfig(id:'+json.dumps(cid)+',global:{checkInterval:"601s"}){id}}')
        before=digest_database(config/"wing.db")
        original_rules=run('ip','-j','rule')
        if args.bridge and args.mode=='tproxy':
            keys=['net.ipv4.conf.all.rp_filter','net.ipv4.conf.lan-port.rp_filter']
            previous={key:run('sysctl','-n',key).strip() for key in keys}
            run('sysctl','-w',*(key+'=1' for key in keys))
            blocked=json.loads(gql('{corePlan(target:"'+args.target_core+'")}')['corePlan'])
            assert not blocked['compatible'] and 'tproxy_bridge_rpf' in blocked['problems'],blocked
            assert json.loads(gql('{coreStatus}')['coreStatus'])['current']=='dae'
            assert all(run('sysctl','-n',key).strip()=='1' for key in keys)
            run('sysctl','-w',*(key+'='+value for key,value in previous.items()))
        plan=json.loads(gql('{corePlan(target:"'+args.target_core+'")}')["corePlan"]);assert plan["compatible"],plan
        gql('mutation{switchCore(target:"'+args.target_core+'",revision:'+json.dumps(plan["revision"])+')}')
        assert gql('{nodeLatencies(ids:'+json.dumps(node_ids)+'){alive}}')['nodeLatencies']==[], 'results from the previous core leaked'
        traffic(args.target_core);assert digest_database(config/"wing.db")==before,"switch modified user tables"
        if args.mode=='tproxy':
            assert not any(link['ifname']=='nailink0' for link in json.loads(run('ip','-j','link'))),'TPROXY unexpectedly created a TUN'
            state=json.loads((config/'cores/state.json').read_text());assert state['mode']=='tproxy',state
            owner=json.loads((config/'cores/network.json').read_text())
            if args.fw4:run('nft','chain','inet','fw4','input','{ policy accept; }')
            for port in (owner['proxy_port'],owner['dns_port']):
                code='import socket\ntry:\n s=socket.create_connection(("10.82.0.1",'+str(port)+'),timeout=.5)\nexcept OSError:pass\nelse:raise AssertionError("WAN can access private managed listener")'
                run('ip','netns','exec','core-wan',sys.executable,'-c',code)
            if args.fw4:run('nft','chain','inet','fw4','input','{ policy drop; }')
            keys=['net.ipv4.conf.all.rp_filter','net.ipv4.conf.lo.rp_filter','net.ipv4.conf.lan0.rp_filter']
            previous={key:run('sysctl','-n',key).strip() for key in keys}
            if not args.bridge:
                run('sysctl','-w',*(key+'=1' for key in keys));traffic('TPROXY with strict reverse-path filtering')
                run('sysctl','-w',*(key+'='+value for key,value in previous.items()))
            for mode in ('tun','tproxy'):
                plan=json.loads(gql('{corePlan(target:'+json.dumps(args.target_core)+',mode:'+json.dumps(mode)+')}',explicit_mode=True)['corePlan'])
                assert plan['compatible'],plan
                gql('mutation{switchCore(target:'+json.dumps(args.target_core)+',mode:'+json.dumps(mode)+',revision:'+json.dumps(plan['revision'])+')}',explicit_mode=True)
                traffic('same engine mode '+mode)
                assert any(link['ifname']=='nailink0' for link in json.loads(run('ip','-j','link')))==(mode=='tun')
            run('ip','addr','add','203.0.113.91/32','dev','wan0')
            for _ in range(60):
                if '203.0.113.91' in run('nft','list','set','inet','nailink_tproxy','local4'):break
                time.sleep(.1)
            else:raise AssertionError('WAN address bypass set was not refreshed')
            run('ip','addr','del','203.0.113.91/32','dev','wan0')
        for device, direction in [('lan0', 'ingress'), ('wan0', 'egress')]:
            filters = json.loads(run('tc', '-j', 'filter', 'show', 'dev', device, direction))
            assert not any(item.get('kind') == 'bpf' for item in filters), 'previous dae capture remained attached'
        probes=gql('mutation{testNodeLatencies(ids:'+json.dumps(node_ids)+'){alive message}}')['testNodeLatencies'];assert all(p['alive'] for p in probes),probes
        udp=gql('mutation{testNodeUDP(id:'+json.dumps(node_ids[0])+'){alive message}}')['testNodeUDP'];assert udp['alive'],udp
        if args.fw4:
            assert Path('/etc/nftables.d/0-nailink-core.nft').is_file()
            assert not Path('/etc/nftables.d/0-sing-box-auto-redirect.nft').exists()
            run('fw4','reload');traffic(args.target_core+' after fw4 reload')
            table=json.loads(run('nft','-j','list','table','inet','fw4'))
            policies={entry['chain']['name']:entry['chain'].get('policy') for entry in table['nftables'] if 'chain' in entry}
            assert policies['input']==policies['forward']=='drop',policies
        owner=json.loads((config/'cores/network.json').read_text());os.kill(owner['pid'],9)
        for _ in range(600):
            state=json.loads((config/'cores/state.json').read_text())
            if state.get('code')=='core_restarted' and state['phase']=='stable':break
            time.sleep(.1)
        else:raise AssertionError('core was not recovered')
        traffic(args.target_core+' after unexpected exit')
        run('ip','link','set','wan0','down');time.sleep(.4);run('ip','link','set','wan0','up')
        # Model netifd/DHCP restoring its default route after link recovery.
        run('ip','route','replace','default','via','10.82.0.2','dev','wan0')
        if args.ipv6:
            run('ip','-6','addr','replace','fd81:82::1/64','dev','wan0','nodad')
            run('ip','-6','route','replace','default','via','fd81:82::2','dev','wan0')
        time.sleep(.6);traffic(args.target_core+' after WAN recovery')
        if args.target_core=='mihomo':
            for target in ('sing-box','mihomo'):
                plan=json.loads(gql('{corePlan(target:'+json.dumps(target)+')}')['corePlan']);assert plan['compatible'],plan
                gql('mutation{switchCore(target:'+json.dumps(target)+',revision:'+json.dumps(plan['revision'])+')}')
                traffic('native core round trip '+target);assert digest_database(config/'wing.db')==before
        assert gql('{general{dae{modified}}}')["general"]["dae"]["modified"],"switch applied pending edits"
        stats=gql('{general{runtimeOverview(windowSec:60,maxPoints:60){downloadTotal uploadTotal}}}')["general"]["runtimeOverview"]
        assert int(stats["downloadTotal"])>0,stats
        plan=json.loads(gql('{corePlan(target:"dae")}')["corePlan"]);assert plan["compatible"],plan
        gql('mutation{switchCore(target:"dae",revision:'+json.dumps(plan["revision"])+')}')
        traffic("dae after switching back");assert digest_database(config/"wing.db")==before
        assert run('ip','-j','rule')==original_rules,'managed policy rules leaked after switching back'
        assert not (config/'cores/network.json').exists() and not Path('/etc/nftables.d/0-nailink-core.nft').exists()
        run('nft','list','table','inet','unrelated_sentinel')
        if args.bridge:
            run('nft','list','table','bridge','unrelated_bridge_sentinel')
            assert run('sysctl','-n','net.bridge.bridge-nf-call-iptables').strip()=='1'
            assert run('sysctl','-n','net.bridge.bridge-nf-call-ip6tables').strip()=='1'
            assert 'nailink_broute' not in run('nft','list','tables')
        managed=private/("cores/"+args.target_core);managed.rename(private/("cores/"+args.target_core+".real"))
        managed.write_text('#!/bin/sh\ncase "$1" in '+('version|check' if args.target_core=='sing-box' else '-v|-t')+') exec /usr/libexec/daed/cores/'+args.target_core+'.real "$@" ;; *) exit 77 ;; esac\n');managed.chmod(0o755)
        plan=json.loads(gql('{corePlan(target:"'+args.target_core+'")}')["corePlan"]);assert plan["compatible"],plan
        try:gql('mutation{switchCore(target:"'+args.target_core+'",revision:'+json.dumps(plan["revision"])+')}');raise AssertionError("late startup failure was accepted")
        except RuntimeError as error:assert "core_rolled_back" in str(error),error
        traffic("automatic rollback");assert digest_database(config/"wing.db")==before
        result={"target_core":args.target_core,"real_bpf":True,"real_tun_nftables":True,"lan_dns_tcp_ai":True,"dae_optional_dae":True,"late_failure_rollback":True,"all_user_tables_preserved":True,"pending_edits_preserved":True,"singbox_stats":stats,"selected_core_http_udp_probes":True,"unexpected_exit_recovery":True,"wan_recovery":True,"fw4_reload_policies_preserved":args.fw4,"no_policy_rule_leaks":True,"ipv6":args.ipv6}
        result.update(mode=args.mode,lan_udp=True,same_engine_mode_roundtrip=args.mode=='tproxy',real_tproxy_nftables=args.mode=='tproxy',production_tproxy_probe=args.mode=='tproxy',bridge_netfilter=args.bridge)
        (args.workspace/"result.json").write_text(json.dumps(result,indent=2));print(json.dumps(result),flush=True)
    finally:
        if 'trace_process' in locals():
            trace_process.terminate();trace_process.wait(timeout=5);trace_output.close()
        if args.bridge:
            for command in [('nft','list','ruleset'),('ip','-d','link'),('ip','neigh'),('sysctl','net.ipv4.conf.all.rp_filter','net.ipv4.conf.lan-port.rp_filter','net.ipv4.conf.all.src_valid_mark','net.ipv4.conf.lan-port.src_valid_mark')]:
                try:print('BRIDGE DIAGNOSTIC',command,run(*command),flush=True)
                except subprocess.CalledProcessError:pass
            owner=config/'cores/network.json'
            if owner.exists():print('NETWORK OWNER',owner.read_text(),flush=True)
            for name in ['trace.log','daemon.log','server.log','packets.log','packets-daens.log']:
                log=args.workspace/name
                if log.exists():print('BRIDGE LOG',name,log.read_text(errors='replace')[-48000:],flush=True)
        if 'tcpdump' in locals():tcpdump.terminate();tcpdump.wait(timeout=5);capture.close()
        if 'tcpdump_ns' in locals():tcpdump_ns.terminate();tcpdump_ns.wait(timeout=5);capture_ns.close()
        for command in [("ip","rule"),("ip","route","show","table","all"),("tc","filter","show","dev","lan0","ingress"),("tc","filter","show","dev","wan0","egress")]:
            try:print(command,run(*command),flush=True)
            except subprocess.CalledProcessError:pass
        for command in [("ip","rule"),("ip","route","show","table","all"),("ss","-lunpt"),("tc","filter","show","dev","dae0peer","ingress")]:
            try:print("daens",command,run("ip","netns","exec","daens",*command),flush=True)
            except subprocess.CalledProcessError:pass
        daemon.terminate()
        try:daemon.wait(timeout=35)
        except subprocess.TimeoutExpired:daemon.kill();daemon.wait()
        output.close();wan.terminate()
        try:wan.wait(timeout=5)
        except subprocess.TimeoutExpired:wan.kill();wan.wait()

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    for name in ("runtime","mihomo","singbox","probe","nft-root","workspace","host-netns","cgroup-root"):parser.add_argument("--"+name)
    parser.add_argument("--inside",action="store_true");parser.add_argument("--wan",action="store_true");parser.add_argument("--client-dns",nargs=2)
    parser.add_argument('--target-core',choices=['sing-box','mihomo'],default='sing-box')
    parser.add_argument('--fw4',action='store_true')
    parser.add_argument('--ipv6',action='store_true')
    parser.add_argument('--mode',choices=['tun','tproxy'],default='tun')
    parser.add_argument('--bridge',action='store_true')
    args=parser.parse_args();args.workspace=Path(args.workspace) if args.workspace else None
    if args.client_dns:return client_dns(*args.client_dns)
    if args.wan:return serve_wan(args)
    if args.inside:return inside(args)
    assert args.workspace and not args.workspace.exists(),"use a new dedicated test workspace"
    args.workspace.mkdir(parents=True,mode=0o700)
    env=os.environ.copy();env["PATH"]=str(Path(args.nft_root)/"usr/bin")+":"+str(Path(args.nft_root)/"usr/sbin")+":"+env["PATH"];env["LD_LIBRARY_PATH"]=str(Path(args.nft_root)/"usr/lib/x86_64-linux-gnu")
    # The cgroup mount must expose only this disposable process subtree;
    # otherwise dae's socket hooks could attach to the host's cgroup root.
    cgroup=Path("/sys/fs/cgroup")/("nailink-core-test-"+str(os.getpid()))
    cgroup.mkdir()
    def enter_cgroup():
        (cgroup/"cgroup.procs").write_text(str(os.getpid()))
    command=["unshare","--mount","--net","--cgroup","--pid","--fork","--kill-child=SIGKILL","--mount-proc",sys.executable,str(Path(__file__).resolve()),*sys.argv[1:],"--inside","--host-netns",os.readlink("/proc/self/ns/net"),"--cgroup-root",str(cgroup)]
    try:subprocess.run(command,env=env,check=True,timeout=300,preexec_fn=enter_cgroup)
    finally:
        if (cgroup/"daemon").exists():(cgroup/"daemon").rmdir()
        cgroup.rmdir()

if __name__=="__main__":main()
