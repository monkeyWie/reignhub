# Self-hosted ReignHub

## Standalone Go deployment

Go and Cloudflare Worker are independent deployment options with the same `/p2p/*` contract. The Go service does not need Cloudflare, an account service, Node at runtime or an external database. Both deployments include only the Web console and redirect `/` to `/chat/`; the official website/docs are built and published separately.

```sh
docker compose -f deploy/service/compose.yaml up -d --build
# Or extract a standalone release and run:
./reignhub-server
```

All features are enabled by default: device registration, pairing, signaling, encrypted relay, Web, UDP STUN and TCP STUN. HTTP defaults to `:26330`; TCP/UDP discovery defaults to `:26331`. Native relay uses `:26332` on UDP and TCP. SQLite defaults to `data/reignhub.db`. Edit the bundled `config.json` and restart to change listeners or services. For a binary-only installation, create the template once with `--init`. Preserve the database and signing identity on upgrades.

For a Worker probe only, use `{"services":{"control":{"enable":false},"relay":{"enable":false}}}` as the Go configuration. This mode opens no HTTP listener and no SQLite database. TCP and UDP ports can differ. See [Go deployment, HTTPS and systemd](../../deploy/service/README.md).

Browser crypto requires HTTPS or localhost; LAN HTTP origins are suitable for native testing, not arbitrary remote browsers. STUN sockets require direct reachability and cannot be carried by an HTTP reverse proxy.

## One-click Cloudflare deployment

Use the root repository Deploy to Cloudflare button. The template needs the root pnpm workspace; Cloudflare's deploy button does not include sibling dependencies when given a monorepo subdirectory.

The template provides one Worker, static NSGo WASM console assets, one D1 database and one SQLite Durable Object class. D1 is automatically provisioned by Wrangler/Workers Builds; `pnpm run deploy` deploys the Worker, then applies the schema. Initial requests may briefly return unavailable before that first migration finishes. No paid-plan enforcement exists in this deployment. Cloudflare's own free-tier resource allowances still apply.

After deployment, paste the resulting `https://<worker>.<account>.workers.dev` origin into clients. The browser controller lives at `/chat`. The website and docs are excluded. No custom domain is required.

The one-click resource provisioning requires a real Cloudflare account and has not been exercised by local dry-run tests. The compiled Worker and D1 migrations are tested locally.

## Manual deploy

```sh
corepack enable
pnpm install --frozen-lockfile
pnpm build:web
pnpm exec wrangler login
pnpm run deploy
```

Wrangler provisions missing draft resources. Keep the assigned database ID in your deployment configuration for later releases. Back up D1 before future schema changes. D1 metadata includes the service signing identity; replacing it invalidates existing short leases until clients refresh their service configuration.

To restrict external Web origins, set `ALLOWED_ORIGINS` to a comma-separated list of exact origins. Requests from this Worker's own origin are permitted automatically. A malicious page cannot bypass device pairing by sending a token in the URL: tokens use Authorization or the WebSocket subprotocol.

For a local Worker, apply migrations with `--local` before `wrangler dev`. Do not point development clients at production state directories.

## Optional discovery probe

```sh
go build -o reignhub-server ./cmd/reignhub-server
# Set config.json to {"services":{"control":{"enable":false},"relay":{"enable":false}}} before starting.
./reignhub-server
```

Open `26331/TCP` and `26331/UDP` on the VM and firewall. Both protocols use the same port number. These are STUN Binding/probe listeners, **not HTTP endpoints**, so an Nginx HTTP reverse proxy cannot carry them. A Cloudflare DNS record for this probe must be **DNS only**, not proxied.

Set Worker variables:

```text
EXTERNAL_STUN=["probe.example.com:26331"]
```

All three nodes from the built-in list (Cloudflare, Google and Bilibili (`stun.chat.bilibili.com:3478`) over UDP) are advertised alongside the configured endpoints. Public and self-hosted candidates are gathered concurrently; it is not a waterfall that waits for public relay failure. TCP probes supply native TCP mappings and simultaneous-open candidates. A browser has no raw TCP socket API and only attempts WebRTC.

