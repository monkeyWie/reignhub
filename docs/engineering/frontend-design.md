# Frontend design standards

These are required interaction and review criteria, not a claim that every platform has already passed acceptance. Known violations are defects.

## Shared system and visual language

Controller UI lives in `clients/nsgo/hubui`; native and Web APIs provide platform access. The official website lives in `apps/site`. Reuse the pinned NSGo components and existing semantic tokens instead of creating another chat UI. Follow the site's directory instructions for Next.js changes.

Avoid unnecessary secondary labels and descriptions. Let the layout do the
explaining. Use labels for choices, meaningful status and next steps; do not
repeat the same state in a subtitle, badge and button. Product surfaces describe
what the user can do. Protocol names, endpoints and implementation details belong
in explicitly opened diagnostics unless needed to make a decision.

Use shadcn zinc neutrals as the default interaction language, with a restrained green brand accent. Support light, dark and system appearance. Source controller colors from [appearance.go](../../clients/nsgo/hubui/appearance.go); do not hardcode page-specific palettes.

| Role                             | Light                 | Dark                  |
| -------------------------------- | --------------------- | --------------------- |
| Page / foreground                | `#ffffff` / `#09090b` | `#09090b` / `#fafafa` |
| Muted surface / border           | `#f4f4f5` / `#e4e4e7` | `#27272a` / `#27272a` |
| Secondary text                   | `#71717a`             | `#a1a1aa`             |
| Brand accent / accent foreground | `#79c476` / `#102610` | `#79c476` / `#09090b` |

The website keeps its own semantic CSS roles within the same neutral/green identity. Reuse spacing, typography and radius tokens; the current base radius is 10. Use Lucide for actions and [shared Simple Icons assets](../../internal/brandicons/README.md) for recognizable platform, Agent and browser brands. Preserve attribution and licenses. Ordinary offline status is not an error alarm.

Use one Provider-to-brand mapping across Agent lists, session metadata and tree
nodes. Known Agents use locally embedded Simple Icons silhouettes in neutral
foreground colors. Keep the text label to distinguish interfaces sharing a brand,
such as Codex sessions originating from App, CLI or VS Code. Missing artwork uses the generic Agent icon; never
infer a brand from a user's title, an arbitrary name substring or the model.

Support multiple languages without assuming a fixed language set or pair. Locale configuration defines the supported languages; keep product content, navigation and interactions consistent across them. Layouts must accommodate translated text and locale-aware formatting.

## Neutral interaction colors and selective emphasis

Routine surfaces, loading indicators, text carets, focus rings, navigation
backgrounds and ordinary primary actions use neutral colors. Establish hierarchy
through luminance, typography, spacing, shape and restrained motion.

Use the brand theme color where color communicates an actionable link or an
explicit choice: message hyperlinks, inline highlights, checked checkboxes,
selected radio options, enabled switches and selected options. Retain a shape,
checkmark or underline as well as color. Text selection can use a translucent
brand fill; the caret stays neutral. Completed unread markers also use the brand
color. Avoid coloring every navigation row, spinner or toolbar icon.

In the controller, NSGo `Accent` and `accentFill` express selected controls and
links. Explicit foreground/background roles keep ordinary filled action buttons
neutral. `brand` / `brandText` supply the same theme colors for custom controls.
Use the centralized light/dark palette rather than hardcoded widget colors.

Preserve semantic error, warning and success colors where they communicate an
outcome or urgency, with a label or recognizable symbol. Neutral controls still
need visible hover, pressed, selected and keyboard-focus states. Keep links
recognizable through underlines and preserve adequate contrast in both themes;
do not depend on hue alone. Check focused inputs and selections as well as
resting, loading and completed states. Theme changes must update colors in place
without restarting animations or replacing the focused editor; respect reduced
motion. Native system-owned controls retain platform conventions.

Filled action buttons keep the same foreground/background contrast polarity
through hover and press. Use a small background luminance change on hover and a
slightly stronger change on press, keeping the icon/text fully opaque. Do not
reuse muted row-hover fills for solid buttons: that can turn a light button dark
while leaving its dark icon unreadable. Send, stop and ordinary primary buttons
share this treatment in both themes. Reserve reduced whole-button opacity for
disabled states; hover must not resemble a disabled control.

## Progressive disclosure and contextual actions

Keep the resting interface focused on content, navigation and essential status.
Use progressive disclosure for secondary actions and configuration instead of
showing every possible control at once. Choose what is essential by task and
frequency, not by available space alone.

- On pointer devices, reveal secondary row actions and disclosure indicators on
  hover or keyboard focus within the row. Use stable hit areas and reserved or
  overlaid space so titles and neighboring rows do not jump. Reveal controls
  immediately; moving from content to its actions must keep them available.
- A secondary click opens the same action set. On touch, replace hover actions
  with a long-press context menu; holding or dismissing it must not also activate
  the row. Do not make any operation depend exclusively on hover. Preserve
  keyboard traversal, visible focus and accessible action names.
