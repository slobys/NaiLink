"""Prepare reviewed core binaries in an isolated test directory, never /usr."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import tarfile

def prepare(catalog, archives, output, arch='amd64'):
    output.mkdir(parents=True, exist_ok=True, mode=0o700)
    output.chmod(0o700)
    for engine, release in catalog['engines'].items():
        if engine not in ('sing-box', 'mihomo'):
            raise ValueError('Unknown fixture engine')
        asset = release['assets'][arch]
        data = (archives/asset['archive']).read_bytes()
        if hashlib.sha256(data).hexdigest() != asset['sha256']:
            raise ValueError('Fixture archive checksum mismatch')
        with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as outer:
            payload = outer.extractfile('data.tar.gz').read(220 << 20)
        with tarfile.open(fileobj=io.BytesIO(payload), mode='r:gz') as inner:
            binary = inner.extractfile('./usr/bin/'+engine).read(200 << 20)
        path = output/engine
        path.write_bytes(binary)
        path.chmod(0o755)
        receipt = {'engine': engine, 'version': release['version'],
                   'sha256': hashlib.sha256(binary).hexdigest(),
                   'min_adapter': release['min_adapter'], 'max_adapter': release['max_adapter']}
        receipt_path = output/(engine+'.version.json')
        receipt_path.write_text(json.dumps(receipt))
        receipt_path.chmod(0o600)

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archives', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--arch', choices=('amd64','arm64'), default='amd64')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    prepare(json.loads((root/'openwrt/core-updates.json').read_text()), args.archives, args.output, args.arch)
