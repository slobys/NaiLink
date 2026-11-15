#!/usr/bin/env python3
"""Build the optional, reproducible sing-box compatibility distribution.

No router configuration or upstream service scripts are installed. The patches
preserve REALITY key shares and opt-in proxy domain dialing after rule matching;
authentication remains required and direct/IP dialing is unchanged.
"""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import urllib.request

VERSION = '1.14.3-nailink.1'
TAG = 'openwrt-v0.6.35'
SOURCE_URL = 'https://codeload.github.com/SagerNet/sing-box/tar.gz/refs/tags/v1.14.3'
SOURCE_SHA256 = '9a8e3712a5f611ad95f1d16874bbdcdffbd3b8eed16dae2d71ce7042a33c3386'
CPUS = {'x86_64': 'amd64', 'arm64': 'arm64'}


def archive_bytes(entries):
    """Stable paths, timestamps, owner and gzip header across build machines."""
    output = io.BytesIO()
    with gzip.GzipFile(fileobj=output, mode='wb', filename='', mtime=0, compresslevel=9) as gz:
        with tarfile.open(fileobj=gz, mode='w', format=tarfile.USTAR_FORMAT) as tf:
            for name, data, mode in sorted(entries):
                item = tarfile.TarInfo(name)
                item.size, item.mode, item.mtime = len(data), mode, 0
                tf.addfile(item, io.BytesIO(data))
    return output.getvalue()


def patch_reality(text):
    start = text.index('\tfor _, extension := range uConn.Extensions {')
    end = text.index('\terr = uConn.BuildHandshakeState()', start)
    original = text[start:end]
    if original.count('X25519MLKEM768') != 2 or 'common.Filter' not in original:
        raise ValueError('Unexpected upstream REALITY implementation')
    # Preserve the ClientHello key shares and their generated private keys as a
    # pair. Removing ML-KEM after BuildHandshakeState can invalidate REALITY's
    # authenticated ClientHello. Do not change SNI, fingerprint, public key,
    # short ID, cipher or certificate authentication to make a connection pass.
    text = text[:start] + text[end:]
    text = text.replace('\t"github.com/sagernet/sing/common"\n', '')
    old = '\tecdheKey := keyShareKeys.Ecdhe\n'
    if text.count(old) != 1:
        raise ValueError('Unexpected upstream key state')
    return text.replace(old, old + '\tif ecdheKey == nil {\n\t\tecdheKey = keyShareKeys.MlkemEcdhe\n\t}\n')


def patch_domain_route(root):
    def change(relative, old, new):
        path = root / relative
        text = path.read_text()
        if text.count(old) != 1:
            raise ValueError('Unexpected upstream domain route implementation: ' + relative)
        path.write_text(text.replace(old, new), encoding='utf-8', newline='\n')
    change('option/rule_action.go', 'type RawRouteOptionsActionOptions struct {',
           'type RawRouteOptionsActionOptions struct {\n\tOverrideDomain bool `json:"override_domain,omitempty"`')
    change('route/rule/rule_action.go', 'type RuleActionRouteOptions struct {',
           'type RuleActionRouteOptions struct {\n\tOverrideDomain bool')
    change('route/rule/rule_action.go', '\t\tOverridePort:              options.OverridePort,',
           '\t\tOverridePort:              options.OverridePort,\n\t\tOverrideDomain:            options.OverrideDomain,')
    change('route/route.go', 'func applyRouteOptionsOverride(metadata *adapter.InboundContext, routeOptions *R.RuleActionRouteOptions) {',
           '''func applyRouteOptionsOverride(metadata *adapter.InboundContext, routeOptions *R.RuleActionRouteOptions) {
	// Opt-in by NaiLink only for proxy rules in dae domain dial mode, after
	// their original IP/domain conditions have matched. Direct rules and IP
	// dial mode are unchanged; never silently turn an IP literal into a name.
	if routeOptions.OverrideDomain && !routeOptions.OverrideAddress.IsValid() && metadata.Destination.IsIP() && M.IsDomainName(metadata.Domain) {
		if !metadata.RouteOriginalDestination.IsValid() { metadata.RouteOriginalDestination = metadata.Destination }
		metadata.Destination = M.Socksaddr{Fqdn: metadata.Domain, Port: metadata.Destination.Port}
		metadata.DestinationAddresses = nil
	}''')
    (root / 'route/nailink_route_test.go').write_text('''package route
import (
 "net/netip"
 "testing"
 "github.com/sagernet/sing-box/adapter"
 R "github.com/sagernet/sing-box/route/rule"
 M "github.com/sagernet/sing/common/metadata"
)
func TestNaiLinkDomainDialOptIn(t *testing.T) {
 original := M.ParseSocksaddr("31.13.92.37:443")
 m := adapter.InboundContext{Destination:original, Domain:"www.google.com", DestinationAddresses:[]netip.Addr{original.Addr}}
 applyRouteOptionsOverride(&m,&R.RuleActionRouteOptions{})
 if m.Destination != original {t.Fatal("changed default IP/direct behavior")}
 applyRouteOptionsOverride(&m,&R.RuleActionRouteOptions{OverrideDomain:true})
 if m.Destination.Fqdn != "www.google.com" || m.Destination.Port != 443 || m.RouteOriginalDestination != original || len(m.DestinationAddresses)!=0 {t.Fatal("did not preserve domain dial semantics")}
 m.Destination=original;m.Domain="31.13.92.37"
 applyRouteOptionsOverride(&m,&R.RuleActionRouteOptions{OverrideDomain:true})
 if m.Destination != original {t.Fatal("rewrote an IP literal")}
 m.Domain="www.google.com"
 fixed:=M.ParseSocksaddr("192.0.2.1:0")
 applyRouteOptionsOverride(&m,&R.RuleActionRouteOptions{OverrideDomain:true,OverrideAddress:fixed})
 if m.Destination.Addr!=fixed.Addr {t.Fatal("overrode an explicit route target")}
}
''', encoding='utf-8')
    subprocess.run(['gofmt', '-w', str(root / 'option/rule_action.go'), str(root / 'route/rule/rule_action.go'),
                    str(root / 'route/route.go'), str(root / 'route/nailink_route_test.go')], check=True)


