#!/usr/bin/env python3
"""Reproducible Mihomo compatibility build: route original IPs, dial proxy names."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile
import urllib.request

import build
from singbox import archive_bytes


def patch_destination(root):
    def change(relative, before, after):
        path = root / relative
        text = path.read_text()
        if text.count(before) != 1:
            raise ValueError('Unexpected Mihomo destination implementation: ' + relative)
        path.write_text(text.replace(before, after), encoding='utf-8', newline='\n')

    change('component/sniffer/dispatcher.go', '\t\tmetadata.DstIP = netip.Addr{}',
           '\t\t// NaiLink retains the original IP for rule matching. Proxy adapters\n'
           '\t\t// encode Host; direct egress retains the original address below.')
    for method in ('DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error)',
                   'ListenPacketContext(ctx context.Context, metadata *C.Metadata) (C.PacketConn, error)'):
        anchor = 'func (d *Direct) ' + method + ' {'
        change('adapter/outbound/direct.go', anchor,
               anchor + '\n\tmetadata = nailinkOriginalDirectMetadata(metadata)')
    direct = root / 'adapter/outbound/direct.go'
    direct.write_text(direct.read_text() + '''
// A sniffed name must not turn an explicit direct IP decision into a new DNS
// lookup with a different address family. Never mutate the caller's metadata.
func nailinkOriginalDirectMetadata(metadata *C.Metadata) *C.Metadata {
 if metadata.SniffHost != "" && metadata.DstIP.IsValid() {
  copied := *metadata
  copied.Host = ""
  copied.DNSMode = C.DNSNormal
  return &copied
 }
 return metadata
}
''', encoding='utf-8', newline='\n')
    (root / 'component/sniffer/nailink_destination_test.go').write_text('''package sniffer
import("testing";"net/netip"; C "github.com/metacubex/mihomo/constant")
func TestNaiLinkSniffKeepsOriginalDestination(t *testing.T) {
 for _, ip := range []string{"192.0.2.123", "2001:db8::123"} {
  original := netip.MustParseAddr(ip)
  m := &C.Metadata{DstIP: original}
  replaceDomain(m, "www.example.com", true)
  if m.DstIP != original || m.Host != "www.example.com" || m.SniffHost != "www.example.com" || m.AddrType() != C.AtypDomainName { t.Fatal("lost routing IP or proxy domain") }
 }
}
func TestNaiLinkRouteOnlySniffUnchanged(t *testing.T) {
 m := &C.Metadata{DstIP: netip.MustParseAddr("2001:db8::1")}
 replaceDomain(m, "www.example.com", false)
 if m.Host != "" || m.DstIP.String() != "2001:db8::1" || m.SniffHost != "www.example.com" { t.Fatal("changed route-only sniffing") }
}
''', encoding='utf-8', newline='\n')
    (root / 'adapter/outbound/nailink_direct_test.go').write_text('''package outbound
import("testing";"net/netip"; C "github.com/metacubex/mihomo/constant")
func TestNaiLinkDirectPreservesSniffedIP(t *testing.T) {
 for _, ip := range []string{"192.0.2.5", "2001:db8::5"} {
  m := &C.Metadata{Host:"example.com", SniffHost:"example.com", DstIP:netip.MustParseAddr(ip), DstPort:443}
  got := nailinkOriginalDirectMetadata(m)
  if got == m || got.Host != "" || got.DstIP != m.DstIP || m.Host != "example.com" { t.Fatal("lost original direct IP or mutated caller") }
 }
}
func TestNaiLinkDirectKeepsDomainOnlyAndIPDialing(t *testing.T) {
 for _, m := range []*C.Metadata{{Host:"example.com"}, {DstIP:netip.MustParseAddr("2001:db8::5")}} {
  if nailinkOriginalDirectMetadata(m) != m { t.Fatal("rewrote non-sniffed metadata") }
 }
}
''', encoding='utf-8', newline='\n')
    files = ['component/sniffer/dispatcher.go', 'adapter/outbound/direct.go',
             'component/sniffer/nailink_destination_test.go', 'adapter/outbound/nailink_direct_test.go']
    subprocess.run(['gofmt', '-w', *[str(root / p) for p in files]], check=True)


def package(entry, cpu, work, output):
    if ' go1.26.0 ' not in subprocess.check_output(['go', 'version'], text=True):
        raise ValueError('Reviewed build requires Go 1.26.0')
    work, output = work.resolve(), output.resolve()
    work.mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    compressed = work / 'upstream.tar.gz'
    if not compressed.exists():
        with urllib.request.urlopen(entry['source_url'], timeout=60) as response:
            compressed.write_bytes(response.read(32 << 20))
    if hashlib.sha256(compressed.read_bytes()).hexdigest() != entry['source_sha256']:
        raise ValueError('Mihomo source checksum mismatch')
    source = work / 'source'
    if source.exists():
        raise ValueError('Use a fresh work directory')
    source.mkdir()
    with tarfile.open(compressed) as archive:
        for item in archive.getmembers():
            item.name = '/'.join(item.name.split('/')[1:])
            if item.name:
                if not (source / item.name).resolve().is_relative_to(source):
                    raise ValueError('Unsafe source member')
                archive.extract(item, source, filter='data')
    patch_destination(source)
    env = {**os.environ, 'GOOS': 'linux', 'CGO_ENABLED': '0', 'GOTOOLCHAIN': 'local'}
    host = subprocess.check_output(['go', 'env', 'GOHOSTARCH'], text=True).strip()
    subprocess.run(['go', 'test', '-mod=readonly', '-count=1', './component/sniffer', './adapter/outbound', '-run', 'NaiLink'],
                   cwd=source, env={**env, 'GOARCH': host}, check=True)
    binary = work / 'mihomo'
    subprocess.run(['go', 'build', '-mod=readonly', '-trimpath', '-buildvcs=false',
                    '-ldflags', '-s -w -buildid= -X github.com/metacubex/mihomo/constant.Version=v' + entry['version'],
                    '-o', str(binary), '.'], cwd=source,
                   env={**env, 'GOARCH': {'x86_64': 'amd64', 'arm64': 'arm64'}[cpu]}, check=True)
    build.verify_elf(binary, cpu)
    metadata = {**entry, 'go': '1.26.0', 'patch': 'preserve routing IP, proxy domain and original direct destination'}
    payload = archive_bytes([('./usr/bin/mihomo', binary.read_bytes(), 0o755)])
    name = 'nailink-mihomo-' + entry['version']
    (output / (name + '-' + cpu + '.tar.gz')).write_bytes(archive_bytes([
        ('data.tar.gz', payload, 0o644), ('LICENSE', (source / 'LICENSE').read_bytes(), 0o644),
        ('SOURCE.json', (json.dumps({**metadata, 'cpu': cpu}, indent=2) + '\n').encode(), 0o644)]))
    entries = [(str(p.relative_to(source)), p.read_bytes(), 0o644) for p in sorted(source.rglob('*')) if p.is_file()]
    here = Path(__file__).resolve().parent
    entries += [('NAILINK_SOURCE.json', (json.dumps(metadata, indent=2) + '\n').encode(), 0o644),
                ('NAILINK_BUILD.py', Path(__file__).read_bytes(), 0o644),
                ('build.py', (here / 'build.py').read_bytes(), 0o644),
                ('singbox.py', (here / 'singbox.py').read_bytes(), 0o644)]
    (output / (name + '-source.tar.gz')).write_bytes(archive_bytes(entries))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cpu', choices=('x86_64', 'arm64'), required=True)
    parser.add_argument('--work', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    metadata = Path(__file__).with_name('NAILINK_SOURCE.json')
    entry = json.loads(metadata.read_text()) if metadata.exists() else json.loads(Path(__file__).with_name('core-updates-sources.json').read_text())['mihomo']
    package(entry, args.cpu, args.work, args.output)
