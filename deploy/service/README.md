# Standalone ReignHub server

`reignhub-server` provides the device directory, encrypted pairing, signaling, encrypted QUIC, TLS/TCP and WebSocket relay, Web console and TCP/UDP STUN discovery in one pure-Go binary. It uses local SQLite and needs no Cloudflare account or external database. All features start by default. The server never runs Agents or stores chat plaintext; Agents continue running on controlled computers.

## One-command Docker deployment

From the repository root:

```sh
docker compose -f deploy/service/compose.yaml up -d --build
```

Compose mounts `./config.json` at `/config.json` in the container. Edit the file beside `compose.yaml` and restart the service after changes. The default relative state path resolves to `/data/reignhub.db` in the persistent volume. The image also includes this config for standalone Docker runs.

HTTP listens on 26330/TCP. Expose discovery on 26331/UDP and TCP and native relay on 26332/UDP and TCP. Compose's `REIGNHUB_HTTP_PORT`, `REIGNHUB_TCP_PORT`, `REIGNHUB_UDP_PORT` and `REIGNHUB_NATIVE_PORT` change the **published Docker ports**. Set `REIGNHUB_NATIVE_UDP_PORT` and `REIGNHUB_NATIVE_TCP_PORT` when the public relay ports differ. Behind NAT or an HTTP proxy, set `services.stun.publicAddress` and `services.relay.native.publicAddress` to directly reachable advertised endpoints. Paired native clients obtain the public pin and scoped node credentials from `POST /p2p/routes`. To deny a device access to this node, add its 32-character device ID to `services.relay.revokedDevices` in config.json. The running node rereads this list every two seconds and closes matching connections. Use HTTPS/WSS for public signaling.

After the first start, use the same config to print the secret node credential while the server is running or stopped. It contains the public TLS pin and the key used to authorize connections. Store it privately. Keeping the SQLite database preserves it across restarts.

```sh
./reignhub-server --print-relay-credential
# With the supplied Docker Compose deployment, from the repository root
docker compose -f deploy/service/compose.yaml exec -T reignhub-server /reignhub-server --print-relay-credential
```

## Add an external node

On the external Go node, edit `config.json` and start `./reignhub-server`. This example enables both discovery protocols and starts a standalone native relay.

```json
{
  "services": { "control": { "enable": false } }
}
```

Read its node credential with `./reignhub-server --print-relay-credential`. Privately register its address and credential with the control service. See the [Go and Worker configuration examples](../../docs/platform/self-hosting.md#add-external-native-relays-and-stun-servers).

## Standalone binary

Extract a release archive for Linux, macOS or Windows (amd64/arm64), verify its SHA-256 against the digest on GitHub Releases, then run:

```sh
./reignhub-server
# Windows: .\reignhub-server.exe
```

The release binary embeds the optimized, chunked WASM console. Visit `/chat/`; `/` redirects there. Both Go and Worker self-hosted packages include only the control console; the official website/docs are published separately. Native clients use the service origin, e.g. `http://192.168.1.10:26330` for LAN testing. Browser cryptography/WebRTC needs HTTPS or localhost; remote/LAN browsers need an HTTPS endpoint. Check `/healthz` and `/p2p/config`.

Unix installation with curl and Python 3:

```sh
sh scripts/platform/install-server.sh --version X.Y.Z
# Optional: --prefix /usr/local/bin
```

The installer downloads the matching archive, verifies SHA-256 and installs to `~/.local/bin` by default. It does not configure a background service. Copy the systemd unit from this source directory for Linux.

## Configuration and ports

The server loads `config.json` from its working directory. Release archives include a ready-to-edit file. For a binary-only install, run `./reignhub-server --init` once. It creates the template exclusively and exits. To use another file, supply `--config PATH` to startup, initialization or the credential command. Database, Web directory and TLS file paths resolve relative to the configuration file's directory. Keep the existing SQLite path when upgrading.

The supplied config starts all three services. Edit the file and restart the process. An omitted group defaults to enabled. Set `enable` to `false` on any group to disable it. When upgrading from RC.2, explicitly disable every group or transport you previously disabled by omission.

```json
{
  "services": {
    "control": { "enable": true },
    "stun": { "enable": true },
    "relay": { "enable": true }
  }
}
```

| Group | Defaults and options |
| --- | --- |
| `control` | Device API, pairing, signaling and Web console on `:26330`. Set `listen`, `database`, `allowedOrigins`, `webDir` or `tls` as needed. |
| `stun` | UDP and TCP on `:26331`. Set `listen` or `publicAddress` once to share them. Each of `udp` and `tcp` can override the shared address or set `enable: false`. |
| `relay` | With local control, an empty group enables WebSocket plus native QUIC and TLS/TCP on `:26332`. Set `websocket` to false or set `native.enable` to false to disable a transport. With control disabled, the shared HTTP listener still serves `/p2p/node` and `/healthz`. Proxy WSS upgrades to that listener (default port 26330). A standalone relay accepts an optional `database` path for its persistent identity. |

For UDP discovery alone use `{"services":{"control":{"enable":false},"stun":{"listen":"0.0.0.0:3478","tcp":{"enable":false}},"relay":{"enable":false}}}`. Listener addresses can specify a network interface, for example `127.0.0.1:26330` or `[::1]:26330`.

Use a single list for public and self-hosted STUN. Set `services.control.stunServers` on Go or `EXTERNAL_STUN` on Worker. Both services always advertise the built-in Cloudflare, Google and Bilibili (`stun.chat.bilibili.com:3478`) UDP nodes. Omit the array or set it to `[]` to add no other nodes. Go additionally advertises its own active STUN listeners. A shared `host:port` enables both transports. Prefix with `udp:` or `tcp:` when a node supports just one. Include a `wss://relay.example.com` origin in the node’s address array to enable browsers. Register a relay under `services.control.externalRelays` on Go or `EXTERNAL_RELAYS` on Worker.

## HTTPS and service management

Use the Nginx or Caddy example to terminate HTTPS/WSS, or set `certFile` and `keyFile` in `services.control.tls` in the JSON. Preserve WebSocket upgrades and the original Host. HTTP reverse proxies must not forward STUN. When DNS is proxied through Cloudflare, use a separate **DNS only** hostname for discovery. For TLS behind a proxy, listen on loopback/private interfaces and expose the proxy publicly.

For systemd, extract under `/opt/reignhub`, create `/etc/reignhub/config.json` containing at least `{"services":{"control":{"database":"/var/lib/reignhub/reignhub.db"},"stun":{},"relay":{}}}`, install `reignhub-server.service` to `/etc/systemd/system/`, then run:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now reignhub-server
```

The unit uses a dynamic unprivileged user and `/var/lib/reignhub` for state. Stop the service before copying the SQLite database for backups (including WAL/SHM if present); alternatively use SQLite's backup command. Preserve the database when upgrading: it contains service identity/signing keys, token hashes, paired permissions and encrypted pending requests. Replacing it changes the service identity.

## Building from source

```sh
# Optimized Web console and six server archives
python3 scripts/platform/package-server.py --version X.Y.Z
# Server development without embedded Web
# Set "services.control.webDir" in config.json to the built console directory.
go run ./cmd/reignhub-server
```

A source-only `go install github.com/monkeyWie/reignhub/cmd/reignhub-server@VERSION` works for API/probes, but Web assets are embedded only by the release builder; set `services.control.webDir` to a built console directory. Compilation uses `CGO_ENABLED=0`, including SQLite; no native SQLite library is needed.
