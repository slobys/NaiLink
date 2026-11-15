#!/usr/bin/env python3
"""Build independently published, reviewed cores using the original packagers.

The legacy bundled versions stay pinned; this catalog is a separate channel.
Publish only after the adapter and protocol tests pass for these artifacts.
"""
import argparse
import hashlib
import json
from pathlib import Path
import sys

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
        import singbox
        singbox.VERSION = entry['version']
        singbox.UPSTREAM_VERSION = entry['upstream']
        singbox.SOURCE_URL = entry['source_url']
        singbox.SOURCE_SHA256 = entry['source_sha256']
        sys.argv = [str(Path(__file__)), '--cpu', args.cpu, '--work', str(args.work), '--output', str(args.output)]
        singbox.main()

    channel = json.loads((Path(__file__).parent/'core-updates.json').read_text())
    arch = {'x86_64':'amd64', 'arm64':'arm64'}[args.cpu]
    asset = channel['engines'][args.engine]['assets'][arch]
    if hashlib.sha256((args.output/asset['archive']).read_bytes()).hexdigest() != asset['sha256']:
        raise ValueError('Rebuilt core differs from the reviewed update catalog')

if __name__ == '__main__':
    main()