- Put optional filters, grouping and display settings behind one clearly named
  icon that opens a compact menu. Present dimensions as icon-and-label rows
  with a trailing submenu arrow, and choices with a trailing checkmark. Avoid
  dialog headings, explanatory paragraphs and a full form of always-visible
  controls for these quick settings. Use cascading submenus where space allows;
  touch and narrow windows use a bounded sheet with one level and a Back row.
  Preserve multi-select choices without closing the menu. Show active filters,
  result/empty feedback and an easy reset without remounting the working content.
- Suppress empty optional collections when they have no useful content. Keep
  essential navigation discoverable. Collapsible headings expose their expanded
  state and work across their entire hit area. Collapsed headings retain a visible
  disclosure cue; expanded headings may reveal it on hover/focus. Touch retains
  the cue in both states. Section headings stay unfilled on hover; reveal their
  disclosure and trailing action icons instead of applying a selected-row
  background. Retain pressed feedback and visible keyboard focus. Filter actions
  use a funnel icon with foreground emphasis and a separate active-filter marker.
- Tree branches use one leading icon, without a separate disclosure arrow beside
  the object icon. When an object has recognizable open/closed variants, use
  them to show expansion, such as an open or closed folder. Keep the entire row
  clickable and expose its expanded state to assistive technology. Move optional
  aggregate counts and paths into a bounded information panel on hover or keyboard
  focus; secondary click and touch hold expose the same information without
  toggling the branch.
- Persistent collections need a concise empty state beneath their expanded
  heading. Distinguish no results, initial loading and unavailable data instead
  of leaving unexplained blank space. Optional empty collections may stay hidden.
  When the entire collection is empty, replace its empty subdivisions with one
  compact, aligned state and a relevant next action. Do not repeat “no items”
  under multiple vacant headings or add a bordered onboarding card inside a
  sidebar. Restore normal sections when data arrives. Background polling must
  not repeatedly replace a settled empty state with a loading indicator.
- A row's trailing area can show activity or unread status at rest and contextual
  actions on hover/focus. Swap them in the same reserved space; do not overlap
  indicators and actions or shift the title. Use transparent icon buttons with
  foreground emphasis on hover, visible keyboard focus and individual tooltips;
  avoid adding separate filled tiles inside the row's hover surface.
- Align the visible glyphs of content actions with the content's text edge,
  not just their padded button bounds. Keep comfortable hit areas by extending
  them into available gutters rather than shrinking the controls.
- Use recognizable icon-only controls for contextual navigation when the icon
  communicates the action. Reveal them only when useful; provide a tooltip and
  accessible name instead of a persistent text label. For example, returning
  to the latest message uses a small circular down-arrow centered above the
  composer only while reading earlier messages. Its overlay must pass through
  input outside the button and retain a comfortable touch target.
- Contextual controls still provide immediate pressed/pending feedback. Confirm
  destructive or provider-dependent removal with the actual scope and outcome.
  Keep the confirmation dismissible and failures recoverable; uncertain writes
  must not become retry buttons. Never overload the content's primary click.
- Hints use themed surfaces, borders, padding and bounded wrapping, positioned
  inside the viewport. They must not obscure or compete with menus or row actions.
  Information panels use aligned icon-and-text rows with one consistent icon
  column, a stronger title and subtle dividers between meaningful groups. Wrapped
  values align with the text column. Use neutral icons, not colored tiles or
  repeated field labels; omit unavailable metadata rather than inventing it.

Icon-button tooltips share one placement policy: centered above the trigger
first; flip below when the top cannot fit and the bottom fits or has more room;
if neither fits, use the roomier vertical side and constrain the hint inside the
safe viewport. Clamp horizontally rather than switching to a side-facing hint.
Use the shared tooltip helper and NSGo overlay placement, including after window
resizing; individual buttons must not choose arbitrary sides. Rich information
panels may deliberately open toward the content area (for example, sidebar
session/project details), but action hints keep the same top/bottom policy.

Terminal tab hints show the shell type and full path as separate icon-and-text
rows. Keep the last path reported in the terminal title and fall back to the
session's launch directory. Add the current program-supplied title as a third
row when it contains additional information, such as a tool name. The title is
display metadata. Wrap long values inside the hint and update them in place.

Validate the resting, hovered, focused, expanded, empty, pending and failed states,
and the equivalent touch workflow, in light/dark and narrow/wide layouts. These
rules apply to lists, trees, cards and toolbars throughout the product.

## Surface hierarchy and section grouping

Use one enclosing surface for a coherent task. Inside a dialog, panel or card,
group related content with whitespace, aligned headings and, when needed, a
single subtle divider. Do not nest rounded, outlined containers merely to mark
another level of hierarchy: dialog → outlined page → outlined cards repeats the
same boundary, consumes space and obscures the content hierarchy.

Compose embedded content without page-level backgrounds, borders, corner radii
or duplicate title/padding layers. Use the shared `contentSection` for sections
within an existing surface. Reserve another enclosed surface for an independently
meaningful object or interaction, such as an input, code preview or selectable
item, rather than every information group. Controls retain their appropriate
boundaries and focus indicators. Prefer restrained background contrast or a
divider over stacking borders, shadows and backgrounds at successive levels.

## Every action provides feedback

Every clickable control, including icon buttons, list actions, toolbar controls and menus, must acknowledge activation immediately on the UI loop, before network, disk or Agent work. Hover is not click feedback and is absent on touch devices.

