# Current connection protocol

There is no `/v1` or `/v2` fallback. All connectivity endpoints share `/p2p/`.

| Endpoint                | Purpose                                                                        |
| ----------------------- | ------------------------------------------------------------------------------ |
| `/p2p/config`           | Service identity, lease public key, STUN/TCP discovery and login capability    |
| `/p2p/devices/register` | Register a public key and device kind; return a credential and connection code |
| `/p2p/devices/presence` | Update name and coarsened last-seen                                            |
| `/p2p/devices/resolve`  | Resolve connection code; online state is authoritative socket state            |
| `/p2p/pairings/request` | Submit an encrypted pairing intent                                             |
| `/p2p/pairings/pending` | Host reads pending intents                                                     |
| `/p2p/pairings/result`  | Controller reads its own result                                                |
| `/p2p/pairings/respond` | Host consents or denies; encrypted reply contains pairing key                  |
| `/p2p/pairings/revoke`  | Host revokes the pairing and closes controller links                           |
| `/p2p/routes`           | Authenticated role-bound route enrollment and policy refresh                       |
| `/p2p/signal`           | WebSocket handshake, signaling and lease renewal only                          |
| `/p2p/node`            | Independent binary WebSocket rendezvous at the assigned Go node                |
| `/p2p/relay`            | Authorized write-side ingress for encrypted business frames                    |

JSON HTTP endpoints use POST (config also accepts GET). Identity tokens are opaque random bearer credentials. Browser WebSocket authentication uses subprotocols `reign.p2p` and `auth.<base64url(token)>`. Tokens are never placed in query strings.

The signal welcome assigns a peer, protocol `1`, `allowRelay` and a short signed grant. `allowRelay` permits the WebSocket data path. A sender attaches an authorized WebSocket relay socket with its signal peer. WebSocket business frames arriving on that relay are delivered to the recipient's signal socket, keeping a single read stream and peer namespace. Native relay connections carry data in both directions.

The room injects authenticated controller identity and signed grant into forwarded frames. Caller-supplied identity/grants are ignored. Signal sockets accept only hello, signal and renewal; business frames cannot be moved onto that free ingress. Signal size and rate are bounded. Idle heartbeat replies bypass Durable Object JavaScript; client-driven lease renewal wakes it only periodically.

The encrypted `network` response includes the host's optional `platform`
(Go OS name). Native and browser controllers retain it with the verified
pairing for offline display. Older hosts may omit this field.

Pairing uses X25519, HKDF-SHA256 salt `reign/pairing`, AES-256-GCM with per-message random nonce and domain-separated context. A PSK-authenticated hello exchanges fresh nonces; session keys bind PSK, peer and both nonces. Data has directional counter nonces, authenticated context, replay protection and bounded 4,096-byte chunk reassembly. Mutations are never transparently retried after uncertain delivery.

Lease format is `base64url(JSON).base64url(Ed25519 signature)` signed over `reign/grant/v1/<payload>`. Grants bind relay admission to the service namespace, host, controller, role, expiry and allowed scopes. The host checks its local device key at RPC dispatch and before returning results. Changes to relay entitlements affect only relay forwarding.

The hosted account service advertises login methods in the browser account response and in authenticated device status. Email codes create a verified account or sign in an existing one. Email/password signs in existing credentials. OAuth providers link to the same verified email identity. Native devices post to `/api/account/device-email/{send,verify,password}` or start a `/api/account/device-login/start` request naming a configured social provider. The system browser signs in and asks the user to confirm the device code. The initiating device polls with its original credential and request ID. Browser sessions stay in the browser. Self-hosted services are account-free.

An account-enabled control may return an opaque `accountBinding` from authenticated device resolution. Both endpoints opt in with `accountAccess: true`. An authenticated host presence response lists its account-authorized controllers and their public keys. Each endpoint derives its connection key locally from X25519 and HKDF with context `reignhub/account/link/<binding>`. The host acknowledges installation before the first handshake. The control pushes removals to the host and a successful presence response reconciles missed notifications. Hosts retain account keys in memory and restore them only after reconciling membership. Manual pairing remains independently revocable. A host isolated from control applies remote revocation when it next receives a notification or reconciliation.

