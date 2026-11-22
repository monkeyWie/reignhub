# Go and Cloudflare Worker parity

## One contract, two implementations

Go and Worker must preserve the same client-visible public behavior. Internal changes need not edit both. Public contract changes require inspecting both and updating affected code, or explaining why the other implementation already satisfies the contract.

| Capability               | Go                                             | Worker                                         | Shared requirement                                           |
| ------------------------ | ---------------------------------------------- | ---------------------------------------------- | ------------------------------------------------------------ |
| HTTP/config              | `internal/servercmd`, `internal/deviceservice` | `apps/self-hosted`, `worker-core/src/index.ts` | Discovery and service identity                               |
| Devices/pairing/presence | `internal/deviceservice`                       | `worker-core/src/index.ts`, `schema.ts`        | Identity, authorization, availability, revocation            |
| Signaling/relay          | `internal/relay`                               | `worker-core/src/room.ts`                      | Authentication, peer allocation, frames, renewal, disconnect |
| Signed grants            | `pkg/access`                                   | `worker-core/src/crypto.ts`                    | Ed25519 payloads, namespace, expiry, permissions             |
| Web console              | `internal/webui`                               | `worker-core/src/console.ts`                   | The same NSGo assets, entry and cache semantics              |
| Storage                  | SQLite/GORM                                    | D1/Drizzle                                     | Durable identity, isolation, idempotency; layouts may differ |
| TCP/UDP probes           | Selectable in the Go server                    | Advertised external endpoints                  | Discovered from the configured service origin                |

Both control services accept arrays of public address strings. A bare `host:port` serves both transports. The prefixes `udp:` and `tcp:` restrict an address to one transport. Go’s `services.control.stunServers` and Worker’s `EXTERNAL_STUN` each take one array for all discovery providers. Both services always advertise the built-in Cloudflare, Google and Bilibili (`stun.chat.bilibili.com:3478`) UDP nodes. An omitted or empty array adds no endpoints. Duplicate UDP entries are advertised once. Go also advertises its enabled local STUN listeners. Each external relay contains an `addresses` array and its private registration `credential`. Discovery translates STUN addresses to the existing client fields.

Both controls accept a public WSS origin in each relay’s address array and return its node endpoint to the paired controller. The Go relay serves `/p2p/node` on the HTTP listener even when local control is disabled. Both controls implement role-bound route enrollment at `POST /p2p/routes` after pairing. The device proves membership to the pinned Go relay independently of its signal socket. Go can assign its local listener or an external node. Worker assigns a configured external Go node. Self-hosted membership persists across restarts. Operator-managed nodes verify a signed relay grant on each transport leg. The optional signed HTTPS admission endpoint shares concurrent relay limits across nodes. Self-hosted nodes and P2P require no commercial admission. Manual device approvals persist until revoked by the host. Pending approval requests and managed relay leases have separate lifetimes. Hosted native sign-in uses authenticated device email challenge and verification endpoints. The account-enabled Worker owns email delivery, OAuth, and per-plan limits. The public Go and Worker controls require no account. The [wire contract](../platform/protocol.md) covers independent registration, revocation and path selection.

Public TS types live in [protocol](../../packages/protocol/src/index.ts); Go counterparts live in `internal/device`, `internal/connectivity`, `internal/relay` and `pkg/access`. They are not generated from each other. Review them manually and verify interoperability.

Encrypted RPC is an endpoint contract defined by `internal/control`, `internal/transport` and Provider models. Services forward opaque frames; an RPC version change does not inherently require service-side business parsing.

## Required alignment

- Routes, methods, types, defaults, units, omitted/null/empty meanings and capability discovery. An empty Agent allow list and unrestricted access are different.
- HTTP status, machine-readable failures, authorization and idempotency results. Clients must not depend on one runtime's human-readable error text.
- Host/controller roles, exact origins, Bearer or browser WebSocket subprotocol authentication. No credentials in URLs; browsers cannot declare themselves hosts.
- Pairing consent/local verification, authenticated ownership, read scope and revocation. Caller-supplied identity is not authentication.
- Durable service signing identity and service-injected peer/grant data. Direct paths enforce device authorization at the host.
- Presence, unknown states, batch lookup, heartbeat, renewal and cleanup. A failed lookup is not confirmed offline.
- Self-hosted `/` redirects to `/chat/`; existing 302/307 differences are acceptable. Packages exclude marketing, docs and release-list routes.
- Input/frame limits, concurrency/connection budgets, expiry and cleanup. Document infrastructure-specific limits separately from optional commercial policy.

Full fields and frames belong in [the protocol](../platform/protocol.md). Advertise capabilities through the contract rather than branch product clients on the implementation language.

## Change and verification workflow

1. Describe the external trigger, request, response, failure and supported scope. Distinguish service, encrypted RPC and Agent protocol versions.
2. Inspect corresponding routes, types, authentication, persistence and recovery in both implementations. Mark runtime-specific features explicitly.
3. Update affected implementations, clients, types and documentation. Explain a no-edit result with evidence.
4. Cover success, rejection and recovery; startup success alone does not cover recovery after missing trust configuration.
5. Exercise both deployments with the same client/isolated host and report results. Verify packaged console assets when packaging changes.

Go tests cover `internal/deviceservice`, `internal/relay`, `internal/servercmd` and `pkg/access`; Worker tests live in `packages/worker-core/test`. The shared [smoke entry](../../scripts/platform/smoke-worker.mts) runs against Worker, Go packages and Go containers in CI.

That smoke is not a complete reusable endpoint/failure conformance suite. Add changed scenarios to affected tests or the common entry; do not claim existing coverage proves new behavior. Platform sleep, real weak networks and vendor integration need separate evidence. See [validation](validation.md).

```text
Public contract: changed / unchanged, with reason
Go: changes or evidence existing behavior satisfies the contract
Worker: changes or evidence existing behavior satisfies the contract
Clients, types, docs: affected scope
Verification: scenarios and outcomes against each deployment
Limits: unverified platforms or failure conditions
```