- Provide visible pressed/active and keyboard-focus states through shared components. Give icon-only controls accessible names and tooltips where supported.
- For asynchronous work, enter a local busy state immediately, expose it to assistive technology and show a stable spinner, progress indication or pending label at the action. Block duplicate activation when it would repeat work. Do not block unrelated navigation.
- Scope pending state to the operation and affected resource. Conflicting choices for one request share a gate; another request, session or settings action stays usable. A background completion must not clear another operation's pending state.
- Finish busy state on success, failure or cancellation. Show the result or actionable error. A no-op, unavailable action or offline state needs an explanation, not a silent ignored click.
- Keep form values and the initiating dialog available after failure. Prevent conflicting edits while submitting. Dismissing a submitted write does not cancel or replay it; a late result must not close a newer dialog, change a newer selection or erase a different request.
- Center text-only labels and icon-label groups within their buttons. Loading must not introduce asymmetric empty slots. Keep button bounds and component identity stable while pending; do not restart its animation on unrelated updates. Respect reduced motion. Do not add fake delays or fabricated progress to make feedback visible.
- Repeated indeterminate rings in one workspace share a single animation phase.
  A newly mounted or recycled indicator joins the current angle; adding, removing
  or refreshing another indicator must not restart the others. Use a shared
  platform animation, not per-row timers or UI refreshes. Release the animation
  when no indicators remain mounted and show a static indicator under reduced
  motion. This synchronization communicates activity, not equal task progress.
- Manual refresh shows local pending feedback until its request finishes, even when the response contains no changes. Automatic refresh stays visually quiet: it does not animate or disable the manual refresh button or repeatedly announce loading. Preserve existing list content and reading position. A manual refresh supersedes an older background read; stale background results cannot overwrite it.

Use the controller's shared [action controls](../../clients/nsgo/hubui/actions.go) and the website/account [Button](../../packages/account-ui/src/ui.tsx) instead of duplicating pending-state handling. Async button handlers return their operation promise so feedback covers the full request and follow-up refresh. Custom clickable rows also need pressed and focus states; selected appearance does not replace press feedback.

Copy confirmations last two seconds on the scoped UI clock. Repeating a copy
restarts that lifetime; the old timer must not clear the replacement confirmation,
and disposing the workspace cancels its pending timer.

## Refreshing lists

- Define a deterministic order with a stable resource ID as the final tie-breaker. Preserve relative order across equivalent snapshots. Reorder a surviving row only when the selected sort or a field that participates in it changes. Normalize unordered responses before binding them to a list.
- Key rows and their actions by resource identity. Treat equivalent snapshots as a no-op for rendering. Keep row and control instances mounted while changing their labels, icons and availability in place. Bind dynamic branches to the specific values that change their structure.
- Preserve hover, keyboard focus, scroll position and pending feedback during background refresh. Verify those behaviors with repeated equivalent snapshots in different orders and a metadata change, then verify that real membership or action changes still appear.

## Dialogs and notification layers

Email sign-in takes an address first, then a six-digit code. Allow users who
already have a code to enter it without requesting another email. Email codes
and numeric pairing device IDs use six visible cells backed by one accessible
text field. Size the six cells equally across the full form width, with consistent
gaps and both ends aligned to the other fields. Preserve leading zeroes,
whole-code paste and scanned device IDs.
Enable the submit action when all six digits are present.

Modal dialogs place an accessible X close control at the top right, reachable in normal, pending, failed and expired states. Keep the footer to at most two decision buttons by default; extra actions need a specific workflow reason. Do not add a third footer button merely to make the dialog dismissible. Informational or expired dialogs may have no footer actions. Use the shared dialog header; custom modal layouts follow the same placement.

Dismissal never implies consent. Expired or withdrawn requests stop offering invalid actions and explain how to start a fresh request; ordinary failures keep a usable retry and close path. Keyboard, back and backdrop behavior must agree with the visible controls. A dismissed request must not immediately reopen without a new user action or request.

Incoming remote-control authorization opens above any current workspace dialog. Keep the underlying dialog mounted with its draft and return to it when the authorization dialog closes. Only the top dialog handles modal input.

Global errors and notifications render above dialog backdrops using the framework's overlay layer, not a larger page-local z-index. Notification actions remain clickable while a dialog is open. The empty area around a notification passes input through without dismissing or blocking the dialog; opening, updating and closing notifications must preserve dialog state, drafts and focus. Check both stacking order and hit testing with a modal present. Keep notifications within the safe viewport on narrow screens.

