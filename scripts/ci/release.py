#!/usr/bin/env python3
"""Version metadata and an explicit, checked list of public release assets."""
import argparse
import hashlib
import os
from pathlib import Path
import re
import shutil

ROOT = Path(__file__).resolve().parents[2]
SEMVER = r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?'
DESKTOP = ('macos_arm64', 'macos_x64', 'windows_x64', 'windows_arm64', 'linux_x64', 'linux_arm64')


def desktop_suffixes(target):
    if target.startswith('macos_'):
        return ('.dmg',)
    if target.startswith('windows_'):
        return ('-setup.exe',)
    if target.startswith('linux_'):
        return ('.deb', '.pkg.tar.gz', '.AppImage')
    raise SystemExit(f'Unsupported desktop target: {target}')


def desktop_assets(v):
    return [f'reignhub-{v}-{t.replace("_", "-")}{suffix}'
            for t in DESKTOP for suffix in desktop_suffixes(t)]


def server_assets(v):
    return [f'reignhub-server-{v}-{o}-{a}' + ('.zip' if o == 'windows' else '.tar.gz')
                for o in ('linux', 'darwin', 'windows') for a in ('amd64', 'arm64')]


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def checksum(path):
    path.with_name(path.name + '.sha256').write_text(f'{digest(path)}  {path.name}\n')


def version(value):
    if not re.fullmatch(SEMVER, value):
        raise SystemExit(f'Invalid version: {value!r}; expected X.Y.Z or X.Y.Z-prerelease')
    return value


def metadata(args):
    config = (ROOT / 'clients/nsgo/nsgo.yaml').read_text()
    base = re.search(r'^version:\s*([0-9.]+)\s*$', config, re.M).group(1)
    ref = os.environ.get('GITHUB_REF', '')
    run = os.environ.get('GITHUB_RUN_NUMBER', '1')
    v = version(ref.removeprefix('refs/tags/v')) if ref.startswith('refs/tags/') else version(base + '-dev.' + run)
    lines = f'version={v}\nbuild_number={int(run)}\n'
    if os.environ.get('GITHUB_OUTPUT'):
        with open(os.environ['GITHUB_OUTPUT'], 'a') as f:
            f.write(lines)
    print(lines, end='')


def collect(args):
    source, output = Path(args.source), Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    for target in args.targets.split(','):
        stem = f'reignhub-{args.version}-{target.replace("_", "-")}'
        bundle = source / f'ReignHub-{args.version}-{target}'
        if target in DESKTOP:
            for suffix in desktop_suffixes(target):
                src = source / 'releases' / (stem + suffix)
                if not src.is_file():
                    raise SystemExit(f'Missing native installer: {src}; enable NSGo installers and install required packaging tools')
                archive = output / src.name
                shutil.copy2(src, archive)
                checksum(archive)
                print(archive)
            continue
        elif target in ('android_arm64', 'ios'):
            suffix = {'android_arm64': '.apk', 'ios': '.ipa'}[target]
            src = source / 'releases' / (stem + suffix)
            if not src.is_file():
                raise SystemExit(f'Missing release file: {src}')
            if target == 'android_arm64' and not os.environ.get('REIGNHUB_ANDROID_STABLE_SIGNING'):
                stem += '-debug'
            if target == 'ios':
                stem += '-unsigned'
            archive = output / (stem + suffix)
            shutil.copy2(src, archive)
        elif target == 'ios_simulator':
            if not (bundle / 'ReignHub.app').is_dir():
                raise SystemExit('Missing iOS simulator application')
            continue  # A compile check, not a distributable device package.
        else:
            raise SystemExit(f'Unsupported target: {target}')
        checksum(archive)
        print(archive)


def verify(args):
    output = Path(args.output)
    expected = desktop_assets(args.version)
    expected.append(f'reignhub-{args.version}-ios-unsigned.ipa')
    android = f'reignhub-{args.version}-android-arm64'
    apks = [output / (android + suffix) for suffix in ('.apk', '-debug.apk')]
    apks = [path for path in apks if path.is_file()]
    if len(apks) != 1:
        raise SystemExit('Expected exactly one Android APK')
    expected += [apks[0].name]
    expected += server_assets(args.version)
    actual = {p.name for p in output.iterdir() if p.is_file() and p.suffix != '.sha256' and p.name != 'SHA256SUMS'}
    if actual != set(expected):
        raise SystemExit(f'Release assets differ: missing={set(expected) - actual}, unexpected={actual - set(expected)}')
    for name in sorted(expected):
        path = output / name
        if path.stat().st_size == 0:
            raise SystemExit(f'Empty asset: {name}')
        want = f'{digest(path)}  {name}\n'
        if not path.with_name(name + '.sha256').is_file() or path.with_name(name + '.sha256').read_text() != want:
            raise SystemExit(f'Checksum mismatch: {name}')
    (output / 'SHA256SUMS').write_text(''.join(f'{digest(output / name)}  {name}\n' for name in sorted(expected)))
    print(f'Verified {len(expected)} release assets')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    subs = p.add_subparsers(dest='command', required=True)
    subs.add_parser('metadata')
    c = subs.add_parser('collect')
    c.add_argument('--source', required=True)
    c.add_argument('--targets', required=True)
    c.add_argument('--version', required=True, type=version)
    c.add_argument('--output', required=True)
    v = subs.add_parser('verify')
    v.add_argument('--version', required=True, type=version)
    v.add_argument('--output', required=True)
    args = p.parse_args()
    {'metadata': metadata, 'collect': collect, 'verify': verify}[args.command](args)


if __name__ == '__main__':
    main()
