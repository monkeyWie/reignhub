# Conversation terminals

`terminal.go` owns terminal processes; `conn.go` adapts the endpoint contract to
an `io.ReadWriteCloser`. Neither depends on NSGo. Desktop composition injects
the official NSGo engine from `clients/nsgo/terminalengine` through the native
bridge and embedded runtime. The root service alone does not advertise terminal
support. Web and iOS use the NSGo terminal view with the same remote connection to a
paired desktop. Android requires upstream terminal renderer support.

## Ownership and lifetime

A terminal belongs to an authenticated controller identity, Provider, conversation
and random 256-bit terminal ID. Local calls use the same manager with a local
owner. Remote identity is supplied by the verified transport, never request JSON.
Different controllers receive independent shells; a new attachment to the same
terminal invalidates the older attachment's input/read token.

The host resolves the initial absolute directory from its Provider's conversation
metadata and checks that it exists. Each tab starts an independent interactive
shell in that directory. The host owns executable paths, arguments and
environment. On Windows the default looks for `pwsh.exe`, `powershell.exe`,
`%COMSPEC%` and `cmd.exe` in order. A controller can also request an installed
profile by ID. The Windows host discovers cmd and Windows PowerShell in the
system directory and uses PowerShell installation records, conventional
directories and absolute PATH entries to find side-by-side pwsh versions.
Product version metadata distinguishes their major versions. Git for Windows
and WSL with a registered default distribution are also supported. macOS and
Linux hosts list the configured shell, executable entries from `/etc/shells`
and common shells found on PATH, with one entry per shell name. Discovery
returns IDs and names. Opening the profile resolves the executable again.

Opening reserves the ID before setup. Repeating open attaches to that process;
failed setup can be retried. Retired IDs are retained, so a delayed open cannot
recreate a terminated process. Setup has a ten-second host context and continues
when the user switches tabs during startup. The controller unmounts the view on
hide or navigation and keeps recent receivers in memory. Closing a
tab or using Kill Terminal ends that shell. The host waits for engine exit before
recording a scoped termination acknowledgement. Accepted cleanup continues if the
request disconnects. If the acknowledgement is lost, the controller checks the
host's close status without replaying the write. The tab stays visible when the
host cannot confirm termination. Host application shutdown closes all shells and
waits under one two-second deadline. Shells and panel state do not survive app
restart. No real Agent process is terminated by this manager.

## Transport and bounds

The host is a headless official Terminal plus Mirror. Never attach an NSGo View
to that same Terminal: View and Mirror consume the engine's drawing changes.
Each controller creates a receiver Terminal with `Options.Conn` and
`ResizeByConn`, and an official View. Mirror sends screen changes, scrollback,
cursor, modes and program colors, not an application-written terminal parser.
Upstream OSC 52 clipboard writes are retained: a command in a trusted terminal
can copy to the controller clipboard. Copy/paste also use the official Ref API.

The controller retains the eight most recently viewed receivers per workspace,
including their attachment, emulator and scrollback. Switching tabs, collapsing
the panel or moving between conversations cancels the outgoing read and pauses
polling until the receiver is visible again. Returning to a cached receiver
renders its current screen and scroll position immediately and resumes from its
last frame sequence. Input already sent can finish while a tab is hidden.
Eviction releases the attachment and receiver. Forgetting a device clears its
cached views and receivers. Revisiting an evicted receiver or reconnecting after a
transport failure requests a fresh host snapshot.

Because upstream has no headless change subscription, a read samples Mirror at
most 30 times/second and returns on a change or after 15 seconds. Terminal
content remains in memory and stays outside transcript caches and mutation receipts.

Output carries an attachment token and sequence. The host retains its last frame
until the receiver acknowledges it; a cancelled/lost response cannot consume
unseen output. Reattachment resets the receiver from the host's current screen.
Input has a per-attachment monotonic sequence, is serialized and never retried.
If a write's result is uncertain, automatic reconnect stops and the UI asks the
user to check execution after reconnecting. Reads and resizing failing during a
pending write retain that same uncertainty. Resize requests coalesce without
blocking the UI thread.

Limits: 16 retained terminals per host, 4 concurrent terminal reads per peer
(separate from ordinary request slots), 400 by 160 cells, 4 MiB per frame,
512 KiB engine scrollback, 32 KiB per input RPC and 1 MiB per paste. Because NSGo's
asynchronous input queue has no public backpressure hook, total accepted input
is capped at 16 MiB per shell lifetime. Opening a new shell resets that budget.
At most 4,096 terminal IDs are retained during one host lifetime. Exceeding a
limit returns an explicit error. Setup, reads, resizing and cleanup are bounded
and cancellable. Unattended shells count toward the retained-terminal limit.

## Permission and recovery

Local use is available without an account or remote permission. Remote use
requires the existing pairing/grant/Provider authorization plus the desktop's
persisted `RemoteTerminalEnabled` setting, which defaults to true when absent. An explicit false persists across restarts. Only local
`host.terminal.enabled` changes it. Permission is checked before dispatch and
again before a successful response, including an in-flight read. Revocation
blocks further access but does not kill a running command; application shutdown
still cleans it up. Provider disablement also rejects new terminal operations.

The UI retries transient attachment failures up to five connection attempts with
bounded backoff; it does not automatically repeat creation or input. Definite
permission, missing-terminal, replaced-attachment and expired-grant failures stop
retrying. Reconnect attaches to the existing process. An exited/missing terminal
offers explicit reopen with a new ID after confirming termination. An old host
without the capability shows an update hint instead of opening a local shell.

## Verification

- Root `go test -race ./internal/terminal ./internal/control ./internal/remote -run TestTerminal`
  covers owner isolation, retained frames, cancellation, limits, duplicate input,
  shutdown, encrypted relay/TCP recovery and permission rejection.
- NSGo `go test ./terminalengine ./hubui -run TestTerminal` exercises a temporary
  non-login shell, Mirror replay and the actual official receiver component with
  a fixture host. `go tool nsgo test -target macos -run TestTerminalPanelLifecycleAndShortcut ./hubui`
  exercises native key dispatch, IME, focus, below-composer geometry and cleanup.
- Test fixtures never access real Agent conversations. Cross-compilation is not
  Windows/Linux runtime acceptance; real ConPTY, full-screen programs, sustained
  output floods and physical lock/resume require separate platform checks.

See the [wire contract](../../docs/platform/protocol.md) and [UI standards](../../docs/engineering/frontend-design.md).