Informational notices use the shared [notice lifecycle](../../clients/nsgo/hubui/notices.go): dismiss after five seconds and provide an accessible close icon. A new notice, including the same action repeated, starts a fresh lifetime; unrelated streaming or polling updates must not extend it, and an obsolete timer must not dismiss its replacement. Timers belong to the workspace and stop when it is disposed. Announce notices politely without moving focus. Time-sensitive platform hints may expire earlier (Android's exit confirmation uses two seconds). Actionable errors and ongoing transfers keep their own explicit resolution or cancellation flow; do not time them out as ordinary notices.

## Responsive layout

Choose layout by available viewport width, not OS, device name or a guessed mobile flag. [responsive.go](../../clients/nsgo/hubui/responsive.go) owns the current thresholds:

| Available width      | Navigation                                                   |
| -------------------- | ------------------------------------------------------------ |
| Below 650 points     | Unified device/session directory and conversation page stack |
| 650 points and above | One resizable unified sidebar beside the conversation        |

The 1000-point threshold only adjusts conversation surface and content spacing.
The sidebar defaults to 300 points, clamped for the available viewport.

Wide layouts provide a persistent, accessible sidebar toggle at the sidebar's
upper right. In native desktop windows it shares the title-bar row with platform
window controls; macOS traffic lights remain platform-owned and aligned with the
toggle. Align its horizontal center with the sidebar's trailing actions,
including after resizing. Continue sidebar/content surface colors through their
respective title-bar areas. Adjacent workspace panes share top and bottom edges;
do not inset only the conversation into another rounded, bordered card. The
platform owns outer window corners. Reserve the platform's title-bar insets and exclude buttons from window
dragging. After collapse, keep the restore control at the upper left; Web and
full-screen windows provide the equivalent toolbar. Remember the wide-layout
preference without hiding compact page-stack navigation. Toggling must preserve
the conversation/editor identity, draft, sidebar filters and reading position.

On desktop, put the selected conversation's title and contextual actions into
that same single toolbar row, alongside the native window controls. Do not add
another two-line header below the title bar. Show a folder/title disclosure on
the left and compact icon actions on the right; truncate long titles so controls
remain reachable. Put device, Agent identity, project and working directory into
a bounded details popover. Its trigger and actions must opt out of window dragging,
while unused toolbar space remains draggable. Polling and streaming must preserve
an open details panel, the transcript and editor; changing sessions dismisses
stale details. Web and full screen use the same toolbar composition. Compact page
navigation retains its touch-sized floating header and Back control.
Only remote conversations show a connection-status icon in their header, in both
wide and compact layouts. Local Agent execution has no remote transport; show the
host's service connectivity in the device overview instead.

Use shared breakpoints; do not introduce per-page alternatives without updating affected layouts and tests. Check just below, at and above each threshold, resizing and orientation changes. Preserve selected device/session, drafts, focus and reading position when switching layouts. Avoid horizontal page overflow, clipped actions and dialogs wider than the viewport. Content may shrink or wrap; primary actions and back navigation remain reachable.

Card collections such as attachment galleries use container-driven grids: a preferred minimum card width, automatic column repetition and equal flexible tracks, equivalent to `repeat(auto-fill, minmax(minimum, 1fr))`. Column count follows the collection's available width rather than OS or page breakpoints. Choose the minimum and gap for the content using shared spacing; example dimensions are not universal tokens. Clamp the minimum to the container width on very narrow surfaces, set card/text minimum widths to zero and truncate long labels without hiding actions. Preserve source order and stable resource keys as columns change; resizing must not remount cards, restart images or reset pending actions. Use `auto-fill` to retain empty column slots; choose `auto-fit` only when sparse collections should stretch into them. The controller's [cardGrid](../../clients/nsgo/hubui/responsive.go) shares this policy. Verify column transitions, sparse and dense lists, long labels and containers narrower than the preferred minimum. Compact inline chips do not need to become full card grids.

Respect safe areas, text scaling and the soft keyboard. Touch targets need enough hit area even when their icon is small. Check keyboard navigation, contrast, accessible names and non-color status cues. Platform window controls keep their platform style inside the custom title bar. Desktop close hides to tray; explicit quit exits. At the mobile root, back requires the existing second-action confirmation.

Keep the mobile display awake while an active conversation page is visible, including offline cached history. Release the display hold on navigation away, inactivity, backgrounding or disposal; restore it when returning. This is separate from background connection lifetime. Mobile browsers use Screen Wake Lock where supported in a secure context; denial must not block navigation or start an automatic retry loop.

## Scrollbars and scroll behavior

All application scrollbars are hidden at rest, appear only while scrolling and fade after scrolling stops. Use one shared thin overlay style without reserving layout width; do not expose a second native indicator. Apply it to device/session/message lists, settings, dialogs and other scrollable regions. Hover alone does not reveal a persistent bar. Preserve keyboard scrolling and accessible navigation.

[scrollbars.go](../../clients/nsgo/hubui/scrollbars.go) owns controller scrollbar policy. Do not copy its timing or sizing into individual pages. Scroll indicators are distinct from download/progress indicators, which stay visible while their operation is active.

A new touch interrupts inertia immediately in both session and message lists. While following the latest message, sending, content growth and keyboard show/hide preserve the bottom anchor. When the user intentionally reads history, preserve that position and offer the return-to-latest action. Viewport-height changes alone must not be interpreted as user scrolling. On conversation entry, ignore pre-layout scroll events and retain the latest-message anchor through initial history and content measurements. Explicit navigation declares history-reading intent before jumping; layout correction must not undo that jump.

## Unified session directory

The sidebar puts local/remote device controls and Add device above the session
list, without another session-title toolbar. Align trailing device, filter,
new-conversation and sidebar-toggle glyphs on one center line; short status text
must not push an icon onto a different axis. Device activation opens management; users open conversations directly.
Desktop starts with the local environment plus saved remote environments. Browser
and mobile aggregate remote environments. Cached sessions remain readable while
connections and live directories load independently. New conversation explicitly
chooses an execution device, a ready create-capable Agent and its working path.

Before a conversation is selected, the native desktop's right content pane shows
the local device name, remote connection status, device ID, QR/copy actions and
local Agent availability/enablement. Keep the aggregate sidebar mounted alongside
this overview. Its brand/home action returns to the overview while preserving
session drafts. Device management exposes the same overview in compact layouts.
Authorized controller identities and revocation live one level deeper in access
management; do not fill the landing page with the controller list. Agent activation
uses the same pending, confirmation and recovery flow as detailed settings. Missing
registration or Agent discovery must not imply readiness. Controllers without a
local host show the connect-device welcome instead.

Keep New conversation at the sidebar footer. A single settings icon opens the
account/settings/help panel; append the authenticated account display name when
available. Theme and manual refresh controls belong inside that panel/settings,
not in the resting footer. Account lookup must be bounded and optional: an
unavailable account service cannot block local or self-hosted navigation.

The top-level collapsible headings are ordered Pinned, Projects, then Recent.
Collapsed headings always show their right-pointing arrow; expanded headings
show their down-pointing arrow on hover/focus on desktop. Touch keeps the cue in
both states. Empty
Pinned is hidden. Pinned and Recent are flat sections. Session pin/unpin and
remove actions appear on desktop hover/focus and in the shared secondary-click
or touch-hold menu. New pins enter at the top; the user can drag pinned rows to
reorder them on desktop. Show a clear insertion marker without navigating or
replacing the active editor. Commit and persist the order on drop, and leave it
unchanged on cancellation. The same menu offers Up/Down for keyboard and touch;
disable moves past either end. Persist ranks for unavailable devices too, so
reconnecting does not reset the user's ordering. Pinned sessions
appear only in Pinned, excluded from Recent and all Projects grouping modes.
Pinning opens Pinned immediately; unpinning restores the session to its project
and makes it eligible for Recent again. Prune project/device/Agent groups with no
unpinned descendants, while retaining their expansion preferences for restoration.
If every matching session is pinned, explain that in the empty Recent/Projects
sections rather than implying that the sessions disappeared.
Recent contains all matching unpinned sessions across all execution states, including
completed sessions and offline cached entries, with no row limit. It follows the
entire Projects tree; collapsing Projects leaves Recent available immediately below
its heading. Keep the shared list virtualized. Pinned, Projects and Recent each
have an independent filter menu on the heading's right. Reveal the trigger on pointer
hover or keyboard focus, keep it visible while open/filtered, and show it by
default on touch. Opening a filter must not toggle the section. All three menus
filter devices, Agents and projects; only Projects offers grouping. Filtering
pins preserves their manual order and does not move them into other sections.
Keep Pinned visible when filters match no pins but the collection is nonempty.
Counts, reset and empty results belong to
the corresponding section; a filtered empty section retains its heading and
controls. Exclude pins and apply Recent's filters, then sort descending by `recencyAt`: the later of user-message submission
and successful Agent reply completion. These are fixed event times. Streaming
text, tools, metadata edits, polling and opening a conversation never advance
recency. Retain the newest confirmed boundary when older directory/history
snapshots arrive; keep status updates independent. Fall back to creation time
when both events are unknown, then sort unknown times last with an identity
tie-breaker. Never substitute mutable `updatedAt` or a controller receipt clock.
Simply opening a session does not change this order. Completion keeps the session
in Recent; live status indicators remain independent of section membership.
Pinned and Recent start expanded on every application launch, including after an earlier
manual collapse; collapsing them lasts for the current controller lifetime. Other
section and project-tree expansion preferences remain persisted. Expanded Recent
and Projects show concise empty states when they contain no rows.
At rest, session rows show a stable neutral spinning ring for running work, a waiting
indicator for input/approval, and a small brand-colored dot for completed unread work, following light/dark appearance.
Hover/focus replaces this status with pin/remove actions. Keep spinner identity
stable through unrelated updates and preserve the accessible status label.
A stable list key alone is insufficient: row construction must subscribe only
to identity, while labels, indentation, status and tooltip contents update in
place. Directory polling must not remount rows, reset animation progress or
close/reopen a hovered hint. Verify repeated metadata refreshes during an open
tooltip and across a full spinner revolution.
Completion dots are controller-local: a fresh observed running → completed/idle
transition marks a conversation unread unless it is currently being viewed.
Opening that conversation clears its dot across duplicate appearances; unread
markers survive restart. Collapsing a section or tree branch carries descendant
unread state to a single theme-colored dot on that visible parent. Expanded
parents leave the indicator on their visible descendants; expanding is not a
read acknowledgement. Apply each section's filters and pinned membership before
aggregation, and propagate through project, Agent and device levels. Opening a
conversation clears both its row markers and any affected parent markers.
On desktop, a collapsed section shows its unread dot in the far-right action
slot at rest. Hover and keyboard focus reveal the filter action in that slot.
Initial historical rows, failed/interrupted work,
unknown state and offline cache never fabricate successful completion. Each
Provider route tracks its own transitions so an idle duplicate cannot settle
another Provider's running session. Completion while this controller is absent
is not inferred from a historical update timestamp.
Projects contains all matching unpinned sessions, including Recent entries, and orders by
creation time descending with a deterministic identity tie-breaker. Unknown
creation times sort last and never borrow mutable last-activity time.

Only Projects is a tree: project (default), device → project, or device →
Agent instance → project. Matching host-supplied repository project keys can merge
across environments; other projects use device and full canonical project path.
Names alone never merge projects. Group ordering follows newest creation time,
then name/identity. All levels expand/collapse independently, with no five-row
truncation. Project groups use open/closed folder icons with the same foreground color in
both states; device and Agent groups
retain their respective single type icon. Branches do not display inline session
counts. Their information panel contains the name and matching unpinned session
count, plus the active count when nonzero. Active includes running and waiting
sessions on connected devices; offline cached activity is not counted. Project
panels also list distinct host-supplied repositories and associated directories,
including native project roots without a current session. Deduplicate paths per
device and add device names when a group spans devices. Apply the same filters
and membership as the tree. Hover previews show at most seven metadata rows and
an overflow hint; the context menu exposes the full bounded, scrollable list.
Project rows expose a trailing New session action on pointer hover or keyboard focus,
using the same SquarePen icon as the sidebar footer and context menu. Keep the same
action's icon consistent across entry points. Use a transparent background,
highlight and tooltip. It replaces a collapsed
project's unread marker in the same reserved space. Secondary click and touch
hold offer the same New session action; activating it must not toggle the tree
or navigate away from the current conversation. Open the shared creation dialog
with that project's device, Agent and project-root directory as defaults.
Aggregated projects retain a separate path per represented device; switching
devices fills that device's own directory. The device/Agent grouping scope and
current project filters determine the eligible project locations. Never copy a
remote path onto another device or silently fall back to an unrelated device
when a project's devices are offline. Keep the ready-Agent selector and editable
directory, preserve the form on failure, and clear project defaults for the
global New session entry.
Session rows show the Agent brand icon followed by the device name. Keep the
Agent name in the accessible row label and hover details rather than repeating
it beside its recognizable icon. Session tooltips use the same icon-and-text rows
for title, device, Agent, model, execution directory and Git branch. The branch
comes from the owning host's execution checkout, not the project group's main
checkout or the controller's filesystem. Detached HEAD shows a short commit ID;
absent Git metadata adds no placeholder. Refresh these values without replacing
the open panel or advancing session recency. Keep the panel bounded and themed,
with a border and shadow. Opening a context menu dismisses the hover tooltip.

Each section's filter icon opens a compact menu of Devices, Agents and Projects,
with Group sessions additionally available under Projects. Dimension submenus
show All and individual choices with checkmarks, scrolling within the available
space. Show paths or device details only to distinguish equal names. Omit a
separate title, hint paragraph and footer; Clear filters appears when needed.
Desktop menus support hover, click and keyboard selection; Escape closes one
level at a time. Narrow windows and touch use drill-in sheets with a Back row.
With NSGo `v0.2.0`, programmatic menu focus reaches the native/DOM keyboard
target on macOS and Web. Directional submenu navigation and Return/Space
selection are covered by platform tests, alongside Tab traversal. Keep these
checks on dependency upgrades; headless focus assertions alone do not validate
keyboard delivery.
Each dimension supports multiple selections (OR); dimensions combine with AND. An empty selection means all. Agent filters identify the
Provider across devices; project filters share the tree's repository/directory
identity, never the display name. Filter options come from the complete directory
so selections remain reversible. Pinned, Projects and Recent keep independent
selections, result counts, empty-result feedback and Clear filters.
Filters are temporary view
state; grouping, pins and expansion (except Pinned/Recent at startup) remain locally persisted. Filtering does not
change the selected conversation or its execution route.

Grouping and pin changes must preserve the selected conversation,
input identity and draft. Execution always uses the session's own environment
and Provider, never a group's display identity.

## Connection and message behavior

Reopen the last visited session from the local or refreshed catalogue on startup,
including when its device is offline. Explicit navigation takes precedence over
late restoration. A removed or unavailable session must not force navigation to
an invalid conversation. Persist unsent text, model choice and attachment bytes
per device/Provider/session, and restore them without depending on the original
file or remote upload lifetime. Keep navigation responsive during restoration;
disable edits until its result is known, and offer retry after a storage failure.
Consume the draft into the existing outgoing-message flow on submission; never
let an older read or send completion overwrite a newer draft. See
[controller persistence](architecture.md#controller-drafts-and-navigation).

Desktop message composers use Enter to submit and Shift+Enter to insert a native
line break at the caret or replace the selected range. Preserve input-method
composition, Unicode selection and undo; committing a composition must not send
the message. Mobile software Return inserts a line break and the explicit send
button submits. Keep editor identity and multiline drafts through background
updates and send completion.

Entering a conversation focuses its composer once, after its saved draft has
loaded and editing is enabled. Returning to another conversation requests focus
for that entry; streaming, polling and theme changes do not request it again.
Cancel pending focus with the conversation scope, and never let an older draft
restore move focus after navigation or beneath an open dialog. Verify actual
keyboard input without clicking or explicitly focusing the field in the test.

Code-block copy actions use transparent icon buttons with hover/focus emphasis,
an accessible name and tooltip. Replace the copy glyph with a check briefly on
activation. Keep their hit area outside selectable code and reserve space so the
button cannot cover the first line. Composer image thumbnails open the shared
large-image preview without sending or downloading the attachment. Removal is
a separate action anchored to the thumbnail's upper-right corner, within its
reserved hit area rather than a detached neighboring column. Hide the remove
icon at rest; reveal it on thumbnail hover or keyboard focus within, keeping it
visible as the pointer moves to the button. Secondary click and touch long press
offer the same removal without opening the preview. Preserve the
preview hit target and image identity while uploads progress or images reorder.
Preview download/close actions follow the same icon-button
rules; downloading local draft images works without a connection.

Device rows show availability, not connection stages or transport paths; failed presence reads are unknown, not confirmed offline. Offline devices remain accessible through cache. Connection status distinguishes connected, connecting and disconnected. Time only the current active attempt. Diagnostics distinguish active transport from prepared standby channels and distinguish network, pairing, authorization and Agent failures.

Send acknowledges immediately and retains a recoverable draft/pending message until acceptance. Explicit failures offer retry next to copy; unknown outcomes display pending confirmation and never offer a replay. Restored in-flight sends are also unconfirmed. Reconcile echoes even after an error response: a write can persist before its acknowledgement or presentation fails. Only a successful complete fresh history read begun after a definitely settled submission may remove an absent failed overlay; partial or paginated history cannot prove absence. Pending and failed overlays retain their original send position using persisted predecessor identities, including when predecessors receive remote IDs or disappear; later replies do not push them to the list end. Long-press copy and selection must survive background updates.

Agent-owned queued follow-ups are separate from delivery overlays and executed
chat history. Show them in order as compact rows immediately above the composer,
with one quiet section label, a single-line text preview and attachment presence.
An empty queue occupies no space. Use spacing rather than another nested bordered
card; bound the queue height and scroll additional entries. Activation by mouse,
keyboard or touch opens selectable full text and attachment names. Reveal the
details affordance on hover/focus, and keep it visible without hover. Preserve
the editor, its draft and an open preview across queue updates. Unknown/offline
state retains the last known list with a synchronization hint; only an explicit
current empty list clears it. Never infer queue consumption from matching text,
because equal prompts can be separate submissions. Queue actions require actual
Provider support; a preview must not pretend to send, edit or remove a message.

Initial and incremental history render received content progressively in conversation order. History catch-up dots float above the composer without changing list height and disappear when caught up, even if the Agent is still generating. Watch and history loading have separate states. Spinners keep their identity across updates; a typewriter effect must not conceal buffered delivery or delay already received text. Back, cancellation and connection details remain responsive throughout synchronization.

Separate process updates from the final answer using Provider message metadata.
While a turn runs, show its incoming commentary and activity. Once the turn is
terminal and a visible answer is available, fold explicitly marked `commentary`
and typed tool activity into one collapsed Work details entry per turn, at the
first process position. Subsequent steering messages do not create additional
process disclosures; their process records join the same chronological detail
list. Keep user/steering bubbles, all answer content and attachments in order; never
guess that an unclassified assistant message is disposable process output.
Without an answer, preserve the available commentary, including failed,
interrupted and partially synchronized turns. Expanding details uses a bounded,
virtualized list, retains safe activity metadata and never exposes raw tool
output or private reasoning. Refresh preserves the expanded state and final
answer identity; pending submissions retain their predecessor through folding.
When the Provider supplies the turn's recorded elapsed time, label the single
process disclosure with localized duration text (for example, `Worked for 2m 15s`).
Use seconds, minutes and hours as needed; show less than a second for subsecond
durations. Show the whole-turn duration once, including turns with steering
messages. Unknown timing keeps the neutral Work details label.
Never time history loading or sum overlapping tools to invent execution time.
Late timing updates change the label without remounting the disclosure or answer.

Message navigation runs in conversation order. Clicking a tick jumps directly to the virtualized target without animated scrolling through intervening history. Hover previews open promptly and show the instruction followed by the beginning of its matching Agent response, truncated to a compact size. Do not borrow a response from a later instruction. Keep navigation targets and open previews stable during streaming updates. Nearby ticks may form a short hover wave; honor reduced motion and retain keyboard access.

Tool activity summaries use text emphasis on hover: illuminate the whole label and icon without a full-row background. Preserve the running shimmer's identity, visible press feedback, keyboard focus and expand/collapse behavior. Animate only the tool group at the active turn's live tail. A later message or tool group stops the earlier group's shimmer; completion stops all of them. Historical groups must not imply concurrent execution merely because their turn is still running.

Attachments follow the same progressive disclosure rules as other contextual actions. On desktop, reveal the download icon when its file/image card is hovered or contains keyboard focus; keep its space stable and use foreground emphasis and an individual tooltip on the transparent icon. A secondary click or touch long press opens the same download action. Apply this to full attachment cards, inline images and compact user image previews. Overlaid image actions may use subtle contrast on the image surface, without an individual filled icon tile. Image activation opens its preview; download activation alone enters the save/download flow, with immediate feedback, actual progress, cancellation and errors. Keep those hit targets independent so download does not also open a preview, and holding or dismissing a context menu never activates the attachment. Unsupported file previews remain noninteractive content without button semantics or misleading hover feedback; file hyperlinks are explicit actions: a local execution route reveals the file
in the system file manager, while a remote route uses the ordinary attachment
download flow. Keep the theme accent and pointer cursor on message links.
Resolve relative local references against the session working directory and
ignore source-line suffixes for file-manager selection. A remote path never
opens a controller-local file; downloads require an attachment exported by its
Provider. Missing/unavailable files provide visible feedback. Ordinary inline file attachments still expose their download action in the
attachment collection. Source-location references remain compact inline links
without duplicating every referenced source file as a card. Preserve filenames and extensions. A corner toast alone is not adequate download feedback. User image inputs appear as small, right-aligned previews above their bubble; Agent attachments appear below the reply text. Keep preview and download hit targets separate in both layouts.

Upload cards enter a queued/pending state immediately. Show actual acknowledged progress, capped at 99% until the host confirms completion. Compact cards use percentage, progress and icons rather than status prose; provide localized accessible labels and tooltips. Preserve image identity during progress updates. Queue excess files behind the shared concurrency limit instead of rejecting them as busy.

Connection diagnostics use passive samples from existing authenticated traffic: one response-latency metric, transfer rate, pending requests and connection/data age. Response latency includes host processing and queueing; it is not network RTT. Do not present application errors as network quality or packet loss. The reliable data-channel transport does not supply a portable packet-loss percentage; omit it rather than derive it from RPC outcomes or unrelated audio/video statistics. Omit metrics unavailable for the active implementation; use an unavailable placeholder only for supported metrics awaiting a sample. Do not add probes, traffic or new polling loops solely for this panel.

## Web startup progress

Use one overall bar and percentage, without byte sizes or extra small bars. Within it, display real downloaded ranges at their segment positions so parallel requests are visible. Completed segments join without permanent gaps. Distinguish download, compilation and initialization; downloaded bytes do not mean the application is ready.

## Review and verification

Check light/dark, wide/narrow, touch/keyboard and the changed platform behavior. Exercise delayed, failed, cancelled and unchanged-result actions; verify feedback on the actual control rather than merely checking the request ran. Test breakpoint boundaries, long content, keyboard transitions, scroll anchors and idle/active scrollbar states. Supply relevant screenshots and use the [validation guide](validation.md). Native inertia and system-window behavior still need real-device checks.

## Agent questions

Present Agent questions as an explicit form, not a generic permission prompt or
raw tool JSON. Options remain unselected until chosen, with custom text available.
Activating an option confirms that answer immediately: advance to the next
question, or submit the complete answer set on the last question. Send/Next
belongs only to custom text, even when that text matches an option label.
Multiple questions use a compact step indicator and previous navigation; retain
prior answers when going back. Offer Skip
only when the Provider allows it. Use the theme color for the selected option,
neutral surfaces and a clear busy state on the activated option or custom Send.
Disable the affected form during its
submission, preserve its draft after definite rejection and keep other sessions
navigable. Drafts are scoped to device, Provider, session and request and remain
memory-only. Resolved/expired questions disappear; uncertain writes are not
replayed. The adapter owns vendor IDs and answer encoding.

Keep the form compact: one enclosing surface, a muted icon/header, the question,
closely spaced option rows and one shared response bar. Align option numbers with
the first text line, keep descriptions directly below their label, and show a
check in the numbered badge for selection. Reserve trailing space for hover/focus
feedback without shifting text. Number badges indicate order, not keyboard
shortcuts. Use a subtle row hover and the theme accent for explicit selection.
Do not show a redundant `1 / 1` step counter for one question. Put multi-question
back navigation in the header. Optional forms can expose an accessible close
action that skips all questions without submitting the draft.

Place the custom-answer editor and Skip/Send controls on one aligned response
bar, with a restrained input boundary and visible focus state. Use one shared
leading column for header glyphs, option badges and the editor glyph. Match their
visual size and alignment, not just invisible wrapper centers; account for
border insets in row padding. Balance an unframed glyph against the badge's inner
symbol, not its outer border; diagonal shapes can look oversized at equal bounds.
Keep the alignment column stable when adjusting the glyph size. Let the editor
shrink or grow vertically for text without pushing actions outside the card.
Use compact pill actions on pointer devices and retain touch-sized targets.
Avoid large empty gaps between options or a separate oversized footer. Keep
the editor, selection and pending state stable while other messages update.

Answered questions use the ordinary user-message bubble and its existing padding.
Show the question as muted context directly above the answer, with a small gap.
Do not add a nested card, check icon, status heading or labels such as “Answered”
and “Your answer”. Multiple question/answer pairs share the same bubble with
consistent spacing; internal request IDs remain hidden.

A submitted draft is consumed immediately into its delivery row. Late native
input echoes must not restore it. Key, paste and input-method actions begin a new
draft, including identical text; completion of the previous send never clears
that draft. Return uses the editor's final submitted value. Keep editor identity
and focus stable rather than remounting the field to clear it.
