# ReignHub

One workspace for your agents. Organize Codex, Claude Code and Pi sessions by project, open a terminal on your desktop and continue conversations from your phone or browser. Desktop, mobile and browser controllers share one NSGo + Go UI. The website and documentation use Next.js and Fumadocs. The default service is `https://reignhub.app`.

[![Deploy to Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/monkeyWie/reignhub)

## Website and documentation

The official site is [reignhub.app](https://reignhub.app), with [quick start](https://reignhub.app/docs/quick-start), [self-hosting](https://reignhub.app/docs/self-hosting), [technology](https://reignhub.app/technology) and [contribution guides](CONTRIBUTING.md). Documentation uses Fumadocs on Next.js; edit Markdown/MDX in `apps/site/content/docs` and each folder’s `meta.json` for sidebar order. Run `pnpm dev:site` for a content preview and `pnpm build:site` for the official website build. Website publication uses its own CI workflow and is excluded from self-hosted packages.

## Architecture

- **Two self-hosting options:** a standalone `reignhub-server` with SQLite and integrated TCP/UDP discovery, or a Cloudflare Worker with D1 and Durable Objects. Both use the same `/p2p/*` protocol.
- **A lean Worker** hosts `/chat` and `/p2p/*`; `/` redirects to `/chat/`. D1 stores device identities and encrypted pairing envelopes. Hibernating Durable Objects route signaling and encrypted relay frames.
- **Direct first:** native peers try QUIC/UDP and TCP in parallel. Browsers and older peers use WebRTC. Public and optional self-hosted discovery servers provide candidates concurrently. If direct connectivity fails, self-hosted clients use their own Go or Worker relay.
- **One service address:** clients discover optional probe endpoints from `/p2p/config`. No second service address is required in the UI.
- **End-to-end encryption:** pairing uses X25519/HKDF/AES-GCM; session traffic uses fresh directional AEAD keys and replay protection. Servers do not hold chat pairing keys or store chat plaintext.
- **Providers:** Codex has one integration for App, CLI and VS Code sessions. Existing sessions follow their current local IPC owner; ReignHub-created sessions use a private app-server. Claude Code, Cursor, OpenCode, Grok Build, Antigravity, Kiro, Muse Code, fx, DeepSeek Harness and Gemini CLI use a common ACP adapter; Pi uses its native RPC. Custom ACP commands are configured on the host. Local Agent tasks continue independently of remote connection authorization.
- **Caching:** native clients use SQLite. Browsers use IndexedDB for paired identities, snapshots and bounded attachment caching.

The open-source deployment has no account, subscription, or commercial device limits. Our hosted service composes this same public core with private identity, payment and entitlement policies. Cloudflare's own infrastructure limits and charges still apply to your deployment.

## Development

Read [AGENTS.md](AGENTS.md) and the [engineering knowledge base](docs/engineering/README.md) before changing the implementation. The knowledge base covers frontend design, backend architecture, code standards, validation and troubleshooting. Public service changes follow the [Go/Worker parity contract](docs/engineering/service-parity.md): one protocol, two implementations, with both sides reviewed and validated.

Requires Go (see `go.mod`), Node 24 and pnpm 10.34.5.

```sh
pnpm install
pnpm check
pnpm test
pnpm build:web
pnpm exec wrangler d1 migrations apply DB --local
pnpm exec wrangler dev --port 8787
```

Open `http://localhost:8787/chat`. Browser cryptography and WebRTC require a secure context: HTTPS or localhost. Use the generated `workers.dev` HTTPS address for other computers and phones.

Native and Web builds use the root entry point (which selects the NSGo module automatically):

```sh
./scripts/build.sh macos_arm64
./scripts/build.sh android_arm64,web -output dist/local
```

Default artifacts go to root `dist/`, with distributable archives in `dist/releases/`. Explicit relative output paths are resolved from the caller's working directory.

All controller UI development lives in [`clients/nsgo`](clients/nsgo). NSGo maps this shared UI to native views on desktop/mobile and Go WebAssembly + DOM in browsers. `/chat` serves a standalone HTML shell and compressed, content-addressed NSGo assets.

## Self-hosting

The button above deploys the **whole repository**, not an isolated monorepo subdirectory. Cloudflare provisions the D1 database and SQLite Durable Object. The deploy command applies the D1 schema after provisioning. No OAuth applications, payment configuration, domain or external VM are required. Set every client to the resulting service origin.

For manual deployment, run `pnpm build:web`, `pnpm exec wrangler login`, then `pnpm run deploy`. See [both deployment options](docs/platform/self-hosting.md).

Alternatively, deploy the standalone Go server with all features enabled:

```sh
docker compose -f deploy/service/compose.yaml up -d --build
# Or extract a reignhub-server release archive and run ./reignhub-server
```

Go uses local SQLite, embeds `/chat`, and listens on HTTP 26330 plus TCP/UDP discovery 26331 and native QUIC/TLS relay 26332. All addresses and features are configured in `config.json`. It has no Cloudflare dependency. Both Go and Worker roots redirect to `/chat/`. Neither self-hosted package includes the marketing website or docs. See [Go configuration and installation](deploy/service/README.md).

For a Worker deployment, the same server can run only TCP/UDP discovery (no HTTP or SQLite):

```sh
go build -o reignhub-server ./cmd/reignhub-server
# Set config.json to {"services":{"control":{"enable":false},"relay":{"enable":false}}} before starting.
./reignhub-server
```

Expose **26331/TCP and 26331/UDP** directly. Probe mode starts only the selected STUN listeners. No TURN, DERP, cloudflared or Tailcat is required.

## Source map

| Directory              | Responsibility                                                  |
| ---------------------- | --------------------------------------------------------------- |
| `apps/site`            | Official website/docs, built and published separately           |
| `apps/self-hosted`     | Console-only open-source Worker                                 |
| `packages/account-ui`  | Website account and administration pages                        |
| `packages/client-sdk`  | Pairing, encrypted RPC, WebRTC and IndexedDB                    |
| `packages/protocol`    | Current `/p2p/*` protocol and policy interfaces                 |
| `packages/worker-core` | D1 directory/pairing, signed leases and hibernating rooms       |
| `clients/nsgo`         | Shared desktop, Android, iOS and browser UI                     |
| `internal`             | Native Go application, Providers, transport and SQLite          |
| `cmd/reignhub-server`  | Standalone SQLite/Web/relay/discovery server                    |
| `docs/engineering`     | Engineering standards, decisions and troubleshooting knowledge  |
| `.github/workflows`    | Native, Web, Worker and Go server checks/builds/releases |

Protocol compatibility with older prototypes is intentionally not provided. See [implementation scope](docs/platform/implementation.md) and [protocol](docs/platform/protocol.md). Current engineering references are indexed in [the knowledge base](docs/engineering/README.md).