Both native peers advertise the `native` and `quic` capabilities in the encrypted hello. The host responds with their intersection. Native direct paths prefer QUIC over a UDP ICE candidate pair, then authenticated TCP. ICE credentials and candidates travel in encrypted signals. Direct QUIC uses mutually pinned TLS 1.3 identities derived from the fresh pairing session. Peers without native negotiation retain WebRTC and authorized WebSocket relay. A healthy direct session can survive signaling loss while its authorization remains valid.

A paired native controller races saved direct addresses, its assigned Go node and online signaling. It verifies the pairing secret and completes an encrypted network RPC. An authenticated `peer.select` request binds the chosen peer to an increasing generation in the logical connection carried by the hello. New requests use the selected peer. Pending replies drain on the original peer. Relay-only mode omits direct candidates. Browsers race the control WebSocket relay, the assigned Go WSS relay and WebRTC negotiation through either reachable service. Paired browsers store their own enrollment in IndexedDB. The first verified peer serves requests immediately. Healthy P2P peers take precedence. Peers in the same class compete on smoothed end-to-end network RPC time, with a switching margin of 20% and at least 12 ms. Two failed health probes allow a healthy relay to take over. Probes run on path changes, timeouts and periodic active use.

Go and Worker accept authenticated `POST /p2p/routes` with the target host ID. The reply includes a scoped connection grant and, when permitted, a pinned node endpoint and a membership key bound to service namespace, operator issuer, host, device and role. Only control and node hold the parent credential. A private self-hosted grant carries the `offline` scope and zero expiry. Access still requires the current host pairing secret on every connection. The host checks local authorization at dispatch and before sending results. Local revocation disconnects the controller and persists its pairing generation for conditional notification when control returns.

The Go node listens for QUIC and TLS/TCP using ALPN `reign-native/2`. A host registers once per assigned node. New admissions prove possession against a fresh node challenge. The node delivers controller invitations to that registration and pairs each controller with an independently authenticated host leg. TCP, QUIC and WebSocket legs can be mixed. The Go listener accepts binary WebSocket records at `/p2p/node` with subprotocol `reign.node.v2`. A browser-origin socket can present only a controller role. On WSS the client verifies an Ed25519 signature covering its fresh challenge with the enrolled node pin before sending its membership proof. A reverse proxy may terminate public TLS. The service WebSocket bearer credential is scoped to its own origin. The node forwards opaque records. An operator-managed membership additionally pins the service grant issuer and requires a current, role-bound relay grant on each leg. The node consumes renewals on lane 2 and closes an expired leg. The host independently verifies the controller’s device key. Managed grants refresh over HTTPS or the configured HTTP service origin. Signal socket availability does not gate renewal.

Managed discovery may include two `alternates` in addition to the primary node. Both endpoints receive the same bounded set, and the host registers on each. Managed nodes read the optional `relayUsageUrl` from a verified operator grant. Before forwarding business data the node signs an HTTPS admission request with its node key. The operator counts distinct host/controller pairs across transports. A forwarding holder releases on standby or close. A lost release expires after 60 seconds. Probe and setup traffic is bounded to 64 KiB per minute per candidate before admission. Private nodes require only local enrollment. Route probe chunks use record type 3 on independent links or the `probe` flag on WebSocket frames. A controller can demote a relay with record 4 or a `standby` frame.

The host advertises a persistent listener in encrypted pairing and native hello messages. The controller retains up to eight addresses for each of three recent local networks. Reachable saved addresses support a fresh connection after both devices restart. A changed NAT mapping can require new candidates exchanged through a reachable node or signaling service.