No probe means LAN/IPv6 and public UDP STUN still work. Native public TCP discovery benefits from this optional probe. Some NATs still cannot be traversed; neither TCP nor UDP hole punching guarantees a direct route. Self-hosted Worker WSS is the fallback.

## Add external native relays and STUN servers

Both open-source control services can advertise static nodes. On each external Go machine, enable the required services in the node's `config.json` and start the node. Run the second command in another shell from the same directory.

```json
{"services":{"control":{"enable":false}}}
```

```sh
./reignhub-server
./reignhub-server --print-relay-credential
```

Keep the node's state file on a persistent volume. Allow inbound TCP and UDP on 26331 for STUN and 26332 for native relay. The node credential contains the public TLS pin and the secret used to derive each device’s scoped relay membership key. Paired native devices authenticate directly to their assigned node after a restart. To revoke node usage for a device, add its device ID to `services.relay.revokedDevices` in config.json. The running node rereads this list every two seconds and closes matching connections. Clients need direct access to the node's IP or DNS-only hostname. A STUN-only node can use `{"services":{"control":{"enable":false},"relay":{"enable":false}}}` and needs only its advertised address.

**Go control service.** This configuration runs control and publishes an external STUN and native relay node. Replace the example address and credential, restrict read access to the file and restart the server. Local STUN and relay are disabled explicitly here.

```json
{
  "services": {
    "stun": { "enable": false },
    "relay": { "enable": false },
    "control": {
      "stunServers": ["203.0.113.10:26331"],
      "externalRelays": [
        { "addresses": ["203.0.113.10:26332","wss://relay.example.com"], "credential": "REPLACE_WITH_NODE_CREDENTIAL" }
      ]
    }
  }
}
```

**Cloudflare Worker control service.** Set `EXTERNAL_STUN` in Wrangler’s `vars` to `["203.0.113.10:26331"]`. Store the relay registration separately as a Worker **Secret**. Run `pnpm exec wrangler secret put EXTERNAL_RELAYS` and paste this JSON at the prompt, substituting the real address and credential.

```json
[{ "addresses": ["203.0.113.10:26332","wss://relay.example.com"], "credential": "REPLACE_WITH_NODE_CREDENTIAL" }]
```

List public and private STUN providers together. Entries extend the three built-in UDP nodes. Omit this list or use `[]` for zero additions. A Go control service also publishes its own enabled STUN listeners. Prefix a single-transport endpoint with `udp:` or `tcp:`. A bare address advertises both transports. Use brackets for IPv6, for example `tcp:[2001:db8::1]:26331`.

For example, a Go relay can listen on port 26332 internally and expose UDP 26332 and TCP 443 via NAT. Register `{"addresses":["udp:203.0.113.10:26332","tcp:203.0.113.10:443"],"credential":"REPLACE_WITH_NODE_CREDENTIAL"}` on either control. Protocol-specific relay addresses require native clients that support split endpoints. A shared entry applies to both transports.

Each control service supports up to 32 static external relays. With multiple entries, the host ID determines one node for both legs. Go includes its enabled local listener as one additional choice. A native connection can fall back to an enabled WebSocket relay when the selected node is unreachable. Browsers race WebRTC, the control WebSocket relay and the assigned external WSS endpoint. Forward HTTPS WebSocket upgrades to port 26330 on the Go node and use the public WSS origin in its address list. The independent node endpoint is available with `control.enable` set to either value. Check STUN entries with `GET /p2p/config` and look for `Relay · QUIC` or `Relay · TCP` on a native connection using the relay.

## Persistence and privacy

D1 stores device registration credentials as hashes, public keys, names, pairing relations, encrypted pending envelopes and a persistent service signing key. Pending pairing records are removed after expiry. The server does not store chat plaintext or attachment content. Paired control devices hold the keys needed to read messages; revoke a lost device at the controlled computer.

Browsers cache locally with IndexedDB (attachments limited to approximately 100 MiB). Native caches use SQLite and local attachment files. Clearing browser data requires pairing again; it does not by itself revoke the old device credential on the host.