def source(work):
    compressed = work / 'upstream.tar.gz'
    if not compressed.exists():
        with urllib.request.urlopen(SOURCE_URL, timeout=60) as response:
            compressed.write_bytes(response.read(32 << 20))
    if hashlib.sha256(compressed.read_bytes()).hexdigest() != SOURCE_SHA256:
        raise ValueError('Upstream source checksum mismatch')
    tree = work / 'source'
    if tree.exists():
        # The directory belongs only to this build, never the user's checkout.
        raise ValueError('Use a fresh work directory; refusing to replace existing sources')
    tree.mkdir()
    with tarfile.open(compressed) as tf:
        tf.extractall(tree, filter='data')
    root = tree / 'sing-box-1.14.3'
    path = root / 'common/tls/reality_client.go'
    path.write_text(patch_reality(path.read_text()), encoding='utf-8', newline='\n')
    patch_domain_route(root)
    return root


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cpu', choices=CPUS, required=True)
    parser.add_argument('--work', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--verify-pins', action='store_true')
    args = parser.parse_args()
    go = subprocess.check_output(['go', 'version'], text=True)
    if ' go1.26.0 ' not in go:
        raise ValueError('Reproducible core build requires Go 1.26.0')
    args.work.mkdir(parents=True, exist_ok=True)
    args.output.mkdir(parents=True, exist_ok=True)
    root = source(args.work.resolve())
    subprocess.run(['go', 'test', '-tags', 'with_quic,with_utls,with_clash_api,with_gvisor',
                    '-run', '^TestNaiLinkDomainDialOptIn$', './route'], cwd=root, check=True,
                   env={**os.environ, 'GOOS': 'linux', 'GOARCH': subprocess.check_output(['go', 'env', 'GOHOSTARCH'], text=True).strip(), 'CGO_ENABLED': '0', 'GOTOOLCHAIN': 'local'})
    binary = args.work.resolve() / 'sing-box'
    env = {**os.environ, 'GOOS': 'linux', 'GOARCH': CPUS[args.cpu], 'CGO_ENABLED': '0', 'GOTOOLCHAIN': 'local'}
    subprocess.run(['go', 'build', '-trimpath', '-buildvcs=false',
                    '-tags', 'with_quic,with_utls,with_clash_api,with_gvisor',
                    '-ldflags', '-s -w -buildid= -X github.com/sagernet/sing-box/constant.Version=' + VERSION,
                    '-o', str(binary), './cmd/sing-box'], cwd=root, env=env, check=True)
    license_data = (root / 'LICENSE').read_bytes()
    payload = archive_bytes([('./usr/bin/sing-box', binary.read_bytes(), 0o755)])
    name = 'nailink-sing-box-' + VERSION + '-' + args.cpu + '.tar.gz'
    (args.output / name).write_bytes(archive_bytes([('data.tar.gz', payload, 0o644), ('LICENSE', license_data, 0o644)]))
    digest = hashlib.sha256((args.output / name).read_bytes()).hexdigest()
    if args.verify_pins:
        pins = (Path(__file__).parent / 'corelib/state.go').read_text()
        if not re.search('"' + re.escape(name) + r'",\s*"' + digest + '"', pins):
            raise ValueError('Rebuilt archive differs from the reviewed installer checksum')
    metadata = {'upstream': 'SagerNet/sing-box v1.14.3', 'source_url': SOURCE_URL, 'source_sha256': SOURCE_SHA256,
                'version': VERSION, 'go': '1.26.0', 'patch': 'preserve REALITY key-share pairing and opt-in post-rule proxy domain dialing'}
    entries = [(str(p.relative_to(root)), p.read_bytes(), 0o644) for p in sorted(root.rglob('*')) if p.is_file()]
    entries += [('NAILINK_SOURCE.json', (json.dumps(metadata, indent=2) + '\n').encode(), 0o644),
                ('NAILINK_BUILD.py', Path(__file__).read_bytes(), 0o644)]
    # Both CPU jobs must produce the same corresponding-source archive.
    (args.output / ('nailink-sing-box-' + VERSION + '-source.tar.gz')).write_bytes(archive_bytes(entries))
    print(json.dumps({'file': name, 'sha256': digest}))


if __name__ == '__main__':
    main()
