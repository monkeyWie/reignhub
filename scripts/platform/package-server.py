#!/usr/bin/env python3
"""Build standalone Go servers with the same chunked Web console as the Worker."""
import argparse
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts/ci'))
from release import checksum, version

TARGETS = tuple((os_name, arch) for os_name in ('linux', 'darwin', 'windows') for arch in ('amd64', 'arm64'))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', required=True, type=version)
    parser.add_argument('--output', type=Path, default=ROOT / 'dist/release')
    parser.add_argument('--web-dir', type=Path, help='built assets/chat directory; otherwise build a fresh console')
    parser.add_argument('--targets', default=','.join(f'{o}/{a}' for o, a in TARGETS))
    args = parser.parse_args()
    targets = [tuple(t.split('/')) for t in args.targets.split(',')]
    if any(t not in TARGETS for t in targets) or len(set(targets)) != len(targets):
        parser.error('targets must be unique linux/darwin/windows + amd64/arm64 pairs')
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if args.web_dir:
        web = args.web_dir.resolve()
    else:
        subprocess.run(['node', 'scripts/platform/build-console.mjs', '--self-hosted'], cwd=ROOT, env={**os.environ, 'VERSION': args.version}, check=True)
        web = ROOT / 'build/self-hosted-assets/assets/chat'
    manifest = json.loads((web / 'nsgo.json').read_text())
    required = ['index.html', 'nsgo.json', manifest['loader'], manifest['exec'], manifest['wasm']]
    required += [p['file'] for p in manifest['wasmParts']]
    if not manifest['wasmParts'] or any(not (web / name).is_file() for name in required):
        raise SystemExit('Expected the optimized console with a complete WASM part manifest')
    # Never embed an arbitrary tree: only the known console manifest/assets enter the binary.
    allowed = set(required) | {p.name for p in web.iterdir() if p.is_file() and re.fullmatch(r'[a-zA-Z0-9_-]+\.[0-9a-f]{10}\.(?:js|mjs|css|woff2)', p.name)}
    if any(Path(name).name != name or (web / name).is_symlink() for name in allowed):
        raise SystemExit('Console assets must be plain files with flat filenames')
    with tempfile.TemporaryDirectory(prefix='reignhub-server-build-') as temp:
        stage = Path(temp) / 'source'
        stage.mkdir()
        for name in ('go.mod', 'go.sum'):
            shutil.copy2(ROOT / name, stage / name)
        for name in ('internal', 'pkg', 'cmd/reignhub-server'):
            shutil.copytree(ROOT / name, stage / name)
        assets = stage / 'internal/webui/assets'
        shutil.rmtree(assets)
        assets.mkdir()
        for name in allowed:
            shutil.copy2(web / name, assets / name)
        html = (assets / 'index.html').read_text().replace('href="/icon.svg"', 'href="/assets/chat/icon.svg"')
        (assets / 'index.html').write_text(html)
        shutil.copy2(ROOT / 'apps/site/app/icon.svg', assets / 'icon.svg')
        for os_name, arch in targets:
            name = f'reignhub-server-{args.version}-{os_name}-{arch}'
            bundle = Path(temp) / name
            bundle.mkdir()
            binary = 'reignhub-server' + ('.exe' if os_name == 'windows' else '')
            subprocess.run(['go', 'build', '-trimpath', f'-ldflags=-s -w -X github.com/monkeyWie/reignhub/internal/servercmd.Version={args.version}', '-o', str(bundle / binary), './cmd/reignhub-server'], cwd=stage,
                           env={**os.environ, 'GOWORK': 'off', 'GOOS': os_name, 'GOARCH': arch, 'CGO_ENABLED': '0'}, check=True)
            shutil.copy2(ROOT / 'deploy/service/config.json', bundle / 'config.json')
            archive = output / (name + ('.zip' if os_name == 'windows' else '.tar.gz'))
            if os_name == 'windows':
                with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED) as zip_file:
                    for file in bundle.iterdir():
                        zip_file.write(file, arcname=f'{name}/{file.name}')
            else:
                with tarfile.open(archive, 'w:gz') as tar:
                    tar.add(bundle, arcname=name)
            checksum(archive)
            print(f'{archive} ({archive.stat().st_size / 1048576:.1f} MiB)', flush=True)


if __name__ == '__main__':
    main()
