#!/bin/sh
# Install a published, checksum-verified server binary without root privileges.
set -eu
version=""
prefix="${REIGNHUB_INSTALL_PREFIX:-$HOME/.local/bin}"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) version="$2"; shift 2 ;;
    --prefix) prefix="$2"; shift 2 ;;
    *) echo 'Usage: install-server.sh [--version X.Y.Z] [--prefix BIN_DIR]' >&2; exit 2 ;;
  esac
done
case "$(uname -s)" in Linux) platform=linux ;; Darwin) platform=darwin ;; *) echo 'Use the Windows ZIP on Windows.' >&2; exit 1 ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo 'Unsupported CPU architecture.' >&2; exit 1 ;; esac
case "$version" in *[!0-9A-Za-z.-]*) echo 'Invalid release version.' >&2; exit 1 ;; esac
api=https://api.github.com/repos/monkeyWie/reignhub/releases
endpoint="$api/latest"
if [ -n "$version" ]; then endpoint="$api/tags/v$version"; fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
curl -fsSL "$endpoint" -o "$work/release.json"
version=$(python3 - "$work" "$platform" "$arch" "$version" <<'PYMETA'
import json, re, sys
from pathlib import Path

work, platform, arch, requested = sys.argv[1:]
release = json.loads((Path(work) / 'release.json').read_text())
tag = release['tag_name']
if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?', tag) or (requested and tag != 'v' + requested):
    sys.exit('Unexpected release tag')
name = f'reignhub-server-{tag[1:]}-{platform}-{arch}'
for asset in release['assets']:
    if asset['name'] == name + '.tar.gz' and re.fullmatch(r'sha256:[0-9a-f]{64}', asset.get('digest') or ''):
        (Path(work) / 'checksum').write_text(f"{asset['digest'][7:]}  {name}.tar.gz\n")
        print(tag[1:])
        break
else:
    sys.exit('GitHub returned no matching server archive with a SHA-256 digest')
PYMETA
)
name="reignhub-server-$version-$platform-$arch"
url="https://github.com/monkeyWie/reignhub/releases/download/v$version/$name.tar.gz"
curl -fL "$url" -o "$work/$name.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$work" && sha256sum -c checksum)
else
  (cd "$work" && shasum -a 256 -c checksum)
fi
tar -xzf "$work/$name.tar.gz" -C "$work"
mkdir -p "$prefix"
install -m 755 "$work/$name/reignhub-server" "$prefix/reignhub-server"
"$prefix/reignhub-server" --version
printf '\nInstalled to %s. Start with: %s/reignhub-server\n' "$prefix" "$prefix"
printf 'In your service directory, run reignhub-server --init once to create config.json, edit it, then run reignhub-server.\n'
