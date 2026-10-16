# UI

## REQ-NATIVE-UI-001 — UI

An empty launch view welcomes the user with `mekugi-v<version> • codex v<version>`
for a versioned release. The versions identify the running executable and backend.
The embedded release tag takes precedence over module and revision metadata;
versioned source installs use their module version. Development builds show
`Mekugi <revision>`, or `Mekugi dev` without revision metadata; unavailable backend
versions are omitted.
The welcome remains visible while composing the first prompt, including compact
Main frames with at least six rows. History, live-edit replacement, and open
modal views and the full multiline composer take precedence. The welcome is
presentation only, not conversation history.

Operation-row formatting and feed controls are specified by
[native activity presentation](../../modules/activity/CONTRACT.md); shared classification belongs to
[activity observation](../../modules/activity/SPEC.md), and router cost totals to [usage reporting](../../modules/usage/SPEC.md).

### Offline playback

[Session replay](../../modules/sessionreplay/SPEC.md) supplies read-only scheduled
observations and semantic controls. The terminal adapter uses the ordinary
presentation caches and bounded output retention, with a playback bar that always
identifies simulated streaming. It starts no host connection or input admission.

Interactive playback requires terminal stdin/stdout and restores raw mode, mouse
reporting and the alternate screen on exit. `p` or Space pauses; `+`/`-` changes
speed; `[`/`]` seeks; `r` restarts. `Ctrl-B` followed by 1–3 selects Main, toggles
Diff or selects Agents; `Ctrl-E` uses the focused pane's ordinary disclosure
controls. Output dialogs retain their navigation and close controls. Outside a
dialog, Escape or `q` quits; Ctrl+C always quits. Wheel reports scroll Main;
fragmented or unsupported mouse/navigation reports cannot become playback commands.
No presentation control submits host intent.

`--headless` paints to a discard sink using `--width` and `--height`, defaulting
to 160 by 48, bounded to 20–1000 columns and 8–500 rows. It emits no terminal bytes
and returns a JSON timing summary on stdout. Loading, coverage and progress use
stderr. Frame timings include pacing, layout and writes; write time is also
measured separately. Headless timing excludes terminal backpressure. Accelerated
playback changes batching without changing recorded event times and cannot
establish original wall-time latency. Compare identical inputs, seed, speed and
dimensions, using 1.0x for representative latency.