Native records contain a one-byte lane, a big-endian uint16 body length and at most 32 KiB of payload. QUIC opens two bidirectional streams in order, each prefixed by its one-byte lane index. Lane 0 carries control and ordinary messages, lane 1 carries bulk data. TLS/TCP multiplexes both record types on one stream. Application payloads retain their authenticated device-to-device chunk framing. Progressive transcript responses between negotiated native peers carry a per-request sequence starting at 1, including the final success. The receiving inbox withholds out-of-order fragments within its existing byte and count limits.

## Encrypted RPC version 3

The encrypted hello contains `rpc: 3`. Both endpoints require this version;
there is no fallback to older framing. The service welcome protocol remains 1
because the service forwards opaque encrypted frames and does not parse RPCs.

Each encrypted chunk is `{r, id, i, n, e, s, b}`: RPC request ID, transfer ID,
zero-based index, fragment count, `identity` or `gzip`, uncompressed byte count,
and base64 payload. Requests and responses both require the RPC ID. Payloads
are limited to 16 MiB before and after decompression, with at most 4,096 fragments
of 4,096 bytes each. Payloads of at least 1 KiB use gzip at the fastest setting
only when it saves at least 12.5%. Compression happens before encryption.

Per-connection bounded frame queues prioritize live/control traffic, then
session metadata, then transcript and attachment data. Each transfer queues
one frame at a time; one bulk frame is serviced after at most eight other
frames. Per-RPC bounded receive inboxes preserve response order without letting
one slow transcript consumer block the shared socket reader.

A read is cancelled after 30 seconds without its own response-fragment progress.
Other requests and socket heartbeats do not extend that budget. Progressing
transfers have no fixed total timeout. Explicit setup deadlines are absolute.
`request.cancel` names the original read in `cancel`; it never cancels a write.
Cancelled responses' assemblies are discarded immediately. Saturated host
request slots return `HOST_BUSY`; they never silently drop requests. Commands
are never automatically replayed after an uncertain result. A host response with
`code: "WRITE_OUTCOME_UNKNOWN"` means execution may already have completed, even
when its `error` describes an Agent/UI exception. Controllers preserve this code
through local JSON bridges, reconcile the resulting history and do not expose a
write retry while the outcome is unknown. Go and Worker services forward this
encrypted response unchanged and require no service API changes.

`session.remove` is a non-cancellable mutation with `provider` and `session`;
success returns a null result. `providers[].capabilities.sessionRemoval` is
`archive`, `detach` or `delete`, or omitted when unsupported. The host dispatches
to the Provider's removal implementation and existing receipt/authorization
boundary. Busy and unsupported sessions are rejected; an ambiguous completion
returns `WRITE_OUTCOME_UNKNOWN`. Controllers reconcile the directory instead of
replaying this write. These endpoint fields remain opaque to both service runtimes.

Directory reconciliation transfers session metadata deltas. For an opening
`transcript` request, `recentTurns` is an optional hint from 1 to 30 for
turn-paginated sources. The remote controller requests the latest four turns.
Remote controllers additionally request `historyItems: 50` on open and continuation. Codex honors a limit of 1–100 records, counting empty turns as a record. A partial first turn is identified by `partialTurn`. An opaque continuation preserves the source, session, turn and last owed item, allowing older parts of the same turn to be fetched on scroll. The default without the hint remains turn-based. A cached remote page can be revalidated with its existing snapshot revision. Managed Providers use their row and byte budgets.

A regular `watch` returns events for the selected session. The optional `turn`
field requests only that turn's lifecycle events with ID, session ID, turn ID,
kind and status. Both modes preserve epoch, cursor, resets and heartbeats.
Built-in Providers hold the long poll through unrelated traffic. On Android a
background task uses this scoped watch and reconciles on reset or heartbeat
with `turn.status`. Codex requests turn headers for that read. Plain user
messages do not trigger a history snapshot.
History is reread for initial loading, replay-epoch gaps, explicit older pages,
and attachment import events that require an authoritative snapshot.

