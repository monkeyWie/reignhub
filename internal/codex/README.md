# Codex Provider implementation

These notes describe this adapter, not repository-wide standards. The common contract is defined in `internal/provider`; generic architecture is documented in [architecture](../../docs/engineering/architecture.md).

## Unified Codex integration

ReignHub registers one **Codex** Provider. Its stable internal ID remains
`codex-app` so existing enablement preferences, pins, caches and attachment scopes
keep their identity. The independent `codex-cli` Provider and shared-daemon
version/start/discovery flow are removed. Controllers hide retired Provider
entries from old cached directories; the unified route lists native history.
They never redirect an old route's writes or infer ownership from creation source.
The retired ID remains reserved against custom Provider collisions.

Installation can be a desktop App or a Codex executable. Enabled automatic setup
attempts the existing IPC service without requiring a desktop App process;
IPC readiness requires healthy IPC and private reader connections. On Windows
and macOS, a local desktop action can select a shared WebSocket backend when
IPC cannot reach the owner. App, CLI and VS Code source metadata stays available,
but the current IPC owner decides routing.
Availability depends on that owner's IPC support, not merely shared history.
The private `codex app-server --listen stdio://` reader/executor remains necessary
for history, models and new sessions; removing the independent CLI integration
does not remove this executable dependency. Local support is selected at compile
time for desktop Unix and Windows; desktop App detection is not the support gate.
On systems without the desktop App, a compatible IPC service and executable must
already be available. Mobile/browser clients remain remote controllers.

## Runtime paths

- Existing Codex-owned sessions: discover the local IPC owner and follow its snapshots/patches. Send, append, stop and approval operations go to that owner.
- Reads and new sessions: use the private app-server where required. ReignHub-owned sessions publish follower state to the App. Shutting down this executor can interrupt its tasks; lock release does not guarantee seamless continuation in the App.
- Creation source, current owner and IPC schema version are separate facts. An owner lookup timeout is a definite pre-submit failure if the thread is held. For an unlocked thread, the official server can resume the same ID and must acquire its own writer lock before any turn is sent. An uncertain write is never replayed through a different backend.

See [IPC routing](ipc.go), [owner publishing](ipc_owner.go) and [runtime composition](runtime.go).

IPC state projections copy only the turn maps they modify and allocate new item
sequence slices. Nested vendor items remain read-only; never round-trip entire
turns through JSON merely to add projection fields. Event comparison skips
unchanged raw turn projections before normalizing their items, while still
comparing every turn ID so historical corrections are delivered. Approval
synchronization remains independent of unchanged turn content.
[`ipc_projection_test.go`](ipc_projection_test.go) checks corrections, UTF-16
live deltas, unchanged updates and source immutability. Run `go test ./internal/codex -run "^$" -bench BenchmarkIPCObserveHistoryDelta -benchmem` to measure projection cost.

