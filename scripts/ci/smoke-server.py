#!/usr/bin/env python3
"""Exercise a packaged server with temporary data and ephemeral listeners."""
import argparse
import json
import os
import struct
from pathlib import Path
import socket
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request


def check_probes(tcp_host, tcp_port, udp_host, udp_port):
    request = struct.pack('!HHI', 1, 0, 0x2112a442) + os.urandom(12)
    with socket.create_connection((tcp_host, int(tcp_port)), timeout=2) as tcp:
        tcp.sendall(request)
        response = b''
        while len(response) < 20:
            chunk = tcp.recv(1024)
            assert chunk, 'STUN closed before a full response'
            response += chunk
        assert response[:2] == b'\x01\x01' and response[8:20] == request[8:20]
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as udp:
        udp.settimeout(2)
        udp.sendto(request, (udp_host, int(udp_port)))
        response, _ = udp.recvfrom(1024)
        assert response[:2] == b'\x01\x01' and response[8:20] == request[8:20]


def check_probe_mode(executable, work):
    work = work / 'probe-only'
    work.mkdir()
    with socket.socket() as free:
        free.bind(('127.0.0.1', 0))
        port = free.getsockname()[1]
    file = work / 'config.json'
    file.write_text(json.dumps({'services': {'control': {'enable': False}, 'relay': {'enable': False}, 'stun': {'listen': f'127.0.0.1:{port}'}}}))
    process = subprocess.Popen([str(executable)], cwd=work)
    try:
        deadline = time.monotonic() + 10
        while True:
            try:
                check_probes('127.0.0.1', port, '127.0.0.1', port)
                break
            except ConnectionRefusedError:
                if process.poll() is not None or time.monotonic() >= deadline:
                    raise
                time.sleep(0.05)
        assert not (work / 'data').exists()
        print('Packaged server: TCP and UDP STUN passed in probe-only mode')
    finally:
        process.terminate()
        process.wait(timeout=5)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', required=True, type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='reignhub-server-smoke-') as temp:
        work = Path(temp)
        with tarfile.open(args.archive) as tar:
            binaries = [m for m in tar.getmembers() if m.isfile() and Path(m.name).name == 'reignhub-server']
            if len(binaries) != 1:
                raise SystemExit('Expected one packaged server binary')
            config_member = str(Path(binaries[0].name).parent / 'config.json')
            assert sorted(m.name for m in tar if not m.isdir()) == sorted([binaries[0].name, config_member])
            bundled_config = json.load(tar.extractfile(config_member))
            executable = work / 'reignhub-server'
            executable.write_bytes(tar.extractfile(binaries[0]).read())
            executable.chmod(0o755)
        # A binary-only install can initialize explicitly. Reinitializing
        # must preserve operator edits and must never create a node identity.
        subprocess.run([str(executable), '--init'], cwd=work, check=True)
        config = work / 'config.json'
        initial = config.read_bytes()
        assert json.loads(initial) == bundled_config, 'archive and --init defaults differ'
        assert bundled_config == {'services': {group: {'enable': True} for group in ('control', 'stun', 'relay')}}
        assert subprocess.run([str(executable), '--init'], cwd=work).returncode != 0
        assert config.read_bytes() == initial and not (work / 'data').exists()
        check_probe_mode(executable, work)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        base = f'http://127.0.0.1:{port}'
        with (work / 'server.log').open('w+') as log:
            cfg = json.loads(initial)
            services = cfg['services']
            services['control'].update(listen=f'127.0.0.1:{port}', database='state/reignhub.db')
            services['stun']['listen'] = '127.0.0.1:0'
            services['relay'].update(websocket=True, native={'listen': '127.0.0.1:0'})
            config.write_text(json.dumps(cfg))
            process = subprocess.Popen([str(executable)], cwd=work, stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 20
                while True:
                    try:
                        with urllib.request.urlopen(base + '/healthz', timeout=1) as r:
                            assert r.read() == b'ok'
                        break
                    except (OSError, urllib.error.URLError):
                        if process.poll() is not None or time.monotonic() >= deadline:
                            log.seek(0)
                            raise SystemExit('Server failed to start: ' + log.read())
                        time.sleep(0.1)
                with urllib.request.urlopen(base + '/chat/', timeout=3) as r:
                    assert b'ReignHub' in r.read()
                with urllib.request.urlopen(base + '/assets/chat/nsgo.json', timeout=3) as r:
                    manifest = json.load(r)
                for name in ('wasm', 'exec', 'loader'):
                    req = urllib.request.Request(base + '/assets/chat/' + manifest[name], method='HEAD')
                    with urllib.request.urlopen(req, timeout=3) as r:
                        assert r.status == 200 and int(r.headers['Content-Length']) > 0
                import gzip, hashlib
                chunks = []
                for part in manifest['wasmParts']:
                    with urllib.request.urlopen(base + '/assets/chat/' + part['file'], timeout=3) as r:
                        chunk = r.read()
                        assert len(chunk) == part['size']
                        assert 'Content-Encoding' not in r.headers
                        assert 'immutable' in r.headers['Cache-Control']
                        chunks.append(chunk)
                compressed = b''.join(chunks)
                assert len(compressed) == manifest['wasmSize']
                assert gzip.decompress(compressed).startswith(b'\x00asm')
                with urllib.request.urlopen(base + '/assets/chat/' + manifest['wasm'], timeout=3) as r:
                    assert hashlib.sha256(r.read()).digest() == hashlib.sha256(compressed).digest()
                req = urllib.request.Request(base + '/p2p/config', data=b'{}', headers={'Content-Type': 'application/json'})
                with urllib.request.urlopen(req, timeout=3) as r:
                    network = json.load(r)
                    assert network['protocol'] == 1 and network['selfStun'] and network['tcpStun']
                tcp_host, tcp_port = network['tcpStun'][0].rsplit(':', 1)
                udp_host, udp_port = network['selfStun'][0].removeprefix('stun:').rsplit(':', 1)
                check_probes(tcp_host, tcp_port, udp_host, udp_port)
                assert (work / 'state/reignhub.db').read_bytes()[:16] == b'SQLite format 3\x00'
                credential = subprocess.check_output([str(executable), '--print-relay-credential', '--config', str(config)], text=True).strip()
                assert credential.startswith("rh1.") and [len(part) for part in credential.split(".")[1:]] == [43, 43]
                print('Packaged server: HTTP, embedded Web, SQLite and STUN discovery passed')
            finally:
                process.terminate()
                try:
                    process.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == '__main__':
    main()