Progressive history follows source turn/item order. A long item's text is sent
in UTF-8-safe parts of at most 2,048 bytes; `textOffset` is the original UTF-8
byte offset. Receivers append verified contiguous/overlapping text, publish
immutable prefixes and remove `textOffset` from assembled snapshots. They
never wait for a whole long item to render its first prefix. Compression is
also applied to these smaller responses when useful. Codex tool activities
retain their typed metadata without forwarding raw stdout/native tool JSON
that the controller does not render.

Session summaries, transcript turns/fragments and `kind: "turn"` watch events
may carry `recencyAt`, a Unix-millisecond integer for the latest recorded user
submission or successful Agent reply completion. Omitted/zero means unknown.
User items also retain their recorded `timestampMs`. Controllers take the newest
known boundary per device/Provider/session and never advance it for text deltas,
tool updates, metadata edits, reads or reconnection. `updatedAt` retains its
existing content/metadata meaning and is not the Recent ordering key. Failure
or interruption does not fabricate successful completion. These fields remain
encrypted endpoint metadata; both service runtimes forward them opaquely.

Turns in transcript snapshots/fragments and `kind: "turn"` watch events may carry
`durationMs`: a nonnegative integer of Provider-recorded elapsed milliseconds,
including zero for a recorded subsecond turn. Omitted or null means unknown;
clients must not substitute message receipt time. Fragment assembly and caching
preserve it. A later turn event may correct duration without changing status.
This optional endpoint metadata remains encrypted and opaque to both Go relay
and Worker room; service routes, grants and storage need no changes.

Initial/reset catch-up journals watch events until the ordered history is
complete, so small later user messages cannot overtake missing assistant
replies. Once a baseline exists, live watch events remain independent of
background snapshot refreshes. Assistant commentary is retained in completed
history, in its source order; completion does not hide prior visible replies.

Transcript snapshots optionally include `queue: { state, messages }`; a `queue`
watch event carries the same object without a turn ID. `state` is `ready` or
`unknown`. Ready lists replace the queue in source order, including an empty
list; unknown state retains prior messages as stale. Omission means no queue
projection is provided. Each entry has its own `id`, `text`, normalized `status`
(`queued`, `preparing`, `paused`, `uncertain`) and optional attachment previews
(`name`, `kind`). Previews are not download resources. Queued messages are not
turns and do not advance execution status or recency. Initial progressive
metadata carries the queue once; subsequent text fragments may omit it.
Assemblers preserve it, cache content with its watch cursor, and replay newer
queue events over a delayed history snapshot. No new mutation or public service
route is introduced. Go relay and Worker room continue forwarding opaque frames.

## Desktop terminal endpoint operations

`providers[].capabilities.terminal` advertises a desktop host engine. Terminal
requests include the existing `provider` and `session` scope. Params contain a
client-generated 64-hex-character `id`, optional `attachment` token, `after`
output sequence, `inputSeq`, base64 `data` bytes, and `size` with integer `cols`,
`rows`, `cellWidth`, `cellHeight` (pixel dimensions may be zero/unknown).

- `terminal.open` resolves the cwd on the host, reserves the ID and starts the
  user's shell, or attaches to an existing shell with that ID and owner.
- Desktop hosts with shell discovery advertise `providers[].capabilities.terminalProfiles`.
  `terminal.profiles` takes no params and returns an array of `{id, name}`
  for the authorized host. This cancellable read bypasses caches.
- `terminal.openProfile` takes the same params as open plus a required
  `profile` ID. For a new process the host resolves that ID against its
  current installations. An unknown or removed profile fails explicitly.
  Controllers use `terminal.open` for the default shell.
- `terminal.attach` creates a fresh attachment and returns a reset screen.
- `terminal.read` acknowledges `after` and waits up to 15 seconds for a change.
  It is cancellable, but not cacheable or conditionally hashed.
- `terminal.input` accepts exactly the next input sequence; duplicates are
  rejected. Controllers never automatically replay input.
