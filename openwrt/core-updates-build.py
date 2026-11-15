#!/usr/bin/env python3
"""Build independently published, reviewed cores using the original packagers.

The legacy bundled versions stay pinned; this catalog is a separate channel.
Publish only after the adapter and protocol tests pass for these artifacts.
"""
import argparse
import hashlib
import os
import subprocess
import tarfile
import urllib.request
import json
from pathlib import Path
import sys

def build_singbox(entry, cpu, work, output):
    import singbox
    if ' go1.26.0 ' not in subprocess.check_output(['go','version'],text=True):
        raise ValueError('Reviewed build requires Go 1.26.0')
    work.mkdir(parents=True,exist_ok=True)
    output.mkdir(parents=True,exist_ok=True)
    compressed=work/'upstream.tar.gz'
    if not compressed.exists():
        with urllib.request.urlopen(entry['source_url'],timeout=60) as response:
            compressed.write_bytes(response.read(32<<20))
    if hashlib.sha256(compressed.read_bytes()).hexdigest()!=entry['source_sha256']:
        raise ValueError('Upstream source checksum mismatch')
    tree=work/'source'
    if tree.exists():
        raise ValueError('Use a fresh work directory')
    tree.mkdir()
    with tarfile.open(compressed) as archive:
        archive.extractall(tree,filter='data')
    source=tree/('sing-box-'+entry['upstream'])
    reality=source/'common/tls/reality_client.go'
    reality.write_text(singbox.patch_reality(reality.read_text()),encoding='utf-8',newline='\n')
    singbox.patch_domain_route(source)
    tags='with_quic,with_utls,with_clash_api,with_gvisor'
    env={**os.environ,'GOOS':'linux','CGO_ENABLED':'0','GOTOOLCHAIN':'local'}
    host=subprocess.check_output(['go','env','GOHOSTARCH'],text=True).strip()
    subprocess.run(['go','test','-tags',tags,'-run','^TestNaiLinkDomainDialOptIn$','./route'],cwd=source,env={**env,'GOARCH':host},check=True)
    binary=work.resolve()/'sing-box'
    subprocess.run(['go','build','-trimpath','-buildvcs=false','-tags',tags,'-ldflags',
                    '-s -w -buildid= -X github.com/sagernet/sing-box/constant.Version='+entry['version'],
                    '-o',str(binary),'./cmd/sing-box'],cwd=source,env={**env,'GOARCH':singbox.CPUS[cpu]},check=True)
    payload=singbox.archive_bytes([('./usr/bin/sing-box',binary.read_bytes(),0o755)])
    name='nailink-sing-box-'+entry['version']+'-'+cpu+'.tar.gz'
    (output/name).write_bytes(singbox.archive_bytes([('data.tar.gz',payload,0o644),('LICENSE',(source/'LICENSE').read_bytes(),0o644)]))
    metadata={**entry,'go':'1.26.0','patch':'preserve REALITY key-share pairing and opt-in post-rule proxy domain dialing'}
    entries=[(str(p.relative_to(source)),p.read_bytes(),0o644) for p in sorted(source.rglob('*')) if p.is_file()]
    entries += [('NAILINK_SOURCE.json',(json.dumps(metadata,indent=2)+'\n').encode(),0o644),
                ('NAILINK_BUILD.py',Path(__file__).read_bytes(),0o644),
                ('NAILINK_PACKAGER.py',Path(singbox.__file__).read_bytes(),0o644)]
    (output/('nailink-sing-box-'+entry['version']+'-source.tar.gz')).write_bytes(singbox.archive_bytes(entries))

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--engine', choices=('sing-box', 'mihomo'), required=True)
    parser.add_argument('--cpu', choices=('x86_64', 'arm64'), required=True)
    parser.add_argument('--work', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    catalog = json.loads((Path(__file__).parent/'core-updates-sources.json').read_text())
    entry = catalog[args.engine]
    if args.engine == 'mihomo':
        import mihomo
        mihomo.VERSION = entry['version']
        mihomo.COMMIT = entry['commit']
        mihomo.SOURCE_URL = entry['source_url']
        mihomo.SOURCE_SHA = entry['source_sha256']
        mihomo.CPUS = entry['binaries']
        mihomo.package(args.cpu, args.work, args.output)
    else:
        build_singbox(entry, args.cpu, args.work.resolve(), args.output.resolve())

    channel = json.loads((Path(__file__).parent/'core-updates.json').read_text())
    arch = {'x86_64':'amd64', 'arm64':'arm64'}[args.cpu]
    asset = channel['engines'][args.engine]['assets'][arch]
    if hashlib.sha256((args.output/asset['archive']).read_bytes()).hexdigest() != asset['sha256']:
        raise ValueError('Rebuilt core differs from the reviewed update catalog')

if __name__ == '__main__':
    main()