Project metadata in [projects.go](projects.go) retains every configured root from
`project/list` or the desktop assignment fallback in `Project.Roots`, including
directories without a current session. `Project.Root` remains the primary root
for existing grouping and creation defaults. Git display metadata is supplied by
the common host workspace resolver from the actual execution checkout; it does
not use a possibly historical Codex creation-time branch. See the
[Provider contract](../../docs/platform/agent-providers.md#project-identity-across-agents).

Follower revisions are immutable. Applying a single shallow replacement copies the surrounding JSON bytes once. Compound patches keep transactional RFC 6902 semantics. Validation, queued follow-ups, stream events and approvals share the new revision’s turn projection. Text replay owns the appended suffix. The local IPC reader limits queued and currently dispatched broadcasts to 64 MiB of wire data and limits the queue to 64 entries. Overflow closes the connection and invokes the existing fresh-follow recovery.

## App follower writes

The App follower steer entry requires a composer `restoreMessage` even when a
`clientUserMessageId` is supplied. Read the current owner's state and send the
same message ID with the prompt, cwd and composer context (workspace roots,
collaboration mode and attachment collections). Missing restore state causes
its JavaScript executor to read `cwd` from undefined before issuing `turn/steer`.
Do not fall back to a private writer or replay the failed write automatically.

`TestIPCSteerPreservesComposerRestoreContext` verifies the transmitted payload.
The opt-in `TestInstalledAppSteerContract` reads the installed ASAR only and
executes its steer entry with synthetic state and an intercepted follower route:

```sh
REIGNHUB_CODEX_ASAR=/Applications/ChatGPT.app/Contents/Resources/app.asar GOWORK=off go test ./internal/codex -run TestInstalledAppSteerContract -v
```

This reproduces the missing-context error and validates the supplied composer
shape without connecting to a real App conversation or running a model. It is
not evidence of a completed real App send. Reinspect upstream code when this
check reports a changed entry or boundary.

## Questions and answers

The adapter exposes normalized `Approval.questions` through the existing
session-scoped `approvals` / `decide` endpoint calls. Synchronous
`item/tool/requestUserInput` requests retain their exact question IDs, labels,
descriptions and secret-input hints; their answers use the native RPC response or
`thread-follower-submit-user-input` with `response.answers`.

For asynchronous questions, the App attaches
`{ title, options?: string[] }[]` to `agentMessage.questions`. IDs are serialized
`["request_user_input_async", itemID, index]`. The adapter projects unanswered
questions from the latest in-progress/completed turn into optional forms. An
accepted user/steering reply resolves the matching IDs; unacknowledged steering
does not. A newer turn expires old prompts.

Async submissions serialize `[{ questionItemId, question, answer }]` inside
`<send_user_message_question_reply>` and its closing tag. They use the captured
owner's ordinary steer/new-turn route, with normalized text input and composer
restore context. Recheck the owner and question set before writing. Skip dismisses
the form in ReignHub's Provider lifetime and sends no Agent message; omitted
answers in a partial submission are also dismissed. This dismissal is not a
persisted Codex App preference and can reappear after a Provider restart.

Claim each form once. Keep it visible while the write is in flight so polling
does not discard a draft on rejection. Definite pre-write rejection permits an
explicit retry; success or unknown outcome leaves a hidden tombstone until the
request/turn disappears, preventing stale snapshots from recreating submit
buttons. Never retry through another owner or private writer. These additions
do not change public Go/Worker service routes: both relays carry opaque frames.

`questions_test.go` covers textless discovery, exact options/answers, accepted
echoes, skipped siblings, expiry, owner changes, cancellation, concurrent claims,
rejection/recovery and uncertain outcomes using isolated IPC fixtures. Controller
tests exercise multi-question selection, custom text, draft/navigation, mobile
widths and busy/error feedback. They do not submit real Agent answers.

## Optimistic input and delivery outcomes

App IPC input is stricter than app-server input. Every text input requires a
`text_elements` array before the App renders its optimistic user message. The
adapter normalizes text at both send/steer boundaries and in owner publications,
including text references accompanying files; existing rich elements survive.
Missing arrays can crash the App conversation page even after a message has
persisted. Server acceptance does not imply successful App rendering.

Steer routing checks the current owner's latest turn before dispatch: an ended
turn uses a new-turn request. A race rejection is returned without automatically
replaying the write. Only verified pre-submission rejections count as definite
failure; arbitrary IPC JavaScript errors are uncertain. Preserve that distinction
through the encrypted RPC and native JSON bridge for controller reconciliation.

`TestIPCTextInputsSafeForOptimisticAppRendering` covers transmitted send/steer
payloads. `TestInstalledAppTextInputContract` reads the installed ASAR and executes
the actual failing renderer parser with synthetic input, reproducing the missing
array failure before validating the normalized input. Run with the same
`REIGNHUB_CODEX_ASAR` environment variable as the steer contract check. No live
App writes, model execution or restart is involved.

A context compaction item retains its ID and position as a system event in
both history and live updates. Its display text is localized by the controller.
Missing thread names and previews remain empty in Provider metadata.

## State and history diagnosis

Session directories enrich the private reader's metadata with the actual App
owner's runtime state. `thread/list` on a separate app-server can report
`notLoaded` for a thread actively executing in the App; it is not authoritative
for that external owner. The adapter probes OS writer locks, follows only held
threads through existing read-only IPC and projects activity into the shared
Session summary. No `resume`, conversation-item hydration, writer takeover or App
restart is used. IPC snapshots remain host-local; directory clients receive
metadata, not unselected conversation content.

Directory enrichment has four concurrent reads and a two-second page budget,
sharing the existing 128-follower cap. Unknown ownership, disconnect or failed
state reads produce unknown activity, never idle/completion. Terminal owner state
is preserved when the writer releases its lock, then directory-only followers
are released; explicitly opened conversation subscriptions remain owned by their
normal lifecycle. Active runtime flags and pending requests represent waiting
for input/approval. `TestDirectoryUsesLiveAppOwnerWithoutOpeningConversation`
uses isolated IPC and real temporary OS locks to cover active/completed state
without operating real Agent conversations.

A valid replay cursor does not prove that its upstream follower still exists.
Watch/probe restores missing or broken followers independently of replay-window
resets. Opening a directory-followed thread promotes it to an explicit follower,
so directory cleanup cannot unsubscribe it between turns. A historical thread
without a current owner remains read-only; discovery never acquires a writer.
`TestIPCValidCursorRestoresAndRetainsFollower` covers attachment from a valid
cursor and continued live text after directory cleanup using isolated IPC.

Stream revisions are scoped to the owner's current stream, not the IPC client's
lifetime. The installed desktop producer clears its revision map when its backend
reconnects without necessarily disconnecting IPC. Full snapshots establish a new
base even when their revision is lower; only patches require continuity with the
current base. Rejecting lower snapshots freezes all subsequent lower patches
until the counter catches up. Validate the current discovered owner and schema
before accepting either form.

An owner's `thread-stream-following-status-requested` is an invalidation hint,
including when its source differs from the remembered owner. Wake the affected
watch immediately, rediscover ownership, re-follow and wait for a new snapshot.
Never authorize a writer or accept an unsolicited owner's snapshot solely from
that hint. Track invalidation generations separately from received metadata, so
a valid patch or overlapping announcement cannot erase pending repair.

[ipc_recovery.go](ipc_recovery.go) coalesces explicitly invalidated watch/history
reads into one five-second discovery/re-follow/fresh-snapshot attempt. Failures
have a five-second cooldown; waiting readers can cancel independently. An
unchanged revision can acknowledge the new snapshot. Definitive owner absence
retires the projection; failed repair retains the read-only persisted-history
fallback without stale IPC previews. Healthy streams have no age-based full-state
refresh. Completion and repair invalidate queue presentation through the same
notification path. Public Go/Worker service contracts are unchanged.

[Stream lifecycle tests](ipc_lifecycle_test.go) reproduce revision reset and owner
handoff without a timer, session switch or real conversation. [Recovery fixtures](ipc_recovery_test.go)
cover both schemas, invalidation followed by metadata traffic, fresh barriers,
cancellation, backoff, owner loss and consumed queues. The
[installed producer check](testdata/installed-app/stream.cjs) exercises reconnect and patch revisions using a synthetic state.

```sh
node internal/codex/testdata/installed-app/stream.cjs /Applications/ChatGPT.app/Contents/Resources/app.asar
```

Recent ordering uses fixed conversation boundaries, not `thread.updatedAt`.
IPC turns supply `turnStartedAtMs` and completed `durationMs`; app-server turns
supply `startedAt`/`completedAt` in seconds. Explicit steering-user creation
times also advance recency. Only a successful completed turn contributes its
end time; streaming durations and assistant/tool timestamps never do. For
directory rows without live boundaries, read only the latest turn metadata via
`thread/turns/list` (`limit: 1`, `itemsView: "notLoaded"`). This is a read-only
metadata request, not conversation hydration. It has four concurrent slots and
a two-second page budget, with an at-most-2,048-entry cache keyed by session and
metadata revision/status. Failures retain known timing and retry on later scans.
Older reads cannot regress a known event time. No full transcript is fetched
for sidebar ordering. `recency_test.go` verifies streaming, steering, completion,
metadata edits, caching and failed-read recovery with isolated fixtures.

Unknown state or patch structures can break watch independently of history. Schema version alone does not identify App versus IDE creation. Read verified v11/v13 state and apply Immer array-path patches transactionally against revisions; reject unknown structures. Initial history and watch run independently, with read-only history fallback and canonical/live merging. The adapter currently publishes v11 owner state; reading v13 is not complete v13 owner support.

Sources and regressions: [state handling](ipc_state.go), [schema](ipc_schema.go), [schema tests](ipc_schema_test.go), [ownership tests](ipc_test.go) and [controller history failure tests](../../clients/nsgo/hubui/session_failure_test.go). Use isolated fixtures; real App behavior and unsupported upstream operations require separate verification.

App-owned turns also contain `steeringUserMessage` items: read their `input`, preserve the local item ID and recorded creation time, and match acknowledged server representations by explicit server/client IDs. Never deduplicate equal text. Merge canonical history with the live item order; a steer can appear before existing Agent output. Item insertion, removal or replacement requires a snapshot because common item events append at the controller.

Asset-bearing live updates invalidate history instead of publishing an unprocessed input envelope. The cancellable transcript reader imports both regular and steering image/file inputs. Keep already displayed cards until their imported replacements arrive. Plain text remains incremental. Regressions cover both verified state versions, acknowledgements, ordering and live image import in [message tests](ipc_messages_test.go).

A change to IPC `latestModel` invalidates the transcript through the existing
`kind: "snapshot"` notification, even when no turn or item changed. The refreshed
Session exports the current model; unchanged metadata produces no extra read.
This adds no polling and changes no Go/Worker public service contract.

Preserve assistant `phase` in snapshots and item events so the controller can
distinguish commentary from final answers. When one IPC update both finishes a
turn and extends its answer, publish full authoritative items before the terminal
turn event. Terminal item corrections also use full item events, never text
deltas: controllers deliberately ignore streaming deltas after completion.
`TestIPCCompletionPublishesFullAnswerBeforeTerminalStatus` covers both verified
IPC versions, the final text tail, a late correction and history phase parity.

Preserve `durationMs` from canonical IPC entities as well as the live tail in
normalized turns and watch events, including duration-only corrections after
completion. Prefer Codex's recorded duration; when it is unavailable, a valid
app-server `startedAt`/`completedAt` pair (Unix seconds) supplies elapsed time.
Missing or invalid timing stays unknown. Do not derive duration from receipt time,
message timestamps or summed tool durations. Progressive history must retain the
same metadata as a full transcript.

## Windows local connection

Windows uses `\\.\pipe\codex-ipc` through Microsoft's `go-winio`, with platform
file suffixes separating it from Unix sockets. The client requests data
read/write, read attributes and synchronize rights; generic write additionally
requests server-instance and attribute/EA rights which a pipe DACL can reject.
Only a dial returning `ERROR_ACCESS_DENIED` triggers best-effort source diagnosis
in [diagnostic_windows.go](../codexipc/diagnostic_windows.go). It opens a temporary
metadata handle with zero desired access (no data read/write), obtains the server
PID with `GetNamedPipeServerProcessId`, closes the handle, then takes one Toolhelp
process snapshot. Communication still uses `go-winio`; successful connections
perform no diagnostic checks, and no polling or retry loop is added.

Diagnosis has a 200 ms cooperative budget, at most 16,384 snapshot entries and
32 ancestry nodes, with cycle detection and cancellation checks between native
calls. Synchronous Win32 calls themselves cannot be interrupted by the context;
there is no pipe wait/retry or detached diagnostic goroutine. Handles close on
all exits. Unknown sources, query failures, snapshot limits and cancellation keep
the original permission error and explicitly unconfirmed recovery guidance. The
fallback asks users to check for Codex instances in SSH, VS Code or other terminals,
close the corresponding instance if present, reopen desktop Codex App, and check
user / privilege alignment if denial persists. It never claims that another
environment was detected. A successfully queried server PID remains available
when its ancestry cannot be classified. Metadata-open, PID-query, process-snapshot
and cancellation/timeout failures appear in the diagnostic error text instead of
being silently discarded. These secondary failures do not replace the dial error
in `errors.Is` / `errors.As`, including the original `os.PathError`.

Exact, case-insensitive `sshd.exe` / `sshd-session.exe` ancestry produces an SSH
hint; `Code.exe`, `Code - Insiders.exe`, `code-tunnel.exe` and
`code-tunnel-insiders.exe` produce a VS Code hint. SSH takes precedence when both
appear. `node.exe` alone proves neither. These names and parent PIDs are clues,
not authenticated origin: processes can exit and PIDs can be reused. Guidance
says the service **may** come from that environment, identifies the pipe server
PID (not an ancestor PID), and asks the user to close the corresponding Codex
instance there, reopen desktop Codex App, and retry. The Provider consumes this
guidance in `appIPCConnectionError`, including when the Windows diagnostic cannot
identify a source. Other platforms retain their generic permission guidance.
No process termination, ACL changes, elevation or App restart
is performed. This is local Provider diagnosis only; the Go/Worker public
service contract and Worker implementation need no changes.

Package discovery uses the current user's registered package names and Win32
`GetPackagePathByFullName`, then validates the package manifest and executable.
Process discovery uses Toolhelp and process handles; it launches no PowerShell or
process-listing command. Private stdio backends use
the shared `system.HideConsole` Windows adapter (`CREATE_NO_WINDOW` + `HideWindow`).
Automatic setup only attaches to an existing IPC owner or reconnects to an
already configured shared backend.

### Desktop WebSocket fallback

The local Agent list exposes Start Codex or Restart Codex on Windows and
macOS when IPC setup fails, an existing router cannot reach a held thread,
or the shared backend needs the desktop to relaunch. The action is hidden
while the shared WebSocket connection is healthy.
Other sessions remain available while recovery is offered. Restart validates
the approved process generations and accepts those that have already exited.
Closing the last window on Windows leaves Codex running in the tray. The Windows
restart dialog discloses that confirming will forcibly stop Codex. ReignHub
validates the approved main processes by executable path and creation time and
retains their Win32 handles for termination and a bounded wait. Discovery filters
out Electron helper processes. If a new main process appears before launch,
ReignHub asks the user to refresh and confirm again.
macOS uses NSRunningApplication and NSWorkspace to discover, quit, and
launch the app. Both platforms supply the WebSocket override through the
new process environment. Only the local desktop can use host.activate.

ReignHub and Codex App initialize independent JSON-RPC connections over one
loopback listener to a single official app-server. ReignHub supplies bearer
authentication to the upstream server. The desktop URL contains a random
per-launch credential because the desktop override supplies a URL without
auth headers. The listener rejects browser Origins and unrecognized paths.
Neither the URL nor credentials appear in status, logs or diagnostics.
The provider reports connected after forwarding a successful response to the
desktop's initialize request for the current launch. Desktop versions can
proceed with requests without sending an `initialized` notification.

The shared connection handles reads, new sessions, sends, interruption and
server requests delivered to the ReignHub connection. Watches subscribe to
loaded threads on that server. The original IPC route remains preferred
before the explicit switch. The two servers do not transfer an active turn.
Failed or ambiguous writes are never replayed on another connection.
Restarting Codex while the shared backend is healthy reuses that backend.
ReignHub reconnects to the same server after a transient local transport loss.
ReignHub shutdown ends the shared backend and its tasks, while the desktop
process remains open. The App needs a new launch after that backend ends.

The opt-in TestSharedCodexServerWithBundledCLI uses a temporary Codex home
and a compatible executable supplied through REIGNHUB_TEST_SHARED_CODEX_CLI.
It verifies authentication, independent initialization, shared thread reads
and readiness after a replacement desktop connects without an `initialized`
notification. Windows restart tests exercise process generation validation and
termination against a disposable child. Windows Store launch requires
validation with the desktop App.
The local fallback uses the existing encrypted endpoint and local activation
contracts. Go service and Cloudflare Worker forwarding require no changes.

Windows fixtures cover restricted named-pipe data exchange and a console-less
child with redirected IO/cancellation. [Pipe fixtures](../codexipc/local_windows_test.go)
also deny data access on a unique test pipe, query its actual server PID, remove
only the fixture's denial and verify a fresh connection and data exchange.
[Diagnostic tests](../codexipc/diagnostic_windows_test.go) cover recognized and
unknown sources, error identity, query failure, cancellation, budget, cycles and
depth limits; [Provider tests](runtime_windows_test.go) cover guidance rendering.
All new platform tests use Windows-selected files and never connect to the real
`codex-ipc` pipe. These tests require native Windows to run; cross-compilation and
isolated protocol tests do not establish real Windows App interoperability or
confirm the user's pipe ACL. The implementation reuses existing dependencies;
binary size changes require build measurements.

## Queued follow-ups

The controller projects queued input separately from executed turns through
`Transcript.queue` and `kind: "queue"` watch events. [queue.go](queue.go) accepts
v3 `thread-queued-followups-changed` only from the selected thread's current IPC
owner and local host. Exact message IDs and order survive duplicate text. Keep
preparing, paused and uncertain submissions distinct; malformed state or owner
loss is unknown, never proof that the queue is empty. Owned queues publish the
same projection when they change. No queue write or replay is initiated by a read.

Queue broadcasts are independent of conversation snapshots. The desktop IPC interface offers no queue read request. For an existing queue at first open, [queue_storage.go](queue_storage.go)
reads only its selected thread from `queued-follow-ups` in Codex home. An
initialized `.codex-global-state.sqlite` schema v1 (`settings`, global scope)
supersedes the older `.codex-global-state.json`; unknown/corrupt SQLite must not
fall back to potentially obsolete JSON. Reuse the existing SQLite driver with
read-only/query-only access, a 50 ms lock timeout inside the selected queue read's 500 ms total budget.
JSON reads are capped at 16 MiB, each source at 4 MiB and 100 messages with at
most 100 attachment previews each. File I/O observes cancellation between bounded
reads; synchronous OS reads themselves cannot be interrupted. Never create,
migrate or modify vendor state, and do not add a storage polling loop. Live owner
broadcasts, including empty queues, supersede the entire staging bootstrap.

Queue presentation also excludes messages with explicit delivery receipts in
the same thread's history: server `userMessage.clientId` and acknowledged
steering client IDs. A bounded set of the latest 256 IDs reconciles both staging
IDs and server-queue `clientUserMessageId` links, including missed consumption
broadcasts or lagging storage exports. Pending optimistic items and equal text
are not receipts. New receipts invalidate the selected snapshot; late queue
broadcasts cannot reintroduce matching accepted entries. This only filters the
projection: it never removes vendor entries, retries a send or resolves an
unknown execution outcome by guessing. Failed queue sources remain unknown.

The VS Code extension publishes the same v3 broadcasts after queue writes. It stores queues in `globalStorageUri/queued-follow-ups.json`, keyed by thread ID. Existing
queues need not be broadcast just because a follower connects. Compile-selected
desktop discovery checks only the standard Code and Code - Insiders
`User/globalStorage/openai.chatgpt/queued-follow-ups.json` paths beneath
`os.UserConfigDir()` (Roaming AppData on Windows, Application Support on macOS,
XDG config on Linux). Each regular file read is capped at 4 MiB with cancellation
checks between 32 KiB chunks. Do not scan profiles, open unrelated state databases
or write vendor files. Custom user-data directories, remote extension hosts and
legacy VS Code globalState storage are not bootstrap sources; live owner events
remain available. Merge selected-thread staging entries by exact ID, preserve
identical text with different IDs, and cap the union at 100 entries. A failed
source or overflow makes the result unknown while retaining readable entries.
Creation-client `sourceKind` is not evidence of current ownership.

The same bundle also uses a separate app-server queue. [queue_server.go](queue_server.go)
reads `thread/queue/list` without resuming or acquiring the thread, with a 300 ms
budget, at most four pages, and cursor-cycle/size/identity checks. Only explicit
method-not-found means this source is unsupported/empty; failures remain unknown.
Merge server order before desktop staging, using server `clientUserMessageId`
links to suppress duplicates in favor of their desktop entry, never text equality.
Full-input `codex-untrusted-app-input:` placeholders project only visible message
text and image counts. Server `thread/queue/changed` notifications invalidate the
selected snapshot through the existing watch reconciliation; they do not read on
the RPC receive loop or start a new polling task. Fence in-flight reads against
newer notifications and owner loss. Local broadcasts preserve the last known
server portion, and failed reads preserve stale entries until a complete ready
list replaces them. Missing desktop storage means no persisted desktop entries;
corrupt/unreadable storage remains unknown. The controller caps a stale union at
200 entries rather than accumulating unbounded messages during a prolonged failure.

Export only text, IDs, normalized state and attachment names/kinds. Composer
paths, credentials and execution context stay host-local. The UI currently offers
queue preview, not queue mutation. Tests in [queue_test.go](queue_test.go) use
temporary JSON/SQLite and isolated IPC, covering bootstrap, changes, consumption,
owner isolation, malformed state, cancellation, recovery and fragment assembly.
VS Code fixtures also cover first-open persisted queues, missing/corrupt/oversized
files, read-only behavior, exact-ID merging, bounds and live consumption overriding
stale files. A remote controller requires the controlled host to run the queue
export implementation; updating only the controller cannot supply absent data.
[Server queue tests](queue_server_test.go) cover both sources together, explicit
ID matching, consumption, app-message/attachment normalization, pagination limits,
unsupported methods, cancelled reads and stale notification races.
[Controller tests](../../clients/nsgo/hubui/message_queue_test.go) cover stale
snapshot replay, cache/offline display, editor/preview identity and bounded
layouts. These fixtures do not operate real conversations or prove every Codex
App/CLI/VS Code queue implementation. The shared encrypted endpoint fields need
no changes in Go relay, Worker room or public service TS types.

## File links

Markdown file references are exported only from the session workspace or the Hub
attachment directory, through the existing scoped attachment store. Source-line
suffixes and anchors identify the actual file; they do not become part of its
filename. Preserve the original link as `Attachment.Source` for activation and
mark source-location references inline so they do not add separate file cards.
The controller reveals local files through NSGo's file-manager API and downloads
remote files through their exported attachment IDs. It never turns a remote path
into an arbitrary filesystem read or opens it on the controller. This changes no
Go/Worker service endpoints or encrypted relay behavior. Isolated attachment and
controller link tests cover path decoding, location suffixes, remote routing,
missing files and actual downloaded bytes.