Rendered-output snapshots cover running, paused, completed, fractional-speed and
narrow states. The shared [diagnostic-build profiling listener](../../docs/contributing/development.md#profile-live-sessions-and-replay)
is opt-in; replay creates no automatic profile files.

### UI

The [terminal session](../../apps/mekugi/internal/terminalsession/SPEC.md) owns
pane layout, input decoding, source selection, clipboard delivery and terminal
preferences. Feature behavior and host interaction follow the contracts below.

`mekugi codex` uses the native client of `codex app-server` for interactive
terminal launches. Without `--yolo`, Codex's configured approval and sandbox
policy applies and the native client answers Codex approvals
(see [Questions and approvals](#questions-and-approvals)).
The independent VCS guard follows
[remote-write approval](../../modules/remotewriteapproval/SPEC.md).
`--yolo` passes `approval_policy="never"` and
`sandbox_mode="danger-full-access"`, and thread requests repeat them.
Mekugi never silently changes the user's configuration or
sandbox policy. There is no legacy UI selection or fallback. It maps explicit
`--yolo`, model, config, and feature-toggle (`--enable` / `--disable`)
arguments plus `resume`, `resume THREAD_ID` or `resume --last`, and rejects other interactive arguments rather
than ignoring them.
Router readiness, provider catalogs, invocation overrides, native guard hooks
and frontend environment keep their owners; redirected and noninteractive
commands keep their original path. The client speaks newline-delimited stdio RPC.

After restoring the terminal on exit, the native client preserves existing draft,
diagnostic, and wrapper notices and prints a Codex-style token summary and a
shell-quoted continuation command for the established session. It preserves the
original executable, router flags, and Codex options before invocation-only
rewrites, replacing any prior resume selector with `resume THREAD_ID`. Commands
are highlighted in cyan on color-capable terminals (respecting `NO_COLOR`). An
established session also offers `mekugi replay-session --session THREAD_ID` for
offline playback, using the original executable and including `--debug-dir` with
the router-owned directory when debugging is enabled. The summary uses the
main thread's latest cumulative app-server usage, including restored usage on
resume, not child-thread or router cost totals. Displayed input and total exclude
cached input; cached and reasoning counts appear separately when nonzero.
Zero or unavailable usage omits the token line; no established thread omits the
resume and replay hints. A successfully archived session prints `Session archived: THREAD_ID`
instead of continuation and replay hints. Noninteractive commands retain Codex's own exit output.
An unexpected app-server exit retains its diagnostic alongside the child's exit
status; the normal session summary must not make a disconnect look like a clean quit.
The [Codex backend](../../backends/codex/SPEC.md) owns ordered RPC delivery,
frame bounds, and process shutdown. Paused presentation preserves that transport
contract; a session disconnect remains visible to the user.

The client replaces presentation, not projection policy. Codex remains the agent
runtime and execution authority, and Mekugi's router stays in the model-request
path; UI plumbing adds no model calls. The client connects to app-server, never
to the JavaScript execution host, and submits intent rather than executing tools.

| Concern | Owner |
| --- | --- |
| Tools, permissions, sandbox, native agents, JavaScript execution | Codex; the client submits intent and answers server requests. |
| Thread, turn and item lifecycle and history | Codex app-server; never reconstructed from rendered text. |
| Routing, projection, frontend PATH | The router and launcher, with invocation configuration carried into app-server. |
| Changes and recovery references | The capturer and replay store; native displays reuse their scoped receipts and previews. |
| Tokens, prices, missing usage | Router accounting; app-server usage is never added to cost totals. |
| Tool classification | App-server's typed command actions and Mekugi classifiers; renderers never parse shell text. |
| Skills and guidance | The [guidance module](../../modules/agentguidance/SPEC.md) and composer attachments. |

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
startup resume does: Main, roster, child transcripts, pane preferences, and a saved Diff
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
target. Resume keeps the returned workspace. Startup and in-session resume restore
applied model, reasoning effort and service tier through
[session preferences](../../modules/sessionpreferences/SPEC.md). Restoration
requires a host-resumable thread; preferences cannot create host session history.
The routing provider remains invocation-local. Missing settings leave Codex defaults
in effect and show a composer notice. Storage failures are visible without blocking
an applied host setting. Restoration finishes before resumed input is accepted.
Rejected default-reasoning restoration returns waiting input to the composer;
successful restoration or an explicit reasoning selection releases the gate.
The [UI boundary](../architecture/ui.md) owns host RPC integration.
Main hydrates the full resume
history in memory; its paginated hydration remains unfinished.

Resume also restores the Agents roster and child transcripts from Codex's observational
history APIs, including archived descendants. Names/roles, retained assignments,
messages, commands, edit descriptions and answers are presentation history;
children are not resumed and historical unfinished turns never imply live work.
Only completed collaboration items imply delivered assignments or messages;
other attempts retain their recorded status without claiming delivery.
Codex's history omits `exec` cell results and delivered inter-agent
messages, so each thread's host-selected rollout supplies them when its session
metadata names that thread: failed-cell rows and the tasks and messages the
thread received, as live activity observed them in its requests. Evidence
follows the host item it was recorded after, or, for another thread's
record, its completion time. Reading is bounded to each rollout's last 64 MiB;
older evidence shows only what the host's history does. A later live request
that carries restored messages again does not repeat them.
Paginated child history opens on the newest turn's recent items. Older content
is intentionally unloaded, not missing evidence: the selected transcript title
shows its history state. `Ctrl-B o` loads one older page for the selected child,
or the first eligible child while Main is displayed. Loading does
not block Main's conversation. Esc cancels this observational read without
interrupting the child; errors retain already loaded content and offer retry.
Older pages appear before newer child entries and retain their original answer
links. They must not turn older commentary into a final answer, repeat turn
status rows, or overwrite newer live status, usage, or timing. Rollout evidence
anchored to unloaded items waits for its page rather than appearing at a false
position or becoming a missing-evidence report.

Usage is shown only when available from its existing owner, never reconstructed
from transcript text. Missing child history is marked incomplete without
preventing the parent conversation from continuing.

The saved Diff pane reloads the existing durable change projections for the root
and discovered child workspace/thread identities before any new model turn.
It does not derive edit evidence from app-server history or include unrelated
workspace threads. Subsequent requests retain that scope through the existing
automatic-diff owner. Retention gaps remain gaps, not successful recapture.

Child-history hydration reports progress, buffers live notifications, and keeps typed
input as an unsent draft until reconciliation; `/quit` or Ctrl-C on an empty draft can exit while loading.
Discovery is bounded to 128 descendants and eight list pages, with an explicit
partial-history notice at the limit. Legacy child histories still use full reads.
Paginated children use bounded item counts per page, not byte-size bounds;
a single large item can still require a large in-memory frame. No additional
model requests or execution occur merely to restore pane content.

Resume restores [terminal preferences](../../apps/mekugi/internal/terminalsession/SPEC.md#preferences)
through the session-preference owner, independently of applied host settings.

`/side` is deferred; server requests the client cannot answer, such as MCP
elicitations, stay visibly blocked and are never auto-approved. Not in scope: Codex's TUI, PTY emulation
or screen scraping for Main; a second execution or permission control path; settings clones, onboarding, cloud tasks, voice; Git write actions or edit
rollback; browser frontends or remote hosting; new auth flows; a second
transcript store or cost calculator; model-visible UI commentary.

Real-time activity comes from app-server notifications, not from intercepted provider
responses: subagent activity and thread metadata name children by spawn path and role,
typed items supply commands, edits, collaboration calls, messages and reasoning
summaries, turn events drive each agent's state, and
`thread/tokenUsage/updated` supplies token counts. Cost stays with the router's
usage accounting. The client binds Main's thread in the router's activity collector for scoped
assignment/message and filter observations. Tool and lifecycle activity comes from
app-server, never generated commentary. [Router errors](../../modules/notifications/SPEC.md) render as user-only
composer feedback and leave the transcript and provider responses unchanged.
Cleanup planning and reclaimed-storage progress remain transcript content.

Run cards omit literal Bash or Sh `-c`/`-lc` launch wrappers and PowerShell
`-Command`/`-c` wrappers (optionally preceded by `-NoLogo`/`-NoProfile`).
The inner source uses the existing shell highlighting. Commands with outer redirects, assignments, additional arguments,
or dynamic wrapper words remain intact. This is display-only, including resumed items.
When Codex cannot classify a command, the shared shell display classifier identifies
frontend reads, inspections, searches and skill reads after removing a literal
launch wrapper. Literal `cat` output redirections and recognized inline Python or
JavaScript file writes show compact `Edit` intent rows marked `requested`, without
exposing the edit source or claiming saved changes. Heredoc batches classify each
operation, keeping trailing tests and other unclassified operations as Run entries.
Display intent inspection never reads target files or runs nested providers.
Captured shell effects replace the matching command row in Main and child transcripts
using durable thread and host-call identities, including after resume. Both use
the shared edit receipt formatter. Non-`apply_patch` receipts include a subdued
`cat`, `python3`, or other captured source label, separated by a middle dot.
Wrapped continuation rows preserve foreground colors and emphasis, including subdued source labels.
For tracked command lists, each edit intent changes from `requested` to
`ran`, `failed`, or `skipped` using its own segment outcome, without waiting
for later tests. `ran` reports command completion, not captured file counts
or success of the enclosing command. Untracked commands without captured effects
retain their requested intent or ordinary Run classification. A grouped capture is displayed once rather than
claiming per-command attribution. Successful sibling edit rows are omitted once
the grouped receipt covers them; real non-edit operations and failures remain
visible, without internal capture-bookkeeping placeholders. Incomplete capture paths are grouped in
one coverage notice with a `mchanges` reference, not labeled as confirmed edits;
the durable records retain each path and its reason.

The shell frames the selected transcript above Main's composer. Main starts at
full width; selecting Diff (2) opens a right pane, and selecting it again closes
the pane and restores full width. Selecting Main (1) returns to its transcript.
Clicking a child in Agents switches only the displayed transcript, using the
same full-message renderer; input, settings, questions and approvals remain
owned by Main. While a child is displayed, the composer is read-only: Main's
draft is preserved but hidden, and typing, pasting, submission, settings shortcuts
and keyboard interruption are disabled. Returning to Main restores editing.
Transcript navigation and copying remain available; referencing selected text
returns to Main before inserting the reference. A roster (3) below them fits its
content, four rows unfocused and up to 40% of the screen when focused; finished
agents fold into one row. The roster immediately follows the pane borders, with one blank row below it
before the status bar when terminal height permits. Collapsed rows place activity directly after the name
without reserving name-column padding. Each row shows state, timer, tokens, cost
and turns, dropping from the right when narrow. Observed roles color the status
glyphs, with a color-to-role legend only in the expanded Agents pane; role labels
are not repeated in each row. Roles come from host metadata. Missing roles have no label.
Agent names retain their identity colors. Every pane has a title bar with
its tab number, focus and scroll state, and the status bar shows the tabs with
contextual key hints. Ctrl-B + number selects Main (1), toggles Diff (2), or focuses Agents (3).
Uncolored labels in pane and composer borders use the terminal text foreground,
not the border's decorative foreground. Faint styling and explicit label colors
remain intact.

Main's top-right title-bar label reads the host's thread name, including names
restored on resume and inherited by forks. An unnamed thread has no placeholder;
long names truncate to the available width and terminal controls are not rendered.
Scroll position and unread counts remain alongside the name when space permits.

`/title <title>` renames Main locally without submitting a model prompt or
interrupting running work. A nonempty name appears immediately in Main's top-right
header while `thread/name/set` saves it. Before the host thread exists, the latest
entered name stays visible and is saved when startup or resume supplies its identity.
Empty names show usage feedback without changing the title. During a session
replacement, the command waits for the new session and preserves its draft.
Manual renaming suppresses automatic naming immediately; the saved host name
retains that suppression across fresh-process resume and fork. Older name writes
finish before the latest manual name is saved, so an in-flight generated result
cannot overwrite it. Save failures restore the last confirmed name and show
nonfatal composer feedback without affecting conversation work.

The [session-naming module](../../modules/sessionnaming/SPEC.md) owns automatic
title eligibility, generation, and precedence. Its optional inference does not
block conversation work. Generated candidates remain undisplayed until host
confirmation; host and manual renames win over pending generation. The application
retains name-save reconciliation and nonfatal failure feedback.

A blank row immediately above the composer separates it from the latest message
or auxiliary content when height permits.

`/live` toggles the Live pane on or off; `/live on` and `/live off`
apply directly. This presentation-only preference lasts for the current frontend
session, defaults to on at launch (including resume), and never stops activity,
preview updates, or capture. Hidden docks reserve no space. Showing the pane
again displays any still-current cards, not expired work.

Streaming edits with arriving source content and producible source hunks temporarily
replace their owner's transcript. Copy, removal, formatter-only, and generic command
effects do not open live views. Non-file targets and hunkless changes leave the
transcript visible.
The selected thread uses its whole transcript area above the composer; it never hides a saved
Diff. A child's live source appears only while its transcript is selected;
child edits do not take over Main. Saved Diff is not overlaid.

Main splits the available height among as many file views as fit with at least
15 rows per file, including its heading, plus one shared Live heading. A short
terminal shows a file-count summary rather than an undersized diff. At capacity,
a new file slides the visible window forward, keeping the newest file visible
without shrinking existing splits. Overflow files remain in the batch. Ctrl-B e
cycles and pins a file window for the displayed thread (Main or the selected child); subsequent arrivals do not steal that selection. Main's mouse wheel and
Ctrl-B PgUp/PgDn scroll live source independently of the transcript and saved
Diff. Scrolling to the live tip resumes that file's source following.
Ctrl-B r resumes the displayed thread's source following and newest-file window.

An edit batch is a caller-local burst. Each completed call retires after its own
1.5-second settling interval; running siblings and newer edits do not retain its
files. A surviving pinned file keeps its source window. When the selected call
expires, the dock follows the newest remaining file. The transcript returns when
the caller has no remaining previews. Turn termination
and explicit withdrawal remove only the affected previews; transient batches
are not reconstructed by history replay. Shell or JavaScript projections first
received at completion do not open a transient view; captured effects remain in
their receipt and saved diff.
Observed scoped file differences use “observed changes”,
without claiming exclusive attribution or successful command completion.
A recognized literal edit finishes its live card once the observed files match
the projected edit. A following test in the same shell command keeps its own
running status, not the edit's dock; test-only work opens no edit card.
Nested command and patch previews dock the same way. All docks consume the
[live-preview module](../../modules/livepreview/SPEC.md), including its retained
pre-execution source baselines; app-server file-change notifications never reconstruct patches
or re-read already edited files. A completed input stream remains explicitly
labelled as a preview, never a green success check; host item status and captured
effects establish the outcome. A new
saved diff never opens itself; the Diff tab shows an unseen badge instead,
and the saved diff lists its files or changes above the content when the pane is
narrow; `s` shows and focuses that list, enlarging it without covering the diff,
and `s` again hides it. The navigator and diff content have separate keyboard
focus: arrows act on the focused region, clicking a list row keeps list focus,
and clicking diff content focuses the diff. Enter opens a selected file and
focuses the diff; Esc returns from that opened diff to the list. In the focused
Files list, `e` opens the highlighted file's current on-disk contents in `$EDITOR`
(then `$VISUAL`, then `vi`), with editor arguments supported. Folder rows do
nothing, and `e` remains text while filtering. The terminal returns to the same
pane selection after the editor exits; missing files and editor failures show a
notice. Incoming saved
edits refresh content without moving the chosen file or scroll position. Saved
Diff has no follow mode; streaming-preview following is unchanged. Roster
selection changes the transcript without closing an open Diff.

The saved-review [model](../../modules/changehistory/SPEC.md) owns captured
composition, attribution and caller selection. Its Files navigator uses stable
path ordering, with folders first in tree mode. Tree/flat preference and collapsed
folders survive filtering; matching descendants remain selectable. Clearing the
filter restores its prior navigation position. Next/previous file navigation
uses the matching set and reveals destinations inside collapsed folders.

The Changes tab paints captured order as one graph lane per caller, branching
from `main` at that caller's first change. Rows show change ID, source, file count
and known line counts; unavailable counts use `?`. Single-file changes show the
file nested beside a wide diff or inline in a stacked list. Shared
[diff labels](../../libraries/diffrender/SPEC.md) preserve status and rename sources.
Change navigation visits each captured file in order; caller selection composes
other callers as baseline without altering evidence.

The active list row is shaded without a separate marker column. Hover underlines
its text, excluding tree/graph lanes. Moving the cursor previews the file while
retaining list focus; Enter opens it at its heading, and Esc returns to the list.
Opening a caller branch remembers the prior caller filter for Esc. File and hunk
jumps remember the file-local position they leave; scrolling through a file does
not. A scrolled file keeps its heading pinned. Updates retain selected file,
cursor and top-row identities, while resize preserves the logical source anchor
and keeps the focused list entry visible. Help does not permanently consume rows.

Clicking a compact Edit event opens the shared dialog at the clicked file’s
captured diff, rather than the first file in a multi-file capture. This temporary file preview shows the exact
retained capture, not the combined result of later edits, so shifted or superseded
hunks remain inspectable. Only the pointed edit row's text underlines
on hover in Main and child transcripts, not its gutter, alignment gaps, stat bar, or other
rows in the same capture. Click-through content (Edit, message excerpts, question links,
and agent links) opens the shared content dialog above the existing panes. Its
close button or Escape without a selection dismisses it without changing pane filters, scroll
positions, or keyboard focus. New activity and captures remain available.
Output-dialog text selection uses the shared drag and selection-scroll behavior.
The selected document stays frozen while new output arrives; clearing or copying
the selection releases it. Copy excludes the dialog's borders and gutters.
Changing pages, navigating search, or resizing dismisses the selection.
Edit pages use the exact host invocation, including grouped shell captures,
rather than matching nearby paths or edits. Roster picks remain explicit filters.

Successful single-line Ran output remains visible rather than collapsing to a
line-count toggle.

Main and child transcripts share block parsing, operation grouping
and viewport logic. Switching to a child preserves Main's entries and follow state. Typed
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
cards. Its composer supports a new thread, submission, steering, queueing and
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
with spaces are quoted. Selected files retain the `@` prefix as highlighted atomic tokens. Files,
directories, images and skills use the conversation composer's shared attachment
snapshots and editing lifecycle. Native skill metadata retains its exact source
path; managed placeholders resolve through the manager.
Complete enabled `$name` references also bind automatically after a word boundary
or on submission, including pasted prompts. Names must match exactly and have
one enabled path in the active workspace's Codex catalog. In Mekugi mode with
`skills-mgr`, the client also reads `skills-mgr list --codex` in that workspace,
using the wrapped process environment. Managed names take precedence over
same-named Codex filesystem entries. At submission or queueing, managed selections
and generated placeholders read their actual instructions with
`skills-mgr get --codex NAME`, including user-invoked skills omitted from the managed
model catalog, not a name-only reference or synthetic Codex path. Native placeholder selections
retain their metadata source identity. Repeated names read and attach
once per submission. Instructions use the file attachment chunker and budget,
travel in the versioned attachment envelope, and project into separate user
messages. Transcript replay recovers identity and immutable instructions from
that envelope without rerunning the manager. Input history restores the selected
references; resubmitting recalled input reads current contents, as with `@`.
The transcript shows a collapsed `Attached skill` receipt. Clicking it opens the
submitted instructions in the shared read dialog, including after replay.
Unreadable, oversized or budget-exceeding instructions yield explicit omission
frames and `Attach failed` receipts, not truncated contents. Both sources reuse
the file snapshot store, budgets, UTF-8 framing and delivery; no separate skill
snapshot lifecycle is maintained.
Unknown, disabled, ambiguous names and shell variables stay literal. A pending catalog lookup delays
submission rather than silently losing the skill; lookup failure is reported and
leaves unavailable references as text without blocking available skills from the
other source. Managed metadata merges into the workspace-scoped Codex picker
catalog; stale responses cannot bind another workspace's draft.

The [conversation composer](../../modules/conversation/SPEC.md) owns atomic token
editing, draft history, joining, and external-editor reconciliation. Composer and
submitted-input rendering keep a token on one
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

Attachment content, bounds and omission behavior are owned by the
[conversation composer](../../modules/conversation/SPEC.md). The transcript and
recalled prompt hide transport framing. Each submitted user item shows file outcomes before
agent activity: an `Attached` operation per included file, or `Attach failed`
with the file and omission reason. Chunked files appear once per submitted snapshot; stacked submissions retain
separate receipts for each snapshot. These operations
render paths without surrounding quotes, using the existing shared path formatting
(workspace-relative paths, subdued directories and emphasized filename), not
transport-envelope syntax. Paths outside the owning workspace stay absolute. They
are restored from submitted snapshots on resume without reopening files. Successful
receipts open the complete submitted content in the shared read dialog when clicked,
while keeping it out of the transcript feed. Failed receipts do not open content;
rejected submissions do not claim attachment success.
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

Typing `/` at the start of the draft opens a local command
catalog with descriptions for `/title`, `/compact`, `/clear`, `/resume`, `/archive`, `/btw`, `/status`, `/session`, `/ps`, `/copy`,
`/model`, `/effort`, `/reasoning`, `/tier`, `/live`, `/skills`, and `/quit`.
Typing filters commands with fuzzy matching; Up/Down selects, Tab
completes without executing, Enter runs the selected command, and Escape closes
the catalog without changing the draft. Completion replaces only the command token,
preserving trailing images and other draft content. Moving into arguments closes
completion. Pasted text does not activate the catalog. The catalog uses the shared picker viewport and
visible-row column sizing; it never submits a command to the model.

`/archive` asks whether to archive the current Main session, with No selected by
default. Escape or No returns to the session. Confirmation delegates archival and
stopping any running turn to Codex, then exits only after host success. While
archival is pending, new submissions and session switches wait. Failure keeps the
session open with actionable feedback so the user can retry. Archiving requires
an established session and is unavailable during restoration or replacement.
Selecting a child transcript does not change the archive target.

`/copy` opens a local picker for the latest completed response in the active
thread, including restored history. Whole response preserves
the Markdown; fenced code blocks and blockquotes are individually selectable.
Bounded previews never truncate clipboard content. Streaming fragments and other
threads do not replace the last completed response. Enter copies; Escape cancels;
an empty history reports that there is no response to copy. Copying remains
available during a turn without interrupting it. The picker, selection Copy and link copying use the
[terminal clipboard lifecycle](../../apps/mekugi/internal/terminalsession/SPEC.md#selection-and-source-copy).

### Side questions

[Side questions](../../modules/conversation/SPEC.md#side-questions) appear in a
bounded Markdown dock above the composer. Its Answering label shimmers with
elapsed time until completion or failure. Ordinary input still targets Main.
The question and answer use shared Reference, Copy and Clear actions, excluding
borders, status and hints; Markdown copying preserves source text. PgUp/PgDn
scroll the answer, with the question pinned during frozen selection.
Esc clears selection first, then dismisses active prompts or menus, then closes
the dock. Truncation and attachment omissions remain visible.

### Running processes

Main's composer row shows a breathing `N running processes` label while at least
one observed command is running in the active thread or its children. At zero,
the label is absent. Running commands remain ordinary scrollable transcript rows.

`/ps` opens the shared [output dialog](../../modules/activity/CONTRACT.md) over every running
command, in start order. Each page is one whole invocation: its display source,
without Mekugi instrumentation as in its transcript row, and its retained output,
streamed live and following its tail until the reader scrolls up. A tab row,
shown even for one command, names each page's owning agent and command; Left/Right
or a click selects a tab. Commands that start while the dialog is open join the tabs.
A command that completes keeps its page, with its final output and exit, until
the dialog closes. With no running command, the dialog shows an explicit empty
state. Closing uses the shared dialog controls without interrupting work or
submitting a model request. Yielded commands remain listed until host completion.
Restored history never creates a running process, and switching sessions closes
the dialog.

### Session metrics

`/session` opens a temporary, bordered metrics dialog, never a transcript entry
or model request. It uses the same themed, responsive, scrollable and selectable
panel as `/status`, with fixed close and navigation hints. Left/Right, Tab and
Shift-Tab cycle Overview, Transport and Exchanges. Up/Down, mouse wheel and
PgUp/PgDn scroll the selected view. Escape, Enter, q or Ctrl-C closes without
interrupting a turn; Escape first clears a text selection.

Overview groups settled request counts and retries, provider usage with complete,
partial, legacy and missing attempt coverage, measured cache rate and capture
health. Transport groups wire measurements, including all
four control-message directions.
Exchanges shows one retained exchange with its provider attempts, explicit
telemetry including the provider-returned service tier, duration, HTTP status and
capture-error codes, cache-prefix diagnostics, and the validated request kind.
`[` and `]` navigate older/newer
retained exchanges; newest follows new observations, while an older selection stays on its sequence until retention
reclaims it. Each view distinguishes absent usage from explicit zero evidence
and identifies output-deduplication estimates separately from provider usage.

The dialog consumes the capture-owned snapshot directly, refreshed once per
second only while open; `r` refreshes immediately. It covers all threads in this
router invocation, not just the active thread. Closing releases its view; replay
and resume never reconstruct historical launch metrics or revive resources.
An unavailable capturer and an empty exchange history have explicit empty
states. The metrics JSON endpoint remains available; no HTML dashboard or
startup dashboard announcement remains.

When launched with `--debug`, Overview also shows the actual debug artifact
directory supplied by the launcher, with shared workspace-relative formatting
and path styling. Paths outside the workspace remain absolute. This directory
is invocation-local, not recovered from session history or exported in sanitized
metrics; without `--debug`, the directory row is absent. Debug discovery remains
available even when the capturer snapshot is unavailable.

### Session status

`/status` opens a temporary, bordered session panel with fixed close/scroll
controls, grouped sections, aligned values, and colored remaining-usage gauges.
The layout adapts to narrow panes; long values wrap and the body scrolls without
moving its controls. Drag-select visible text and use the shared Copy action
(`c` or Ctrl-C); selection excludes panel borders and gauges. Escape first clears
an active selection, then closes the panel. Closing also discards its selection.
Colors follow the terminal light/dark theme, while numeric
percentages keep gauges readable without color. The panel includes known model,
reasoning, effective service tier (including Mekugi's per-model override), provider,
directory, session identity, approval policy,
sandbox, loaded instruction sources, and context remaining. It works during a
turn without sending or steering model input. Missing values are omitted, not
represented as zero usage or an unknown-state label. Account information refreshes
through `account/read` without refreshing credentials; ChatGPT accounts additionally
refresh `account/rateLimits/read`. The same panel updates asynchronously with the
account/plan, each reported quota window's percentage remaining and local reset
time with the system timezone's UTC offset (including daylight saving at reset),
credits, and the usage-page link. When the host supplies usage-limit resets,
the panel shows the available count and each available reset's title or type
with its local expiry time. A count-only response does not invent reset details;
missing reset data does not imply zero available. The count remains authoritative
when the host returns fewer detail rows. Non-ChatGPT sessions show observed input
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

The enable/disable management view remains Codex-owned: managed catalog entries
are selectable references, but are never sent to `skills/config/write` with an
invented path. Their selection is configured through `skills-mgr`.

[Terminal source selection](../../apps/mekugi/internal/terminalsession/SPEC.md#selection-and-source-copy)
provides shared drag, scroll, Copy, Clear and Reference actions across the displayed
transcript, composer, saved Diff and content dialogs.
Reference inserts a concise mention at the composer caret without submitting:
`[Selected message]` from Main or a child transcript,
`[Selected text]` from the composer, `[Selected side answer]` from the side-question
dock, `[Selected status]` from the status panel, `[Selected dialog]` from a dialog,
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
Selected text travels through the composer's shared quoted-attachment pipeline,
including question answers. The UI reports oversized selections and omissions.
Arrow keys move the insertion caret across graphemes and displayed
rows. At the first/last displayed row, Up/Down recalls older/newer submitted
input using the conversation composer’s local history.
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
`/tier` initially selects the effective current tier. Confirmed explicit choices,
including `default`, override configured per-model defaults for that thread and
routed model during this invocation, including after compaction, without changing persistent user configuration.
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
never removed. Other pastes insert as text. Bracketed-paste payloads never execute key or escape-sequence commands; only
the exact closing marker ends a paste. Terminal control sequences are removed
after the payload boundary is found.
A partial escape inside pasted content cannot swallow that marker, combine
consecutive pastes, or leave the composer locked. Editing resumes immediately
after an image-path paste without requiring another paste. Images are individual units for character and word navigation and
deletion, including when immediately adjacent to text. Ctrl+Z undoes and Ctrl+Y
redoes the composer’s grouped edits.
Ctrl+G opens `$EDITOR` (then `$VISUAL`, then `vi`) with a temporary draft file,
after releasing the terminal and input reader. Returning restores the UI without
submitting. The composer reconciles attachment bindings with the edited text.
Editor failure keeps the original draft and reports the recovery file.
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
composer frame. The command is syntax-highlighted as Bash using the shared
syntax theme; the leading `!` remains a plain mode marker. Highlighting preserves
editable text, multiline wrapping and cursor placement. Enter or Tab submits
the command through Codex's native
`thread/shellCommand` API, not as a model prompt. Shell syntax passes through
unchanged after removing the leading `!` and surrounding whitespace. Shell mode
disables file and skill completion; attached images and picker tokens must be
removed before execution. An empty command stays in the composer with a hint.
Codex runs it with full access using the thread's shell and workspace, streams
its output, and owns interruption and durable command/result history. While
Main is working, Codex injects the command and output into that turn. Otherwise
they remain in history for the next user input without starting a model reply.
Ordinary input submitted during a standalone shell run waits for its completion.
While manual compaction is pending, a shell submission stays in the composer for
retry once compaction settles.
Rejected submissions return to an empty composer with an error; if a later draft
exists, the rejected command is saved separately in input history instead of
turning that draft into executable shell text. Acknowledgements do
not claim command completion. Removing `!` returns to normal compose mode.
Completed shell edits update Diff totals, the saved pane, and Changes history
through the [command recorder](../../docs/contracts/change-evidence.md#capture-scope).

The host's opt-in `instant_interrupt` feature lets new input preempt model
responses and yield long-running `exec` calls without terminating their
cells. Native launches forward the `instant_interrupt` toggle unchanged, including on resume;
Codex owns validation, precedence, preemption, and continuation. The client uses
the same `turn/steer` path for conversation input whether the feature is enabled
or disabled and never implements instant steering by aborting a turn or replaying a tool.
Explicit composer Escape delivery is separate: it interrupts through Codex and
resubmits uncommitted steers as a new turn, without replaying tools.

`/compact` asks Codex and the provider to compact the current conversation context.
Enter interrupts
the active turn through the host, then waits for both interruption acknowledgement
and turn completion before starting compaction. This explicit command does not
depend on `instant_interrupt`, which controls conversation steering rather than
command dispatch. Tab queues compaction after the active turn without interrupting
it. Both appear in the pending-input list, and the command itself never becomes
model input. It separates surrounding text batches, and later input waits until
compaction finishes. Successful busy
compaction continues with waiting input, or sends one visible task-continuation
message if none is waiting. Idle `/compact` stays idle. Failure or interruption
cancels automatic continuation, including an interrupt racing successful completion.
With an empty composer, Ctrl+C cancels a still-queued command and restores it
along with other waiting input without interrupting Main. Cancelling an Enter
command while its interruption is pending prevents compaction, but cannot undo
the interruption already requested. An interruption rejection restores the command
and waiting input for retry without starting compaction. Once compaction is
starting or running, Ctrl+C cancels that compaction, restores waiting input, and
cancels its automatic continuation, not a later Main turn. Outside compact cancellation, normal Main interruption
is unchanged. Host compaction progress and failures remain visible.
`/clear` clears Main and child transcript presentation and starts a fresh Codex
session with the current workspace and model settings. It is unavailable while a
task or submission is in progress. Existing saved threads and filesystem changes
are not deleted; a failed fresh-session request keeps the old transcript usable.
While the new session starts, model-setting commands, shell commands, and side
questions keep their drafts for retry rather than targeting the old thread.
Late events from the previous session cannot repopulate the new transcript.

Enter steers a running turn; Tab queues input for the next turn and sends like
Enter while idle. [Conversation queues](../../modules/conversation/SPEC.md#queued-and-submitted-input)
retain waiting payloads and attachment snapshots. Waiting and uncommitted steers,
then queued input, appear as one preview per stack above the composer, with one
arrow and at most three text rows per stack and half of Main. Alt+Up or Shift+Left
returns locally waiting input ahead of the draft, preserving its caret. Sent
steers remain host-owned. Consecutive user items appear as one prompt with each
item's navigation target retained.

While a turn is active, Escape in the focused composer expedites pending or
locally unsent steers rather than retracting them into the editor. It requests
host `turn/interrupt`, waits for
the interruption acknowledgement, turn completion, and unresolved steer
acknowledgements to settle, then resubmits only uncommitted steers through
`turn/start`. A rejected interrupt does not resubmit input. The current draft
stays unchanged when steers are resent, and separately Tab-queued input stays
queued. If all pending steers commit before interruption completes, nothing is
resent and normal restoration of queued input applies. Committed user input is
never resent. During a starting turn, or with no pending steers, Escape retains normal
interrupt-and-restore behavior without automatic resubmission. It never quits.
Dismissing a picker, help, or selection and returning a paused transcript to the
bottom take precedence.

`/lock` protects against keyboard interruption: composer Escape and Ctrl-C cannot
interrupt active or starting work or discard waiting input, and Ctrl-C cannot exit
with an empty draft. `/unlock` restores those shortcuts. Draft clearing, undo,
contextual dismissal and copying remain available. `/quit`, explicit commands and
normal steering retain their behavior. A persistent Locked indicator includes
`/unlock`; the preference is restored for that workspace/thread on resume. Host
lifecycle events and an already-requested interrupt are unaffected.

Ctrl-C first clears a non-empty draft (Ctrl+Z restores it), then interrupts the
active or starting turn, and exits like `/quit` only when no input is waiting.
Outside Escape's expedited steer delivery, interrupt restores uncommitted
submissions, locally stacked steers, and queued
entries ahead of the current draft, preserving attachments and typed order;
nothing is automatically resent. A committed user message stays in the transcript.
An interrupted start that Codex has not committed removes its provisional
transcript entry. If it was the first message, Main returns to an empty transcript
and Ready composer with that draft restored. An interrupt requested before the
host supplies the turn ID waits for that ID rather than inventing one.
A rejected steer, or a steer whose turn ended without committing it, also returns
to the composer unless it is part of Escape's expedited delivery.
Submissions settle after their in-flight acknowledgement, so
restoration cannot reorder or duplicate input. On exit, stacked input is
printed with the unsent draft, and unresolved or uncommitted submissions as
outcome unknown, never resent. Composer notices (router errors, command, paste,
editor and Ctrl-C feedback) follow the turn state on the composer border without replacing
it. [Notifications](../../modules/notifications/SPEC.md) owns feedback lifetime and
complete batches. Draft editing clears feedback; the check button (`[✓]`) dismisses
actionable errors.
An ellipsis marks omitted error details; clicking a truncated composer error opens
its complete text, including the full router-error batch, in the shared error dialog.
Composer feedback stays user-only; model-visible error evidence
keeps its red transcript display and details dialog.
`/quit` exits only while idle;
unknown commands are reported, never sent as prompts. The
composer border carries turn state in Main and a read-only label in child views.
Its bottom caption follows the displayed thread's model, effort, service tier,
context usage and output rate; unavailable child settings do not borrow Main's values.
Main's title bar carries the
session title, scroll position and unseen-message count. History
beyond the retained window is not hydrated. Unexpected server requests stay
visibly pending, never auto-approved.

Agents includes Main and its children; selecting a row chooses the displayed transcript.
Main's context usage and output rate appear only in the composer, not its roster row.
Child-agent roster rows retain output rates under the
[usage contract](../../modules/usage/SPEC.md#output-throughput).
The composer and child-agent roster rows show `used/window • percent%` from the latest
Codex `tokenUsage.last.totalTokens` and `modelContextWindow`, independently of
cumulative usage totals. Before the first usage report, show `0%` used, equivalent
to Codex's initial `100% context left`; a missing window shows `N used` without
a percentage or placeholder. New host reports replace the context snapshot,
including reductions after compaction and replayed usage on resume.
Startup and child metadata also restore the latest saved context snapshot from
the absolute rollout path supplied by Codex, validating its session identity.
This observational read never resumes children or replaces newer live usage.
Only regular files are evidence; named pipes are rejected without waiting for a writer.
Restoration reads at most the last 8 MiB and a 64 KiB metadata prefix; absent,
unreadable, mismatched, or unavailable snapshots leave the initial display intact.
Roster edit counts use green for additions and red for removals, independent of
the syntax theme. Show known captured edits only: incomplete captures must not
hide confirmed counts from the same agent or other agents. Omit each zero or
unknown count, including in session totals; detailed capture gaps remain in Diff.
Narrow child-agent roster rows retain context before other metrics;
the composer's bottom border shows `model (effort) • used/window • percent%`
as one right-aligned caption, shortening or omitting the model first when narrow.
Main's title and child roster rows use the [Activity loaded-skill presentation](../../modules/activity/CONTRACT.md#agents-roster-and-navigation).

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
child replies are short excerpts with a link that opens their full retained
content in the shared dialog. Selecting a child in Agents displays its transcript. Assignment excerpts sit below their link on separate, wrapped quote
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
content link counts its omitted rows and closes the thread when nothing follows
it. A shrunk assignment ends with a right-aligned count of its omitted rows;
clicking it opens the full assignment in the shared dialog.
An unanswered assignment stays in full while in view. Once a spawn, follow-up
or Main message has scrolled above the viewport, it becomes an excerpt in the
reply format: the same row budget and a link (`↩ Open assignment`
or `↩ Open message`) that counts the omitted rows and opens that
exact entry. A message the excerpt would not shorten stays in full. Likewise, a
group of two or more of Main's settled operations becomes one row
once it has scrolled above the viewport, counting each verb in its row color
(`Searched 2 patterns • Read 1 file • Ran 1 command`); clicking it opens the
operations and completed edit patches in the shared dialog. Failed exits add a
visible failure count. Restored settled groups start collapsed on resume.
Running, declined or skipped work, failed items without an exit code,
unconfirmed edits and questions keep their rows. Shrinking above a scrolled-up
viewport does not move the visible rows.
Transcript blockquotes use a vertical rail rather than literal `>` markers,
including on wrapped continuation rows, and retain inline Markdown styling.
Child answers link (`↩ re:`) to their retained assignment, not to a
previous answer or similar text. An answer to its thread's latest assignment
omits the link, since that assignment is directly above; an answer to an earlier
assignment in the same thread keeps it. Main's ordinary replies link to the user input
in their own turn. A blank row separates the user excerpt from Main's answer,
so an answer that starts with a blockquote remains distinct. Only the first reply
to an input quotes it; a later reply to the
same input quotes it again only when another message sits between them. Main's own
tools, reasoning and progress do not separate replies. Main's replies remain in
the scrollable transcript without a pinned copy.
Replies following a committed question answer quote `re: your answer` and link
to its Asked record, not the earlier task prompt. The answer envelope never
creates a duplicate user-message band, including after resume.
The `re:` label includes the target text; if only the question
text is retained, it remains text rather than a fabricated navigation target.
Hovering a navigable link underlines it; clicking it scrolls to and briefly
shades the target. A successfully completed
child turn without a final answer promotes only that same turn's last message.
Codex V2 activity notifications carry no directed-message or assignment body.
The UI supplements them with the router's authenticated recipient-input
observations, retaining plaintext message bodies and assignment identities
exactly once. Native message display has a separate 64 KiB limit, with an explicit
clipping marker above it. There is no inline commentary excerpt path.
App-server still owns tool execution and agent lifecycle. Legacy
received envelopes are not injected into either parent's or child's provider output.

UI dimming uses ANSI faint when supported. `--ansi-faint=auto|on|off`
selects the terminal-local policy: `auto` disables faint when `mosh-server`
appears in the process ancestry, otherwise enables it; explicit `on` or `off`
overrides detection. Detached multiplexers can hide transport ancestry and
require `off`. No unrelated sessions are scanned. Unsupported output uses
256-color index 243 for ordinary dimmed text and static muted variants for
agent identity colors. This applies to metadata, secondary labels, separators,
and diff coordinates; non-dimmed semantic colors, syntax highlighting and
animated status ramps stay unchanged.

Host progress has one presentation mapping for live events and restored history.
Compaction start replaces Main's `Working` label with `Compacting context`;
completion restores ordinary turn status and adds a `Context compacted` event.
Turn completion, failure, or interruption
clears an unfinished compaction without claiming it succeeded. Child compaction
does not change Main’s composer. Agent wait starts, completions, and failures appear
only as the working caller's latest roster status, never in Main or child transcripts
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
them while the UI is active; restoration does not resume any host work.
An empty-stdin terminal poll replaces `Working` with `Still running` without
adding a transcript row for every poll. Process completion, further agent
activity, or turn termination clears it; ordinary input writes are not polls.
Compaction takes precedence over polling. Completed agent waits show `Still
running` only for agents whose reported state is running, without claiming
that missing state means success or continued work.

Native public reasoning summaries appear for Main and children, from summary
deltas, item events, or history. Main and child transcripts use the same reasoning sections
for titled and untitled summaries. Body rows are uniformly faint and italic,
including wrapped lines and text after inline formatting. Paragraphs and soft
line breaks continue beneath each section's header; only explicit Markdown
list items introduce body bullets. Roster status uses each section's collapsed
transcript label without its body or duration: titled sections show their title,
untitled summaries too long for the status column show `Thinking…` while streaming
or `Thought` after completion, and short summaries that fit display directly.
Consecutive labels are comma-separated. Roster reasoning uses the same muted
neutral text color as waiting activity. Live waiting text in Main and child transcripts,
roster waiting text, and active roster reasoning share the smooth breathing
animation. Completed or restored text does not restart the animation.
Reasoning does not replace `Working` in the composer status.
Working and its progress overrides shimmer while the turn runs. Other ongoing
states, such as sending and interrupting, breathe smoothly together from dim to light to dim, using a continuous neutral
color ramp until terminal colors are reported. Successful steering returns to
the active turn state rather than remaining in Sending.
Composer status (including Waiting), agent timers, and relative response ages use
shared compact duration formatting from [Activity](../../modules/activity/CONTRACT.md): `82s` displays as `1m22s`, and `3682s` as `1h1m22s`.
Trailing zero units are omitted (`5m`, `1h`), while elapsed timers start at `0s`;
response ages under two seconds remain `just now`.
Successful completion shows the frozen elapsed turn time in hours, minutes, and
seconds as needed, for example `Completed in 12s` or `Completed in 1m22s`. Child transcripts retain their reasoning bodies, including after completion or later activity. The current
summary updates the agent's roster status with the shared breathing animation
that stops when superseded or finished. It uses the same neutral color ramp as
waiting text, adapting to the terminal's reported (OSC 10/11) colors. Raw and encrypted
reasoning stay excluded.
Short reasoning summaries that occupy one rendered body row display directly,
without a `Thinking…` header, while streaming and after completion. Heading-only
summaries remain visible, including when followed by blank lines or an empty
Markdown comment. Completion adds `for <duration>` when at least one second was
observed; a trailing period is stripped immediately before `for`. Otherwise the
summary appears without a duration. The full summary
is retained without truncation, wrapping the duration when needed.
Summaries containing standalone Markdown headings, bold titles, or short plain
titles followed by blank-line-separated paragraphs form separate collapsible
sections. Plain titles are limited to 60 display columns and eight words, omit
sentence-ending punctuation, and are not list, table, or code content. Fenced
content does not introduce sections. Closing code fences use the opening marker,
at least its length, at most three leading spaces, and only spaces or tabs after
the marker. Rendering, source copying and section detection agree on that boundary.
Each titled section uses its title as its
streaming and collapsed header. Untitled longer summaries use `• Thinking…`
while streaming and `• Thought` after completion. The latest three body rows
remain visible, with `· +N lines` counting the rows above them.
Summary bursts roll through at the command-output cadence, retaining their
original Markdown. Earlier sections fold as soon as the next section starts.
Item completion keeps all sections open after queued text has been shown.
They fold with successful command output after later activity and a 5-second
quiet period. An observed item duration is shown on its last section only,
not attributed separately to each section. Each forwarded request to a provider
that streams untitled reasoning shows `• Thinking…` from the request start, so the wait for the
first delta is not silent; the request's first reasoning item takes over that block.
Other output starting first, or the turn's end, removes the still-empty block; a
late completion of the previous request's item does not. Time runs from
the observed request start, or the reasoning item's start when no request start
was observed, falling back to the first visible summary when both are absent,
through item completion; a turn
that ends first completes its unfinished blocks.
In Main and child transcripts, a click on a finished row opens its body in the shared
dialog. Restored long summaries start folded, while short ones remain directly
visible. Each section's own body is retained for its dialog. Consecutive items keep their
own rows. The latest completed item remains open until later activity.
Collapsed sections of one item share a single row, with bullet-separated titles
and the item duration once at the end. Long rows elide titles to retain the duration.
Clicking the row opens its retained sections as dialog pages; expanded presentation
keeps their original paragraphs.
An item that starts without public text creates no summary row; its observed
start still supplies timing when public text arrives later. Provider-request
waiting progress remains separate and is removed when no public summary arrives.
Late deltas cannot reopen completed items. Text appears only when
the host supplies a public summary; raw and encrypted reasoning are not a
substitute for summaries delivered late or only at completion.

Completed reasoning and successful command output eligible to fold share one
5-second quiet-period deadline across Main and child transcripts,
including late completions, so it collapses in a single screen update rather than
one result at a time. An agent's latest command output remains open until later activity.

`Ctrl-E` cycles the displayed transcript through default presentation,
expanded events, and expanded all. Expanded events shows full narrative,
public reasoning, and hook runs while leaving operation source/output at its normal
preview. Expanded all also opens retained operation source/output. Default restores
automatic folding, including keeping new event groups open until they leave the
viewport. User prompts remain unchanged. The choice also applies to arriving content
and stays local to the pane. The status hint follows the pane shortcut and names
the next disclosure choice. Dialogs retain
their existing controls. Toggling preserves following or the item at the top of
scrollback. Layouts are cached; off-screen blocks defer syntax decoration.

Main and child transcripts follow new transcript content until manual scrollback or an
explicit jump to earlier content. Opening or closing Live/Diff, resizing, and
opening a narrative snippet or output dialog do not disable following. Scrolling to the last full viewport
or pressing Esc in a paused transcript resumes following and
clears its unseen count. A paused transcript shows “↓ Back to bottom · esc”
above Main's composer. Scrolling stops at the last
full viewport, including after resizing or following a link, and the link target
briefly highlights. Frames replace changed rows
without blanking the terminal.

`make preview-native-ui` replays a scripted session of fake app-server
notifications through this frontend: delegation, concurrent child edits in both
docks, a failing test and follow-up, answers and a saved diff. Input takes the
real composer path, and the preview answers its turn start, steer and interrupt
requests as app-server would, echoing each user message's client ID; an interrupt ends every running turn and the
remaining playback.

The native client replaces the wrapped Codex terminal. Redirected and
noninteractive commands do not start the UI.

### Terminal notifications and lifecycle titles

The native client publishes Codex-compatible terminal lifecycle titles so pane
managers can recognize working and action-required states without parsing the
transcript. The Main turn supplies the work spinner; pending questions (including
hidden or resumed async questions), pending approvals and unsupported server
requests take priority with `Action Required`.
Settled Main turns remove the spinner; child completion
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

[Notifications](../../modules/notifications/SPEC.md#desktop-eligibility) owns normalized
enablement, focus and event eligibility. Focus reports are terminal events, never
composer or pane input.
`osc9` emits OSC 9 and `bel` emits BEL. `auto` uses OSC 9 in supported terminals
and Herdr, otherwise BEL; tmux receives passthrough-wrapped OSC 9 outside Herdr.
Notification text is generic, excluding prompt, answer, tool and secret content.
Disabling desktop notifications does not disable lifecycle titles. Configuration
or notification-output failures report a notice without stopping host work.
Redirected/noninteractive execution retains Codex's own behavior.

### Questions and approvals

[Conversation](../../modules/conversation/SPEC.md#questions-and-approvals) owns
prompt ordering, answer and approval lifecycle, editor parking, and replay behavior.
The terminal adapter renders both prompts above the composer, hiding live edits.
Clicking a pending banner or Ctrl+B Q opens approvals before questions. Main's title
shows `?N` while another pane has focus. Question input says `answering`, adds
`note` while editing an option note, and adds `turn waiting` for synchronous or
local authentication requests. It adds `draft kept` when Main's parked text is
nonempty. Approval input says `approving · turn waiting`, without the parked-draft
suffix. Until the dock has been painted, keys cannot answer it.

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
on the last question it submits the call. Plain Left/Right move the caret while
editing an Other answer or option note; while choosing options, they navigate
questions.
Ctrl+Left/Right, Alt/Option+Left/Right, and Option's Meta-b/f encodings use the
normal composer's word boundaries to move within the answer without changing questions.
Ctrl-] skips. A submission with gaps requires the inline confirmation
`Submit with N unanswered? · enter submit · esc back`. Escape closes a note,
then hides the dock while retaining drafts; with it hidden, Escape retains its
usual interrupt behavior. Ctrl-C retains clear/interrupt/quit behavior.

Main groups adjacent question records without repeated Main headings. The dock
uses padded question and option rows, an accented position header, right-aligned
state when space permits, theme-aware selection, and subdued hints with accented
keys. Main and child transcripts use the shared `Asked` operation with full question
text and observed answers. Answers sit beneath their questions with a distinct
reply marker and shared Markdown, source-aware selection, and copy. Observed
completion uses a light/dark-aware success color.

The dock shows the title, the command or summary, the directory when it is not
the workspace root, the reason, and numbered choices; it occupies at most half of
Main. Request text takes precedence over spacing and scrolls with PgUp/PgDn only
when the unspaced dock cannot hold it. Digits, Up/Down and Ctrl-P/Ctrl-N select;
Enter confirms. Escape hides the dock without answering; with it hidden, Escape
keeps its usual interrupt behavior. For supported command denials, typing or
pasting selects denial and edits an optional reason in the composer; Enter
confirms the selected choice. Digits select choices while the reason is empty.
Other approval types ignore text and pastes rather than edit the draft.


Answer editors support the shared literal text, Unicode, paste, undo, attachments,
selected-text references, and Ctrl-G controls while disabling completion and Shell
Mode. Secret entry is masked and excludes external editing. Ctrl-C retains
clear/interrupt/quit behavior, Ctrl-B retains pane commands, and hidden prompts leave
Escape's interrupt behavior unchanged. Alt+Up and Shift+Left retain queued-input
editing. Word-navigation keys move within answers rather than switch questions.

Pending banners preserve Main editing until an explicit open or an idle automatic
open. The question banner warns when a new-turn draft will dismiss older questions.
Approval banners show `! N approvals pending · ctrl+b q review`. Terminal snapshots and in-process acceptance cover painting, focus,
key decoding, editor isolation, and secret masking. Installed-host acceptance owns
live response delivery, independently of renderer and replay checks.

Approval state appears beside its associated host item, without a separate Approval
event or Session row. `Pending Approval` is yellow, `Approved` is green, and `Denied`
or `Auto Denied` is red, independently of execution success. Command dialogs retain
the full decision scope or denial reason. Guard decisions update the matching host
command row; compound commands show their state on the aggregate shell-batch row,
not on an unrelated classified subcommand. External resolution and a turn ending
before an answer show `Resolved elsewhere` or `Turn ended before an answer` without
claiming approval or denial.
