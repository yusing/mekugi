# Native app-server UI

## REQ-NATIVE-UI-001 — Native app-server UI

An empty launch view welcomes the user with `Mekugi <version> • codex <version>`.
The versions identify the running executable and backend. Development builds
without revision metadata show `dev`; unavailable backend versions are omitted.
The welcome is presentation only, not conversation history.

Operation-row formatting and feed controls are specified by
[native activity presentation](activity_display.md); shared classification belongs to
[activity observation](activity.md), and router cost totals to [usage reporting](usage.md).

### Native app-server UI

Journal shares the auxiliary column with Diff and Activity (`Ctrl-B 5`). No bare
key opens it, so composer text that starts with any letter is typed. Its plan strip, tree navigation, transition rows and expandable
terminal card follow [journal presentation](journal.md#native-main-presentation).
Pane preferences retain its visibility and focus; transient selection and expansion
remain local to the active frontend.
New sessions default to Journal. Live child spawns and follow-up turns temporarily
show Activity without moving Main's keyboard focus. Journal returns after the last
outstanding child completes, fails, or is interrupted, unless the user explicitly
selected an auxiliary pane or the roster. Existing manual Diff/Activity choices are
not replaced on a spawn. Temporary Activity visibility is not persisted as a pane
preference; replay cannot revive pending child lifecycles.

`mekugi codex` uses the native client of `codex app-server` for interactive
terminal launches. Explicit `--yolo` remains required; without it startup rejects
before launching Codex. There is no legacy UI selection or fallback. It maps explicit `--yolo`, model, config, and feature-toggle (`--enable` / `--disable`)
arguments plus `resume`, `resume THREAD_ID` or `resume --last`, and rejects other interactive arguments rather
than ignoring them.
Router readiness, provider catalogs, invocation overrides, native recovery hooks
and frontend environment keep their owners; redirected and noninteractive
commands keep their original path. The client speaks newline-delimited stdio RPC.

After restoring the terminal on exit, the native client preserves existing draft,
diagnostic, and wrapper notices and prints a Codex-style token summary and
`mekugi codex --yolo resume THREAD_ID` hint for the established session, highlighted
in cyan on color-capable terminals (respecting `NO_COLOR`). The summary uses the
main thread's latest cumulative app-server usage, including restored usage on
resume, not child-thread or router cost totals. Displayed input and total exclude
cached input; cached and reasoning counts appear separately when nonzero.
Zero or unavailable usage omits the token line; no established thread omits the
resume hint. Noninteractive commands retain Codex's own exit output.
An unexpected app-server exit retains its diagnostic alongside the child's exit
status; the normal session summary must not make a disconnect look like a clean quit.
Temporary event bursts or paused presentation do not drop RPC messages or
terminate app-server. Transport buffering is bounded and applies backpressure
until the consumer catches up, preserving message order. Explicit close and
the graceful-shutdown deadline can release a reader waiting on that consumer.

The client replaces presentation, not projection policy. Codex remains the agent
runtime and execution authority, and Mekugi's router stays in the model-request
path; UI plumbing adds no model calls. The client connects to app-server, never
to the Code Mode host, and submits intent rather than executing tools.

| Concern | Owner |
| --- | --- |
| Tools, permissions, sandbox, native agents, Code Mode | Codex; the client submits intent and answers server requests. |
| Thread, turn and item lifecycle and history | Codex app-server; never reconstructed from rendered text. |
| Routing, projection, frontend PATH | The router and launcher, with invocation configuration carried into app-server. |
| Changes and recovery references | The capturer and replay store; native displays reuse their scoped receipts and previews. |
| Journals and delivery receipts | The [journal owner](journal.md); native events carry its records without new receipts or model-context insertion. |
| Tokens, prices, missing usage | Router accounting; app-server usage is never added to cost totals. |
| Tool classification | App-server's typed command actions and Mekugi classifiers; renderers never parse shell text. |
| Skills and guidance | The [guidance contract](guide.md) and router projection. |

`resume --last` resolves the most recently updated non-archived thread in the
launch directory through `thread/list`, including CLI, VS Code and app-server
sources across providers, excluding exec and child-agent sessions. Lookup failure
or an empty result exits without creating a thread.

Bare startup `resume` and in-session `/resume` open a session picker that
replaces Main. It lists saved threads through `thread/list` in pages of 25,
most recently updated first, with the same source kinds and providers as
`resume --last`; exec and child-agent sessions are excluded. It starts filtered
to the thread's workspace (the launch directory at startup); Tab toggles all
workspaces, adding a Directory column shown with the shared workspace-relative
path formatting, and restarts the listing. Rows show relative update time, Git
branch and the thread name, else its first-message preview; column widths come
from visible rows. Typing filters loaded rows by title, branch, directory and
thread ID, loading further pages until matches appear or 1,000 sessions were
scanned. Scrolling within five rows of the loaded end, or an unfilled viewport,
loads the next page. Only the latest request's page is applied, so a filter
change never mixes stale results. Loading, empty, no-match and failure states
stay in the picker; a failed page stops automatic paging until Tab restarts the
listing. Enter resumes the selected row; Escape first clears the
search. At startup Escape then starts a new thread and Ctrl-C quits without
one; in-session both close the picker and keep the current thread.

`/resume` and `/resume THREAD_ID` follow `/clear`'s availability: they are
refused while a task, submission or stacked input is pending, and the current
thread is marked in the list. Choosing it only reports that it is already
open. Switching uses `thread/resume` with the same invocation overrides as
startup resume. The current session stays live until Codex returns the chosen
thread; meanwhile input stacks locally and events for other threads are
buffered. Success unsubscribes the previous thread, retires its and its
descendants' late events, and then restores the chosen thread exactly as
startup resume does: Main, roster, Activity, pane preferences, and a saved Diff
rescoped to that thread and its descendants. After the rescope, observed model
requests join the Diff only when the thread or its parent is already in scope,
so the previous session's running children stay out. While any resumed thread
restores, streaming deltas from its descendants are not buffered; their
completed items carry the content. Returning to a previously left thread
readmits its events.
Failure keeps the current thread, replays its buffered events and returns
stacked input to the composer. Drafts are not kept per thread.

Startup `resume THREAD_ID` uses `thread/resume`, not a new thread or a replayed
prompt. The returned thread identity must match the requested ID; failure exits
without falling back to a new conversation. Main hydrates text messages and
command/edit items from the returned turns before accepting input, using the
existing retained transcript window. It hydrates after the roster's histories
are read, so child starts, assignments, messages and answers take their place
in Main as they did live. A failed turn, which live Main reports only in its
status line, leaves an error row with the recorded failure. Buffered notifications then reconcile by
item identity. Historical tools are display-only: they do not recreate live
edit previews, processes or delivery receipts. Subsequent input
starts a turn on the same thread; an active snapshot retains its steer/interrupt
target. Resume keeps the returned workspace and effective model metadata, with
journal sinks scoped to that thread. Explicit invocation model/effort settings
and the routed provider are forwarded as resume overrides; Codex owns their
precedence and reports the effective configuration. Full-history
resume is limited by the 16 MiB RPC frame cap; oversized histories fail rather
than bypassing the transport bound. Paginated hydration remains unfinished.

Resume also restores the Agents roster and Activity from Codex's observational
history APIs, including archived descendants. Names/roles, retained assignments,
messages, commands, edit descriptions and answers are presentation history;
children are not resumed and historical unfinished turns never imply live work.
Only completed collaboration items imply delivered assignments or messages;
other attempts retain their recorded status without claiming delivery.
Codex's history omits Code Mode cell results and delivered inter-agent
messages, so each thread's host-selected rollout supplies them when its session
metadata names that thread: failed-cell rows and the tasks and messages the
thread received, as live Activity observed them in its requests. Evidence
follows the host item it was recorded after, or, for another thread's
record, its completion time. Reading is bounded to each rollout's last 64 MiB;
older evidence shows only what the host's history does. A later live request
that carries restored messages again does not repeat them.
Usage is shown only when available from its existing owner, never reconstructed
from transcript text. Missing child history is marked incomplete without
preventing the parent conversation from continuing.

The saved Diff pane reloads the existing durable change projections for the root
and discovered child workspace/thread identities before any new model turn.
It does not derive edit evidence from app-server history or include unrelated
workspace threads. Subsequent requests retain that scope through the existing
automatic-diff owner. Retention gaps remain gaps, not successful recapture.

Activity hydration reports progress, buffers live notifications, and keeps typed
input as an unsent draft until reconciliation; `/quit` or Ctrl-C on an empty draft can exit while loading.
Discovery is bounded to 128 descendants and eight list pages, with an explicit
partial-history notice at the limit. Child histories share the current 16 MiB
RPC frame limit; paginated turn/item hydration remains unfinished. No additional
model requests or execution occur merely to restore pane content.

Native pane preferences persist separately from replay/correctness records under
`$XDG_STATE_HOME/mekugi/ui` (or `~/.local/state/mekugi/ui`), keyed by workspace
and Codex thread identity. Successful resume restores the Main/right-column
split, active Diff/Activity pane, keyboard focus, and diff navigator width.
Geometry is clamped by the current terminal layout; new threads and other
workspaces never borrow these preferences. Roster height remains automatically
fitted. Scroll positions, filters, selections, drafts and transient live docks
are not persisted in this increment.

Preference writes coalesce interaction bursts and flush pending changes on
orderly exit or cancellation. Files are private, atomically replaced, versioned
and bounded to 4 KiB on read. Missing state uses defaults. Invalid/unavailable
state reports a presentation notice but cannot fail resume, submit a prompt or
change execution. Simultaneous clients for the same workspace/thread use the
last completed preference write; no process resources are restored.

Approval controls and `/side` are deferred; pending server requests stay
visible and are never auto-approved. Not in scope: Codex's TUI, PTY emulation
or screen scraping for Main; a second execution, permission or Code Mode control
path; settings clones, onboarding, cloud tasks, voice; Git write actions or edit
rollback; browser frontends or remote hosting; new auth flows; a second
transcript store or cost calculator; model-visible UI commentary.

Real-time activity comes from app-server notifications, not from intercepted provider
responses: subagent activity and thread metadata name children by spawn path and role,
typed items supply commands, edits, collaboration calls, messages and reasoning
summaries, turn events drive each agent's state, and
`thread/tokenUsage/updated` supplies token counts. Cost stays with the router's
usage accounting. The client binds Main's thread in the router's activity collector for scoped
assignment/message and filter observations. Tool and lifecycle activity comes from
app-server, never generated commentary. [Router notices](notices.md) render directly
in Main and leave provider responses unchanged.

Run cards omit literal Bash, Zsh, or Sh `-c`/`-lc` launch wrappers and PowerShell
`-Command`/`-c` wrappers (optionally preceded by `-NoLogo`/`-NoProfile`), matching
Codex's shell recognition. The inner source uses the existing shell highlighting. Commands with outer redirects, assignments, additional arguments,
or dynamic wrapper words remain intact. This is display-only, including resumed items.
When Codex cannot classify a command, the shared shell display classifier identifies
frontend reads, inspections, searches and skill reads after removing a literal
launch wrapper. Literal `cat` output redirections and recognized inline Python or
JavaScript file writes show compact `Edit` intent rows marked `requested`, without
exposing the edit source or claiming saved changes. Heredoc batches classify each
operation, keeping trailing tests and other unclassified operations as Run entries.
Display intent inspection never reads target files or runs nested providers.
Captured shell effects replace the matching command row in Main and Activity
using durable thread and host-call identities, including after resume. Both use
the shared edit receipt formatter. Non-`apply_patch` receipts include a subdued
`cat`, `python3`, or other captured source label, separated by a middle dot.
Wrapped continuation rows preserve foreground colors and emphasis, including subdued source labels.
For tracked command lists, each edit intent changes from `requested` to
`completed`, `failed`, or `skipped` using its own segment outcome, without waiting
for later tests. `completed` reports command completion, not captured file counts
or success of the enclosing command. Untracked commands without captured effects
retain their requested intent or ordinary Run classification. A grouped capture is displayed once rather than
claiming per-command attribution. Successful sibling edit rows are omitted once
the grouped receipt covers them; real non-edit operations and failures remain
visible, without internal capture-bookkeeping placeholders. Incomplete capture paths are grouped in
one coverage notice with a `mchanges` reference, not labeled as confirmed edits;
the durable records retain each path and its reason.

The shell frames Main on the left and one right pane: the saved diff (2),
Activity (3), or Journal (5), each filling the pane. A roster (4) below them fits its
content, four rows unfocused and up to 40% of the screen when focused; finished
agents fold into one row. The roster immediately follows the pane borders, with one blank row below it
before the status bar when terminal height permits. Collapsed rows place activity directly after the name
without reserving name-column padding. Each row shows state, timer, tokens, cost
and turns, dropping from the right when narrow. Observed roles color the status
glyphs, with a color-to-role legend only in the expanded Agents pane; role labels
are not repeated in each row. Agent names retain their identity colors. Every pane has a title bar with
its tab number, focus and scroll state, and the status bar shows the tabs with
contextual key hints. Ctrl-B + number focuses a pane.

A blank row immediately above the composer separates it from the latest message
or auxiliary content, including the journal plan strip, when height permits.

`/live` opens an on/off picker for the Live pane; `/live on` and `/live off`
apply directly. This presentation-only preference lasts for the current frontend
session, defaults to on at launch (including resume), and never stops activity,
preview updates, or capture. Hidden docks reserve no space. Showing the pane
again displays any still-current cards, not expired work.

Streaming edits share one Live dock across Main and child agents, keyed by call
ID so caller-less completion and removal events reach the existing card.
Main's edits stay above Main's composer and never replace or shrink Activity.
While Main has a card, concurrent child edits share that dock. A child-only dock
occupies the top 35% of the right pane, leaving Activity or saved Diff below it.
When only Main fits on screen, the shared dock sits above its composer.
A new card shows for at least 1.5 seconds, so a quick, small edit does not flash;
once the last card's animation settles after that, the dock closes without
lingering. When no
cards remain, Activity or Diff reclaims the space. Shell or Code Mode projections
first received at completion do not open a transient dock; captured effects
remain in their receipt and saved diff. Concurrent edits use one accordion.
Main's live card wins automatic selection over the roster-selected child; Ctrl-B e
can explicitly cycle and pin another card until it completes. Pending exec scope
uses “may write”; once file differences are observed it uses “observed changes”,
without claiming exclusive attribution or successful command completion.
A recognized literal edit finishes its live card once the observed files match
the projected edit. A following test in the same shell command keeps its own
running status, not the edit's dock; test-only work opens no edit card.
Router previews of exec and Code Mode edits dock the same
way. All docks use the shared router preview owner, including its pre-execution
source snapshots; app-server file-change notifications never reconstruct patches
or re-read already edited files. A completed input stream remains explicitly
labelled as a preview, never a green success check; host item status and captured
effects establish the outcome. A new
saved diff never replaces Activity; the Diff tab shows an unseen badge instead,
and the saved diff lists its files or changes above the content when the pane is
narrow; focusing that list (Tab, s) enlarges it without covering the diff, and s
again hides it. A roster pick that changes Activity's agent filter shows
Activity in place of the saved diff.

Clicking a compact Edit event opens the shared dialog at the clicked file’s
captured diff, rather than the first file in a multi-file capture. This temporary file preview shows the exact
retained capture, not the combined result of later edits, so shifted or superseded
hunks remain inspectable. Only the pointed edit row's text underlines
on hover in Main and Activity, not its gutter, alignment gaps, stat bar, or other
rows in the same capture. Click-through content (Edit, message excerpts, question links, and journal
agent links) opens the shared content dialog above the existing panes. Its
close button or Escape dismisses it without changing pane filters, scroll
positions, or keyboard focus. New activity and captures remain available.
Edit pages use the exact host invocation, including grouped shell captures,
rather than matching nearby paths or edits. Roster picks remain explicit filters.

Successful single-line Ran output remains visible rather than collapsing to a
line-count toggle.

Main and Activity share the activity view's block parsing, operation grouping
and viewport logic; each keeps its own entries and follow/unseen state. Typed
app-server items update entries in place. Image-view items appear as `View`
with their workspace-relative path, including restored history.
Attached user images are inline model input, not image-view tool calls; they do
not synthesize a `View` event or claim that the model inspected them. Skill reads use
the colored `Skill` label; skill scripts display as `Skill  run …`.
Search rows show a muted `(N results)` when complete, attributable output supplies
a count: local result records (or explicit count totals), or a web result array.
Missing, truncated, failed, or ambiguous local output has no inferred count.
Main renders user messages on a tinted
band, assistant text under one `main` heading, tool runs drawn as a tree, agent
start/message/finish events labelled `sender → recipient`, final answers as
cards, and journal blocks. Its composer supports a new thread, submission, steering, queueing and
interruption. Typing `?` in the empty, focused composer opens keyboard shortcuts.
The view docks directly above the composer, temporarily hiding Main's live-edit
dock. It uses bold group headings and blue key labels, with Compose, Session,
and Transcript columns that stack when the pane is too narrow.
`?` or Escape closes them, and other keyboard input dismisses them while retaining
its normal behavior. A mouse event dismisses help without acting on the hidden
transcript. Question marks in non-empty drafts and bracketed paste remain
literal input.

At a composer word boundary, `@` opens the file picker and `$` opens the skill
completion picker. Both dock above the composer and hide its live-edit dock
while focused. Up/Down or Ctrl-P/Ctrl-N wrap selection,
Tab or Enter inserts the selected result without sending, and Escape dismisses
without deleting the token. Bracketed paste does not open a picker. File paths
with spaces are quoted. Selected files retain the `@` prefix as highlighted,
atomic composer tokens, including navigation, deletion, undo, local input
history, and rejected submissions. On submission or queueing, selected text files
are snapshotted from the active thread's absolute workspace. The model receives
the prompt with its inline `@` tokens, followed by separate user messages with
path and byte-range frames containing the file contents. Repeated paths in a
draft attach once. Line boundaries are preferred when splitting; long rows split
only at UTF-8 boundaries, without dropping bytes. Supported images attach through
the existing image composer;
skill selections retain their exact path as structured
Codex skill input through undo, local input history, and rejected submissions.
Complete enabled `$name` references also bind automatically after a word boundary
or on submission, including pasted prompts. Names must match exactly and have
one enabled path in the active workspace's Codex catalog. Unknown, disabled,
ambiguous names and shell variables stay literal. A pending catalog lookup delays
submission rather than silently losing the skill; lookup failure is reported and
leaves the references as text.

Single-unit spans use shared logic for every bound token kind, including file
references, skill references, selection mentions, and `[Image N]` placeholders. Navigation and deletion
never split a token. Composer and submitted-input rendering keep a token on one
row when it fits; oversized tokens wrap only at grapheme boundaries without losing
content. Token types have distinct colors: images are magenta, files green,
skills amber, and selection mentions cyan, consistently across composer and transcript. Literal lookalikes do
not acquire attachment identity.
File and skill span byte ranges travel in Codex text elements, relative to each
text part, so host echoes and restored history retain their presentation without
guessing from prompt text. Structured skill metadata also restores complete,
unambiguous skill references when text elements are absent; explicit text
elements take precedence for repeated identical labels. Without either kind of
metadata historical lookalikes stay literal.

File snapshots travel in Codex-owned input history, not a router-lifetime lookup.
Queued and restored input, fork/resume, and provider switches retain
the submitted content without reopening files. The native transcript and recalled
prompt hide transport framing. A recalled draft with live file bindings takes a
fresh snapshot when submitted again. Attachment contents are not journal question
text and never become developer or system instructions.

Each draft has a 192 KiB encoded attachment budget, with half reserved for omission
notices; file reads are bounded to 96 KiB and framed content chunks to 24 KiB.
Unreadable, non-regular, non-UTF-8, NUL-containing, and oversized files are not
silently truncated: the model receives explicit omission notices and the composer
reports the first omission. Each submitted user item shows file outcomes before
agent activity: an `Attached` operation per included file, or `Attach failed`
with the file and omission reason. Chunked files appear once. These operations
render paths without surrounding quotes, using the existing shared path formatting
(workspace-relative paths, subdued directories and emphasized filename), not
transport-envelope syntax. Paths outside the owning workspace stay absolute. They
are restored from submitted snapshots on resume, without reopening files or
displaying their contents; rejected submissions do not claim attachment success.
A stacked submission with attachments must fit a conservative
1 MiB UTF-8 text budget, including encoded snapshots. Larger submissions remain
unsent and return to the composer with an actionable notice. These bounds limit
attachment growth; they do not promise that an arbitrary pre-existing conversation
fits a provider's context window.

Ordinary file lookup uses Codex's ignore-aware file-search API. `@!` includes
ignored files using a cancellable, debounced, read-only scan because Codex's API
does not expose an ignore bypass. Both modes exclude VCS metadata directories
(`.git`, `.svn`, `.hg`, `.bzr`, `_darcs`, and `CVS`). Lookup is always rooted in
the active thread's absolute workspace, never the router working directory.
The native scan does not traverse directory symlinks. Search updates coalesce
while a request is pending; stale or canceled responses never replace current
results. Loading, empty, and failure states remain visible without polluting
the conversation.

Typing `/` at the start of an otherwise single-token draft opens a local command
catalog with descriptions for `/compact`, `/clear`, `/resume`, `/btw`, `/status`, `/copy`,
`/model`, `/effort`, `/reasoning`, `/tier`, `/live`, `/skills`, and `/quit`.
Typing filters commands with fuzzy matching; Up/Down selects, Tab
completes without executing, Enter runs the selected command, and Escape closes
the catalog without changing the draft. Arguments close completion. Pasted text
does not activate the catalog. The catalog uses the shared picker viewport and
visible-row column sizing; it never submits a command to the model.

`/copy` opens a local picker for the latest completed response in the active
thread, including restored history and journal answers. Whole response preserves
the Markdown; fenced code blocks and blockquotes are individually selectable.
Bounded previews never truncate clipboard content. Streaming fragments and other
threads do not replace the last completed response. Enter copies; Escape cancels;
an empty history reports that there is no response to copy. Copying remains
available during a turn without interrupting it. The picker, selection Copy
(`c` or Ctrl-C), and link copying share terminal clipboard delivery and feedback.
Delivery uses OSC 52 and reports a request sent, not confirmed clipboard access.

### Side questions

`/btw QUESTION` asks a temporary side question without switching away from Main,
steering it, interrupting it, or adding side messages to its conversation. This is
not `/fork` or `/side`: only the side question and streamed answer appear in a
bounded Markdown dock above the composer, alongside queued input. Ordinary
composer input continues to target Main. `/btw QUESTION` after completion follows
up in the same side conversation and replaces the dock's displayed exchange.
While the side answer is running, another `/btw` remains an unsent draft with a
notice. Bare `/btw` explains the required question rather than submitting input.

Codex owns the ephemeral snapshot through `thread/fork` with `excludeTurns`.
A main submission awaiting acknowledgement settles before the fork request;
an already-active turn is neither awaited nor canceled. The boundary is the
history Codex snapshots when it handles the fork, not the keystroke time or an
unfinished streamed token. Later Main activity does not enter side follow-ups.
The fork uses Main's effective model, reasoning effort and service tier, including
live setting changes and resumed sessions, rather than launch-time defaults.
The side turn asks for a context-only answer, disables environment access, and
uses read-only permissions with no approvals. App-server does not expose a
general no-tools policy; non-environment tools are not guaranteed to be absent.

Side responses, errors, usage and server requests do not enter Main's transcript,
status, questions, notifications or roster. Fork and submission failures stay in
the dock. Rejected input returns ahead of any newer composer draft, preserving
attachment tokens for editing; file omission notices remain visible in the dock.
Main keeps its request history, settings and cache identity. Codex owns
side cache affinity, which may share Main's key despite a distinct thread identity;
actual cache reuse is provider-dependent. No router cache-key rewrite or reconstructed
conversation is used to simulate a branch.

The fork alone disables its provider's Codex-facing WebSocket capability using a
thread-local config override. This skips Codex's redundant startup model prewarm
when the side question is already ready and streams the turn to the router over
HTTP. Main's transport, provider/auth configuration, and the router's upstream
WebSocket support remain unchanged. No warmup response is fabricated or discarded.

PgUp/PgDn scroll the side answer while the dock is open. Esc, after dismissing
active questions or command menus, closes the dock without changing the main
draft or canceling Main. It cancels only a running side turn and unsubscribes its
thread; delayed fork/start acknowledgements still finish that cleanup and cannot
replace a newer dock. The display retains at most 256 KiB across 256 answer items
and reports truncation; Codex retains the full side context for follow-ups.
The dock is not persisted or restored on resume, and closing it makes the next
`/btw` take a new snapshot. No processes or handles are replayed.

### Session status

`/status` opens a temporary, bordered session panel with fixed close/scroll
controls, grouped sections, aligned values, and colored remaining-usage gauges.
The layout adapts to narrow panes; long values wrap and the body scrolls without
moving its controls. Drag-select visible text and use the shared Copy action
(`c` or Ctrl-C); selection excludes panel borders and gauges. Escape first clears
an active selection, then closes the panel. Closing also discards its selection.
Colors follow the terminal light/dark theme, while numeric
percentages keep gauges readable without color. The panel includes known model,
reasoning, service tier, provider, directory, session identity, approval policy,
sandbox, loaded instruction sources, and context remaining. It works during a
turn without sending or steering model input. Missing values are omitted, not
represented as zero usage or an unknown-state label. Account information refreshes
through `account/read` without refreshing credentials; ChatGPT accounts additionally
refresh `account/rateLimits/read`. The same panel updates asynchronously with the
account/plan, each reported quota window's percentage remaining and local reset
time, credits, and the usage-page link. Non-ChatGPT sessions show observed input
and output tokens instead. Refresh failures stay on the panel without replacing
turn status. Escape, Enter, q, or Ctrl-C closes the panel without interrupting a turn.
Closing leaves no transcript entry; late responses cannot reopen or update a
new panel. Arrow keys, mouse wheel, and PgUp/PgDn scroll the panel independently
of the transcript. Status is presentation only and does not change configuration,
permissions, or router cost accounting. Detailed usage-based billing, spend-control
banners, and reset-credit redemption are outside this command's scope.

`/skills` replaces the composer with Codex's numbered Skills action menu:
List skills inserts `$`; Enable/Disable Skills opens a searchable management
view showing enabled and disabled skills. Arrow keys navigate, printable text
filters by display or canonical name, and Space or Enter saves a toggle through
Codex's path-scoped skills configuration API. Checkboxes change only after a
successful response, failures remain visible, and Escape closes the view.
The catalog reloads after management closes. Column widths use only the current
visible rows; off-screen names never widen columns or hide descriptions.
Selected rows, aligned columns,
overflow indicators, descriptions, and footer hints follow the stock picker;
the menu and management layouts are checked against Codex snapshot fixtures.
Plugin browsing is outside this file/skills picker scope.

Dragging across text in Main, the composer, Activity, or the saved Diff selects the
visible text and offers Reference (R), Copy (Ctrl+C, also C), and Clear (Escape) in
the status bar. Selection actions and the existing pane shortcut bar share bold key
labels and bullet separators. Composer selection excludes its prompt and borders;
Diff selection covers only the source column, excluding the file navigator, gutters,
and line numbers, and keeps each row's `+`, `-`, or space marker. A press in the Diff
still reaches the pane, so clicks keep their meaning.
Reference inserts a concise mention at the composer caret without submitting:
`[Selected message]` from Main, `[Selected activity]` from Activity,
`[Selected text]` from the composer, `[Selected status]` from the status panel,
and `[Selected diff hunk @amber1:42-45]` from the Diff, naming the selected
rows' change and gutter lines (`:42` for one line). Lines are new-file
coordinates, or old-file ones when only deletions are selected. A composed file
names its changes comma-separated; rows from several changes read
`[Selected diff hunks @amber1 +N]`, and rows without a change read
`[Selected diff]`. A label already in the draft or in queued, unsent, or
steering input gains a counter, such as `[Selected message 2]`. The mention is one
atomic cyan composer token through navigation, deletion, undo, queueing, input
history, the external editor, and rejected submissions, and the host echo keeps it
as a styled transcript token through its text element. After resume, recalled
input rebinds a mention to its submitted quote when its label occurs once.
On submission or queueing, each mention's selected text follows any file contents
in the same versioned attachment envelope, as its own user message framed with the
label and, for Diff, the file path. A selection over 96 KiB is refused with a
notice; one that exceeds the remaining envelope budget is replaced by an explicit
omission frame, and the composer reports it. While a question is open, Reference
inserts `> SELECTED_TEXT\n\n` instead, since an answer is plain text.
Copy requests the terminal clipboard via OSC 52, and Clear leaves the draft intact.
The selected viewport stays stable while the selection is active; resizing,
scrolling, or resuming editing dismisses it. Clicking a Markdown absolute local
path or HTTP(S) link copies its destination (a local path retains literal spaces
and its line suffix), rather than opening it. Clipboard availability is controlled
by the user's terminal.
Arrow keys move the insertion caret across graphemes and displayed
rows. At the first/last displayed row, Up/Down recalls older/newer submitted
input, restoring the draft and caret after the newest entry. History is bounded
to 100 entries per thread; live entries retain attachments, while resume hydrates
text only from Codex user messages. There is no additional durable input store.
Ctrl+Left/Right and Alt/Option+Left/Right move to the previous word start or next
word end, skipping whitespace and punctuation in the travel direction.
Ctrl+Up/Down move to logical line boundaries. Word navigation accepts modified-arrow sequences and
the Meta-b/f sequences emitted by macOS terminals, including over remote sessions.
Shift+Up/Down steps through model-advertised reasoning levels without wrapping.
`/model`, `/effort` (also `/reasoning`), and `/tier` open local pickers
with model-advertised choices, or accept an explicit value. Arrow keys navigate,
Enter applies the selection, and Escape cancels without changing settings.
The current confirmed value is initially selected when advertised. Loading and
unavailable choices remain in the picker. Commands and choice lists never enter
the transcript or submit a prompt.
`/tier default` clears the requested service tier. Codex validates explicit values.
The native app-server invocation enables Codex's `step_model_switching` and
`reasoning_effort_override` features without writing user configuration.
The client submits `thread/settings/update` and waits for the scoped
`thread/settings/updated` notification before showing confirmed settings or
starting dependent turns. When the same turn is still active, it also submits
`turn/settings/update`; applied publication affects subsequent captures only,
while `targetUnavailable` reports next-turn-only success. Errors remain visible;
no optimistic setting is reported as confirmed. Unchanged defaults do not wait
for a notification that Codex suppresses; an active turn can still be updated.
Child threads are unchanged.
`configuration_update` reasoning history items remain Codex-authored, subject to
its model capability gate; the client never injects them or changes user config.

Alt+Backspace/Delete remove the previous/next word using the same punctuation-aware
boundaries without splitting image attachments. Option+Backspace also accepts the Ctrl+W encoding
used by macOS terminals. Ctrl+K removes text to the logical line end, or removes
the following newline when already at the line end; each press is undoable.
Editing and the visible composer window follow the
caret. Ctrl+V reads a PNG
image from the desktop clipboard and inserts a highlighted, atomic `[Image N]`
attachment. A bracketed paste that is exactly one absolute path to a PNG, JPEG
or GIF file (plain, shell-quoted or escaped, or a local `file://` URL) attaches
that file the same way, followed by a space; the file stays user-owned and is
never removed. Other pastes insert as text. Images are individual units for character and word navigation and
deletion, including when immediately adjacent to text. Ctrl+Z undoes and Ctrl+Y
redoes draft edits; each typed word, run of Backspace or Delete presses,
bracketed paste, attachment, draft clear, and editor save is an undoable unit. History is bounded to 100 edits and resets on submission.
Ctrl+G opens `$EDITOR` (then `$VISUAL`, then `vi`) with a temporary draft file,
after releasing the terminal and input reader. Returning restores the UI without
submitting. Exact image placeholders retain their attachments when moved or
removed in the editor; duplicate placeholders reject the edit. Editor failure
keeps the original draft and reports the recovery file.
Images are numbered in draft order and sent as `localImage` inputs, leaving image
XML framing to Codex. Failed submissions restore attachments with the text.
Submitted and restored attachments retain the composer's highlighted `[Image N]`
labels. Attachment-bearing messages preserve literal composer text; text that
only resembles an image label is not highlighted as an attachment.
Unsubmitted image files remain while referenced by the draft or undo/redo history,
then are removed, including on exit; submitted files
remain in temporary storage for Codex history/resume. A started turn's text appears
in the transcript immediately and is reconciled with the server's user message
without a duplicate; rejection restores the draft, followed by any input stacked
or queued behind it.

Starting a draft with `!` selects Shell Mode, with an explicit label and red
composer frame. Enter or Tab submits the command through Codex's native
`thread/shellCommand` API, not as a model prompt. Shell syntax passes through
unchanged after removing the leading `!` and surrounding whitespace. Shell mode
disables file and skill completion; attached images and picker tokens must be
removed before execution. An empty command stays in the composer with a hint.
Codex runs it with full access using the thread's shell and workspace, streams
its output, and owns interruption and durable command/result history. While
Main is working, Codex injects the command and output into that turn. Otherwise
they remain in history for the next user input without starting a model reply.
Ordinary input submitted during a standalone shell run waits for its completion.
Rejected submissions return to an empty composer with an error; if a later draft
exists, the rejected command is saved separately in input history instead of
turning that draft into executable shell text. Acknowledgements do
not claim command completion. Removing `!` returns to normal compose mode.

The host's opt-in `instant_interrupt` feature lets new input preempt model
responses and yield long-running Code Mode calls without terminating their
cells. Native launches forward feature toggles unchanged, including on resume;
Codex owns validation, precedence, preemption, and continuation. The client uses
the same `turn/steer` path whether the feature is enabled or disabled and never
implements instant steering by aborting a turn or replaying a tool.

`/compact` asks Codex to compact the current conversation. While busy it appears
in the pending-input list and runs after the active turn; it never replaces that
turn or becomes model input. It separates surrounding text batches, and later
input waits until compaction finishes. Interrupt restores a still-queued command
along with other unsent input. Host compaction progress and failures remain visible.
`/clear` clears Main, Activity, and journal presentation and starts a fresh Codex
session with the current workspace and model settings. It is unavailable while a
task or submission is in progress. Existing saved threads and filesystem changes
are not deleted; a failed fresh-session request keeps the old transcript usable.
While the new session starts, model-setting commands, shell commands, and side
questions keep their drafts for retry rather than targeting the old thread.
Late events from the previous session cannot repopulate the new transcript.

Busy input follows Codex's composer queue. Enter while a turn runs steers it;
Tab queues input for the next turn and, while idle, sends like Enter. Input that
cannot be sent yet stacks locally instead of being refused: steers typed while
another submission is unresolved, the turn is starting, settings are applying,
or an interrupt is pending; and every queued entry. Each stack is sent as one
message whose entries are separated by newlines, keeping each entry's
attachments and tokens. Unsent steers go before queued input, and queued input
starts only after the running turn ends. Waiting and uncommitted steers, then
queued entries, are listed above the composer (at most three rows per entry and
half of Main) until Codex's completed user message commits them by
`clientUserMessageId`, or by text when the server does not echo one. Alt+Up or Shift+Left moves the last queued
entry, or else the last stacked steer, back into the composer ahead of the draft.
Consecutive user items in one turn appear as one prompt in the transcript,
with each item's own navigation target retained.
Stacked image files stay owned until sent.

Escape in the focused composer interrupts an active turn without clearing its draft
and never quits. Dismissing a picker, help, or selection and returning a paused
transcript to the bottom take precedence.

`/lock` protects against keyboard interruption: composer Escape and Ctrl-C cannot
interrupt active or starting work or discard waiting input, and Ctrl-C cannot exit
with an empty draft. `/unlock` restores those shortcuts. Draft clearing, undo,
contextual dismissal and copying remain available. `/quit`, explicit commands and
normal steering retain their behavior. A persistent Locked indicator includes
`/unlock`; the preference is restored for that workspace/thread on resume. Host
lifecycle events and an already-requested interrupt are unaffected.

Ctrl-C first clears a non-empty draft (Ctrl+Z restores it), then interrupts the
active or starting turn, and exits like `/quit` only when no input is waiting.
Interrupt restores uncommitted submissions, locally stacked steers, and queued
entries ahead of the current draft, preserving attachments and typed order;
nothing is automatically resent. A committed user message stays in the transcript.
An interrupted start that Codex has not committed removes its provisional
transcript entry. If it was the first message, Main returns to an empty transcript
and Ready composer with that draft restored. An interrupt requested before the
host supplies the turn ID waits for that ID rather than inventing one.
A rejected steer, or a steer whose turn ended without committing it, also returns
to the composer. Submissions settle after their in-flight acknowledgement, so
restoration cannot reorder or duplicate input. On exit, stacked input is
printed with the unsent draft, and unresolved or uncommitted submissions as
outcome unknown, never resent. Composer notices (command, paste, editor and
Ctrl-C feedback) follow the turn state on the composer border without replacing
it. Non-error feedback clears after three seconds or the next draft edit;
actionable errors remain until editing. Only
`/quit` is a command, and only while idle;
unknown commands are reported, never sent as prompts. The
composer border carries turn state, the model, and Main's context usage; Main's title bar carries the
scroll position and unseen-message count. History
beyond the retained window is not hydrated. Unexpected server requests stay
visibly pending, never auto-approved.

Activity shows only child agents; Main stays in the roster for status and usage.
The composer and agent roster show `used/window • percent%` from the latest
Codex `tokenUsage.last.totalTokens` and `modelContextWindow`, independently of
cumulative usage totals. Before the first usage report, show `0%` used, equivalent
to Codex's initial `100% context left`; a missing window shows `N used` without
a percentage or placeholder. New host reports replace the context snapshot,
including reductions after compaction and replayed usage on resume.
Startup and child metadata also restore the latest saved context snapshot from
the absolute rollout path supplied by Codex, validating its session identity.
This observational read never resumes children or replaces newer live usage.
Restoration reads at most the last 8 MiB and a 64 KiB metadata prefix; absent,
unreadable, mismatched, or unavailable snapshots leave the initial display intact.
Roster edit counts use green for additions and red for removals, independent of
the syntax theme. Show known captured edits only: incomplete captures must not
hide confirmed counts from the same agent or other agents. Omit each zero or
unknown count, including in session totals; detailed capture gaps remain in Diff.
Narrow roster rows retain context before other metrics;
the composer's bottom border shows `model (effort) • used/window • percent%`
as one right-aligned caption, shortening or omitting the model first when narrow.
Working roster rows show the latest operation or public summary, including its
target, rather than a generic running/working label. Child names use their canonical
spawn paths and roles use Codex metadata even when no `thread/started` notification
arrives. Late metadata preserves already observed activity and selection; fetching
it must neither resume the child nor load/replay its history.
Each agent run has one heading with the agent's role and start time. Its events
put a short label on its own row, such as the started model, message direction
or answer, then the body at full width, separated by blank rows so narrow panes
stay readable. Directed Main/agent messages appear at both ends. Native
assignments, including follow-ups, keep their own identities; spawn and its first
prompt form one event, and full-history requests do not replay them. In Main,
child replies are short excerpts with a link that activates Activity, selects
the owning agent, and scrolls to that exact reply. The full message stays in
Activity. Assignment excerpts sit below their link on separate, wrapped quote
rows. Excerpts skip paragraph gaps, and truncated excerpts end their last text
row with an inline ellipsis, not a separate ellipsis row.
Adjacent Main items of traffic with one agent form a thread under that agent's
gutter, with no spacer rows. Main reasoning summaries between those items do not
end the thread; they render after it closes, outside its rail. Main's tools
never follow agent traffic headless: when traffic arrives between Main's
reasoning or commentary and its first tools, that item moves below the traffic
to head them; when it already heads earlier tools, a one-row `continued`
heading naming it precedes the later ones. Later items replace the agent heading with a
connector naming the event: replies, completions and failures show how long the
agent took, while follow-ups keep their time. The thread's latest item keeps the
longer excerpt; items the thread has moved past shrink to two rows. A reply's
Activity link counts its omitted rows and closes the thread when nothing follows
it. A shrunk assignment ends with a right-aligned count of its omitted rows;
clicking it opens the full assignment in the shared dialog.
An unanswered assignment stays in full while in view. Once a spawn, follow-up
or Main message has scrolled above the viewport, it becomes an excerpt in the
reply format: the same row budget and a link (`↩ Open assignment`
or `↩ Open message`) that counts the omitted rows and opens that
exact entry. A message the excerpt would not shorten stays in full. Shrinking
above a scrolled-up viewport does not move the visible rows.
Transcript blockquotes use a vertical rail rather than literal `>` markers,
including on wrapped continuation rows, and retain inline Markdown styling.
Child answers link (`↩ re:`) to their retained assignment, not to a
previous answer or similar text. An answer to its thread's latest assignment
omits the link, since that assignment is directly above; an answer to an earlier
assignment in the same thread keeps it. Main's ordinary replies link to the user input
in their own turn. Only the first reply to an input quotes it; a later reply to the
same input quotes it again only when another message sits between them. Main's own
tools, reasoning and progress do not separate replies. While Main is working, its latest ordinary reply is pinned
at the top of Main only when the original is entirely outside the unpinned
transcript viewport. Any visible part of the original suppresses the pin, avoiding
duplicate messages on screen. The copy
is bounded to leave activity visible; the full reply and its links remain in the
transcript. Very short panes omit the copy. A newer reply replaces it, and ending
the Main turn (completion, interruption, or failure) removes the pin. Main has one
pin: a journal state change pinned above the composer that is newer than the
latest reply replaces the reply pin until a newer reply arrives.
Replies following a committed question answer quote `re: your answer` and link
to its Asked record, not the earlier task prompt. The answer envelope never
creates a duplicate user-message band, including after resume.
The `re:` label includes the target text; if only the question
text is retained, it remains text rather than a fabricated navigation target.
Hovering a navigable link underlines it; clicking it scrolls to and briefly
shades the target. Cumulative child journals display each answer ID once,
preserving its original assignment across follow-ups. A successfully completed
child turn without a final answer promotes only that same turn's last message.
Codex V2 activity notifications carry no directed-message or assignment body.
The native UI supplements them with the router's authenticated recipient-input
observations, retaining plaintext message bodies and assignment identities
exactly once. Native message display has a separate 64 KiB limit, with an explicit
clipping marker above it. There is no inline commentary excerpt path.
App-server still owns tool execution and agent lifecycle. Legacy
received envelopes are not injected into either parent's or child's provider output.

Native UI dimming uses ANSI faint when supported. `--ansi-faint=auto|on|off`
selects the terminal-local policy: `auto` disables faint when `mosh-server`
appears in the process ancestry, otherwise enables it; explicit `on` or `off`
overrides detection. Detached multiplexers can hide transport ancestry and
require `off`. No unrelated sessions are scanned. Unsupported output uses
256-color index 243 for ordinary dimmed text and static muted variants for
agent identity colors. This applies to metadata, secondary labels, separators,
and diff coordinates; non-dimmed semantic colors, syntax highlighting and
animated status ramps stay unchanged.

Slice plans use the [journal continuation policy](journal.md#slice-continuation).
The pinned countdown names the next path and title and shows `Esc cancels`.
Context-reset events remain visible even with the Journal pane open. Router-authored
continuations appear as auto-continue rows, not user bubbles. Pending user input
cancels an undispatched continuation rather than competing with it.

Host progress has one presentation mapping for live events and restored history.
Compaction start replaces Main’s `Working` label with `Compacting context`;
completion restores ordinary turn status and adds a `Context compacted` event,
without a reply-context (`re:`) line. Turn completion, failure, or interruption
clears an unfinished compaction without claiming it succeeded. Child compaction
does not change Main’s composer. Agent wait starts, completions, and failures appear
only as the working caller's latest roster status, never in Main or Activity transcripts
or as composer overrides. Replayed events do not restart progress.
Wait status shows named targets using the shared agent display format (`main`,
or the spawn path below `/root/`) and static muted variants of their identity
colors when faint is unsupported, or their normal identity colors with ANSI
faint otherwise. When the host omits receivers, the start event
snapshots the caller's currently running descendants; completion retains that
list even if those agents stop or other agents start. Explicit host targets and
reported states remain authoritative. Presentation-only snapshots are retained
in managed session storage, scoped by workspace, caller thread, turn, and item,
so resume can restore the names without reviving agents. History predating these
snapshots cannot infer missing targets from the current roster. Storage failures
report a notice without blocking the wait or its live display.
The ordinary session retention policy bounds these auxiliary records and protects
them while the native UI is active; restoration does not resume any host work.
An empty-stdin terminal poll replaces `Working` with `Still running` without
adding a transcript row for every poll. Process completion, further agent
activity, or turn termination clears it; ordinary input writes are not polls.
Compaction takes precedence over polling. Completed agent waits show `Still
running` only for agents whose reported state is running, without claiming
that missing state means success or continued work.

Native public reasoning summaries appear for Main and children, from summary
deltas or completed/history items. Main retains dim italic summary bodies in its
transcript; consecutive summaries without an intervening action fold the earlier
summaries into one expandable, comma-separated row, leaving the latest summary
expanded. Inline consecutive summaries are comma-separated too.
Reasoning does not replace `Working` in the composer status.
Working and its progress overrides shimmer while the turn runs. Other ongoing
states, such as sending and interrupting, breathe smoothly together from dim to light to dim, using a continuous neutral
color ramp until terminal colors are reported. Successful steering returns to
the active turn state rather than remaining in Sending.
Composer status (including Waiting), agent timers, and relative response ages share
whole-second duration formatting: `82s` displays as `1m22s`, and `3682s` as `1h1m22s`.
Trailing zero units are omitted (`5m`, `1h`), while elapsed timers start at `0s`;
response ages under two seconds remain `just now`.
Successful completion shows the frozen elapsed turn time in hours, minutes, and
seconds as needed, for example `Completed in 12s` or `Completed in 1m22s`. Activity retains the
same bodies for children in both the combined transcript
and selected-agent detail, including after completion or later activity. The current
summary updates the agent's roster status with a left-to-right brightness sweep
that stops when superseded or finished. The sweep blends between
the terminal's reported (OSC 10/11) foreground and background; without both
reports it steps through dim, normal and bold. Raw and encrypted
reasoning stay excluded.
Summaries without a leading bold title are third-party provider reasoning and
render as thinking blocks, following grok-build: a dim `• Thinking…` header
over the latest three text rows while it streams, with `· +N lines` counting
the rows above them, then `• Thought for 12s` (or `• Thought` when it lasted under a second or no
delta was observed) over the complete body. As in grok-build, each forwarded request
to such a provider shows `• Thinking…` from the request start, so the wait for the
first delta is not silent; the request's first reasoning item takes over that block.
Other output starting first, or the turn's end, removes the still-empty block; a
late completion of the previous request's item does not. Time runs from
the request start (or the first summary delta without one) to item completion; a turn
that ends first completes its unfinished blocks.
One second after an observed completion, the block folds to its header row in
Main and Activity; a click opens its body in the shared dialog. Restored history keeps
its body. In a consecutive run, the latest summary stays expanded instead of
folding on that timer. Titled summaries keep the Codex rendering.

Successful output eligible to fold shares one debounce deadline across Main and Activity,
including late completions, so it collapses in a single screen update rather than
one result at a time. An agent's latest output remains open until later activity.

Main and Activity follow new transcript content until manual scrollback or an
explicit jump to earlier content. Opening or closing Live/Diff, resizing, and
opening a narrative snippet or output dialog do not disable following. Scrolling to the last full viewport
(including End), or pressing Esc in a paused transcript, resumes following and
clears its unseen count. Activity has no `r` follow binding. A paused transcript shows “↓ Back to bottom · esc”
above Main's composer or at the bottom of Activity. Scrolling stops at the last
full viewport, including after resizing or following a link, and the link target
briefly highlights. Frames replace changed rows
without blanking the terminal. Journal records arrive typed from the journal
owner and follow the [native journal presentation](journal.md#native-main-presentation)
contract. A request without workspace metadata keeps its unscoped journal
namespace; the app-server cwd never grants it filesystem authority.

`make preview-native-ui` replays a scripted session of fake app-server
notifications through this frontend: delegation, concurrent child edits in both
docks, a failing test and follow-up, answers and a saved diff. Input takes the
real composer path, and the preview answers its turn start, steer and interrupt
requests as app-server would, echoing each user message's client ID; an interrupt ends every running turn and the
remaining playback.

The native client replaces the wrapped Codex terminal. The dashboard remains
available at the invocation URL. Redirected and noninteractive commands do not
start the native UI.

### Terminal notifications and lifecycle titles

The native client publishes Codex-compatible terminal lifecycle titles so pane
managers can recognize working and action-required states without parsing the
transcript. The Main turn supplies the work spinner; pending questions (including
hidden or resumed async questions) and unsupported server requests take priority
with `Action Required`. Settled Main turns remove the spinner; child completion
does not mark Main done. Herdr owns the distinction between unseen completion
and seen idle, including its blue and green indicators. Titles are cleared when
the native client exits or yields the terminal to an external editor.

Desktop notifications are separate from lifecycle titles. The native client
reads Codex's effective `tui.notifications`, `tui.notification_method`, and
`tui.notification_condition` through app-server for the selected workspace,
without editing configuration. Events are `agent-turn-complete`,
`approval-requested`, `plan-mode-prompt` (synchronous input), and `async-question`.
Only successful live Main completion notifies; pending questions take priority.
History restoration and duplicate question events do not notify again.

Notifications default to enabled and unfocused-only; `always` also allows them
while focused. Focus reports are terminal events, never composer or pane input.
`osc9` emits OSC 9 and `bel` emits BEL. `auto` uses OSC 9 in supported terminals
and Herdr, otherwise BEL; tmux receives passthrough-wrapped OSC 9 outside Herdr.
Notification text is generic, excluding prompt, answer, tool and secret content.
Disabling desktop notifications does not disable lifecycle titles. Configuration
or notification-output failures report a notice without stopping host work.
Redirected/noninteractive execution retains Codex's own behavior.

### User-input questions

Root-thread questions use a shared dock above the composer, hiding the live-edit
dock. `request_user_input` arrives as `item/tool/requestUserInput`; unexpected
server requests retain the visibly blocked fallback. Async questions arrive as
`agentMessage` items with `delivery: "async"` and `questions`, not as server
requests. They never become final-answer cards, journal finals, or final-answer
bookkeeping and do not complete a turn.

The dock shows question position, header, pending state (`waiting` for sync,
`open` for async), full wrapped question text, numbered options and descriptions,
and an always-present Other choice. It occupies at most half of Main. Long option
lists scroll with `option N/M`; only visible options determine column widths.
Descriptions drop when less than 24 columns remain. Key hints wrap into bands,
with compact hints in narrow panes. Long question bodies remain accessible through
PgUp/PgDn rather than ellipsis truncation.
For a single question, omit the Enter-next and left/right question-navigation hints.

Up/Down and Ctrl-P/Ctrl-N wrap option selection. Digits choose options; other
printable input selects Other and edits the answer. Tab on a sync option edits a
note; async answers have no separate note. Enter records an answer and advances;
on the last question it submits the call. Left/Right navigate questions freely.
Ctrl-] skips. A submission with gaps requires the inline confirmation
`Submit with N unanswered? · enter submit · esc back`. Escape closes a note,
then hides the dock while retaining drafts; with it hidden, Escape retains its
usual interrupt behavior. Ctrl-C retains clear/interrupt/quit behavior.

Live sync and async questions open directly when Main's composer is empty and
not pasting, showing a picker, or holding attachments. Otherwise a one-row banner
appears and editing continues; clearing the composer opens the pending question.
Submitting a call opens the next pending call when the restored composer is empty.
Explicitly hiding the dock keeps it hidden until reopened or a new question arrives.
Keystrokes received before the dock is painted do
not select or submit an answer. Clicking the banner or Ctrl+B Q opens the dock;
Session help lists the key. Main's title shows `?N` while another pane has focus.
Alt+Up and Shift+Left retain their queued-input editing meaning.

Opening the dock parks the whole main editor: text, caret, undo/redo, history
position, file/skill tokens, images and Shell Mode. Hiding or submitting restores
it unchanged. The frame says `draft kept` when the parked draft is non-empty.
Each question has its own editor across navigation and hide/reopen. Answer text
never merges into the main draft. Answer editors accept literal text, Unicode,
paste, undo and Ctrl-G, but disable completion and Shell Mode. Ctrl-V and a
bracketed paste containing one image path reuse the main composer's highlighted,
atomic `[Image N]` attachments, editing, undo and host-owned image processing.
Image files remain available across question navigation, hide/reopen and rejected
submissions. Secret answers are masked with `•` and
never enter input history. If resolution, interruption or supersession discards
an unsent non-secret answer draft, it is saved to local input history with
`unsent answer saved to input history`, not inserted into the main draft. Secret
drafts are discarded. Neither main nor answer drafts survive router restart.

Synchronous requests are keyed by thread and JSON-RPC request ID. Exactly one
response is sent, preserving the original ID. Image answers retain their
`[Image N]` references in that text-only response and queue a companion ordinary
composer message with the matching local images after the request resolves.
Codex emits the same image frames as for main-composer attachments.
Its `answers` object maps question
IDs to `{answers: [string]}`: an option sends `[label]`; a note sends
`[label, "user_note: …"]`; Other sends `["user_note: …"]`; skipped questions
are omitted. All-skipped sends `{answers: {}}`. Resolution notifications,
turn completion and interruption close the request, and stale responses are
never sent. While the dock is hidden and a sync request waits, Enter queues the
main draft instead of steering it. The open composer says
`answering · turn waiting`.

Async answers are batched per tool call, in a single ordinary user-input envelope.
Images accompany the complete text envelope as stock local-image inputs, with
labels numbered across the answered questions; image bytes and framing remain
Codex-owned:

```text
<send_user_message_question_reply>[{"answer":"…","question":"…","questionItemId":"[\"request_user_input_async\",\"ITEM_ID\",0]"}]</send_user_message_question_reply>
```

The existing submission path steers during an active turn, starts a turn when
idle, or queues until input is accepted. Different calls are never merged into
one envelope; skipped questions send nothing. Reply envelopes do not enter prompt
history or create duplicate user-message bands. Answers appear only beneath their
Asked record; pending-input previews show question submission progress without
transport framing or repeated answers. Between submission and committed user-message observation the card reads
`sending`. Submitted answers no longer count as actionable questions: their dock,
answer badge, and Action Required title clear immediately, even while steering
remains queued. Pending-input progress remains visible until the host commits
the reply; dismissal does not claim delivery. Other unanswered questions remain
actionable. Rejection reopens the question with its draft, without changing the
main editor. A committed reply from any client resolves the matching
`(threadId, itemId, index)`; replay uses the same identities and never resends.

Async questions remain open across turn end, noting that an answer starts a new
turn. An ordinary prompt committed in a new turn supersedes older open questions;
a steer does not. Before sending a new-turn draft, the banner says
`enter starts a new turn · dismisses N questions`. Resume reconstructs unmatched
questions from history, subject to that supersession rule, without taking focus.

Main groups adjacent question records without repeated Main headings. The dock
uses padded question and option rows, an accented position header, right-aligned
state when space permits, theme-aware selection, and subdued hints with accented
keys. Main and Activity use the shared `Asked` operation with full question text and
observed answers. Pending records say `waiting` or `open`; observed outcomes are
`answered`, `skipped`, `interrupted`, `superseded`, or `answered elsewhere`.
Answers sit directly under their questions with a distinct reply marker and
stronger weight; observed completion states use a light/dark-aware success color.
Secrets are excluded from these answer records. The scripted native preview
includes an async question and a sync question. In-process acceptance covers
encoding, batching, stale IDs, interruption, focus/draft isolation, secret masking
and history exclusion, external commits, rejection, replay and supersession.
Installed-Codex PTY acceptance answers an async question mid-turn and checks one
provider envelope, then answers and resolves a sync request with
`default_mode_request_user_input` enabled.