- `terminal.resize`, `terminal.detach` and `terminal.close` resize, detach the
  reader, and explicitly terminate respectively. Detach does not stop a shell.
- `terminal.closeStatus` is a cancellable, uncached check of an accepted close.
  It returns null once the current host confirms termination for the same scope
  and ID. It cannot initiate termination or confirm an ID from a previous host
  process.

Both open methods, attach and read return `{id, attachment, sequence, data?, size, exited, exitCode}`.
`data` contains official NSGo Mirror VT bytes. Input, resize, detach and close return null.
Exit code is meaningful only when exited. Sequence acknowledgement retains
lost frames; an attachment change requires a reset, never a delta from an old
receiver. `TERMINAL_UNSUPPORTED`, `TERMINAL_DENIED`, `TERMINAL_GONE` and
`TERMINAL_DETACHED` distinguish unavailable capability, local host consent,
missing process and a replaced attachment. Terminals are scoped by trusted
transport identity, Provider and conversation. Existing grants apply on direct
and relay paths; local-only `host.terminal.enabled` persists additional consent.
`TERMINAL_CLOSING` indicates that cleanup is underway. A close can return
`TERMINAL_CLOSE_UNCONFIRMED` when its wait expires while cleanup continues. The
controller can check `terminal.closeStatus` after an uncertain response.

Terminal reads have four independent slots per peer and terminal output uses
bulk transport priority, leaving ordinary navigation and Agent operations free.
Terminal payloads bypass durable mutation receipts. Both Go relay and Worker
room already forward these endpoint operations opaquely; no public service
route, grant schema, storage or RPC framing version changes are required.
See [ownership, limits and recovery](../../internal/terminal/README.md).

## Session directory metadata

Session metadata includes `projectDir`, optional opaque `projectKey`, `cwd`,
`createdAt` and `updatedAt` (Unix milliseconds; zero creation means unknown).
Project identity is resolved on the owning host and shared by first pages,
complete delta reconciliation, creation and transcript snapshots. The controller
adds its environment ID locally for routing and grouping; it is not a Provider ID
or a caller-selected replacement for the authenticated remote host.

Optional `project` metadata includes `id`, `name`, primary `root` and associated
`roots` (absolute directory strings on the owning device). Optional `gitInfo`
contains `repository` (sanitized host/path, without credentials/query/fragment),
`branch` (actual execution checkout's local branch), or `commit` (full detached
HEAD object ID). Unknown values are omitted. These are display metadata, not
new grouping identities or activity timestamps. Older hosts omit them; clients
hide unavailable details and never inspect local paths for remote sessions.

Local controllers use the same request and receipt semantics through an in-process
Caller. Remote callers add authentication and encrypted transport and cannot use
local `host.*`/`client.*` administration. Go relay and Worker room still forward
opaque frames; no service route, grant, pairing or storage changes are required.

## Resumable attachment uploads

The controller generates a random 64-character hexadecimal upload ID before
`attachment.begin` and reuses that ID with the immutable filename, size and
SHA-256. Begin returns the existing acknowledged offset, or the completed
attachment. Reusing an ID with a different scope or metadata is rejected.
Writes at an already stored offset verify matching bytes rather than append
duplicates; finish returns the same confirmed attachment when repeated.
Only these idempotent operations may retry a lost acknowledgement. This does
not authorize replaying Agent messages or other writes.

Controllers queue uploads across drafts with at most two active transfers.
Each chunk starts at 4 KiB and adapts up to 48 KiB to observed acknowledgements.
Each attempt is bounded to 15 seconds, with at most three attempts per operation;
removal cancels work and makes a bounded best-effort abort. Progress measures
acknowledged bytes, stays below 100% until confirmation and does not bypass
content integrity validation. Host finish hashes/copies once before publication.

Services continue forwarding opaque encrypted frames: these RPC/cache changes
require no Go device-service or Worker public API changes. Native and browser
controllers implement the same upload and observation semantics in shared UI.
