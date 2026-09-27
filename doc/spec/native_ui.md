# Native app-server UI

## REQ-NATIVE-UI-001 — Native app-server UI

### Native app-server UI

`mekugi codex` uses the native client of `codex app-server` for interactive
terminal launches. Explicit `--yolo` remains required; without it startup rejects
before launching Codex. There is no legacy UI selection or fallback. It maps explicit `--yolo`, model and config
arguments plus `resume THREAD_ID`, and rejects other interactive arguments rather
than ignoring them.
Router readiness, provider catalogs, invocation overrides, native recovery hooks
and frontend environment keep their owners; redirected and noninteractive
commands keep their original path. The client speaks newline-delimited stdio RPC.

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

Startup `resume THREAD_ID` uses `thread/resume`, not a new thread or a replayed
prompt. The returned thread identity must match the requested ID; failure exits
without falling back to a new conversation. Main hydrates text messages and
command/edit items from the returned turns before accepting input, using the
existing retained transcript window. Buffered notifications then reconcile by
item identity. Historical tools are display-only: they do not recreate live
edit previews, processes or delivery receipts. Subsequent input
starts a turn on the same thread; an active snapshot retains its steer/interrupt
target. Resume keeps the returned workspace and effective model metadata, with
journal sinks scoped to that thread. Explicit invocation model/effort settings
and the routed provider are forwarded as resume overrides; Codex owns their
precedence and reports the effective configuration. Picker, `--last` and in-session switching remain outside this increment. Full-history
resume is limited by the 16 MiB RPC frame cap; oversized histories fail rather
than bypassing the transport bound. Paginated hydration remains unfinished.

Resume also restores the Agents roster and Activity from Codex's observational
history APIs, including archived descendants. Names/roles, retained assignments,
messages, commands, edit descriptions and answers are presentation history;
children are not resumed and historical unfinished turns never imply live work.
Only completed collaboration items imply delivered assignments or messages;
other attempts retain their recorded status without claiming delivery.
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
usage accounting. The client claims Main's thread in the router's activity
collector only so child activity is never injected into Main's provider
responses; the collector does not queue that activity.

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
Commands without captured effects retain their requested intent or ordinary Run classification. A grouped capture is displayed once rather than
claiming per-command attribution. Successful sibling edit rows are omitted once
the grouped receipt covers them; real non-edit operations and failures remain
visible, without internal capture-bookkeeping placeholders. Incomplete capture paths are grouped in
one coverage notice with a `mchanges` reference, not labeled as confirmed edits;
the durable records retain each path and its reason.

The shell frames Main on the left and one right pane: the saved diff (2) or
Activity (3), toggled and each filling the pane. A roster (4) above them fits its
content, four rows unfocused and up to 40% of the screen when focused; finished
agents fold into one row. Collapsed rows place activity directly after the name
without reserving name-column padding. Each row shows state, timer, tokens, cost
and turns, dropping from the right when narrow. Observed roles color the status
glyphs, with a color-to-role legend only in the expanded Agents pane; role labels
are not repeated in each row. Agent names retain their identity colors. Every pane has a title bar with
its tab number, focus and scroll state, and the status bar shows the tabs with
contextual key hints. Ctrl-B + number focuses a pane.

Streaming `apply_patch` edits dock at the bottom of the pane that owns them:
Main's in Main above the composer, subagents' at the bottom of the right pane.
When there are no child agents, Live replaces Activity across the right pane.
When child agents exist but none is responding, Live takes the top 35% of the
right pane and Activity fills the rest. Opening Diff keeps this top Live dock
and switches only the content below it, including when no child agents exist.
Main's live edits move into this right-side Live view while no child is responding.
With responding children, each owner's dock takes 30% of its pane, within 5 to
14 rows. Cards linger for two seconds after
the last card's animation settles. When no cards remain, the Live area collapses
and Activity or Diff reclaims its space. Shell or Code Mode projections first received
at completion do not open a transient dock; captured effects remain in their
receipt and saved diff. Concurrent edits share the dock as an accordion: cards
split evenly when each gets five rows, otherwise one stays open, chosen as the
roster-selected agent's card, then the current card, then the newest; Ctrl-B e
cycles and pins it. Router previews of exec and Code Mode edits dock the same
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

Main and Activity share the activity view's block parsing, operation grouping
and viewport logic; each keeps its own entries and follow/unseen state. Typed
app-server items update entries in place. Image-view items appear as `View`
with their workspace-relative path, including restored history.
Attached user images are inline model input, not image-view tool calls; they do
not synthesize a `View` event or claim that the model inspected them. Skill reads use
the colored `Skill` label; skill scripts display as `Skill  run …`.
Router-owned output-filter reductions remain muted annotations on their command
when the host identity matches, otherwise standalone metrics without a duplicate
command (including unmatched Code Mode calls).
Search rows show a muted `(N results)` when complete, attributable output supplies
a count: local result records (or explicit count totals), or a web result array.
Missing, truncated, failed, or ambiguous local output has no inferred count.
Main renders user messages on a tinted
band, assistant text under one `main` heading, tool runs drawn as a tree, agent
start/message/finish events labelled `sender → recipient`, final answers as
cards, and journal blocks. Its composer supports a new thread, submission, steering and
interruption. Typing `?` in the empty, focused composer opens keyboard shortcuts.
The view docks directly above the composer, temporarily hiding Main's live-edit
dock. It uses bold group headings and blue key labels, with Compose, Session,
and Transcript columns that stack when the pane is too narrow.
`?` or Escape closes them, and other keyboard input dismisses them while retaining
its normal behavior. A mouse event dismisses help without acting on the hidden
transcript. Question marks in non-empty drafts and bracketed paste remain
literal input. Dragging across text in Main, the composer, or Activity selects the visible text
and offers Reference (R), Copy (Ctrl+C, also C), and Clear (Escape) in the status bar.
Selection actions and the existing pane shortcut bar share bold key labels and
bullet separators. Composer selection excludes its prompt and borders.
Reference inserts `> SELECTED_MESSAGE\n\n` at the composer caret without submitting;
Copy requests the terminal clipboard via OSC 52, and Clear leaves the draft intact.
The selected viewport stays stable while the selection is active; resizing,
scrolling, or resuming editing dismisses it. Clicking a Markdown absolute local
path or HTTP(S) link copies its destination (a local path retains literal spaces
and its line suffix), rather than opening it. Clipboard availability is controlled
by the user's terminal.
Arrow keys move the insertion caret across graphemes and displayed
rows; Ctrl+Left/Right move by word and Ctrl+Up/Down move to logical line boundaries.
Alt+Backspace/Delete remove the previous/next whitespace-delimited word without
splitting image attachments. Editing and the visible composer window follow the
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
remain in temporary storage for Codex history/resume. Submitted text appears immediately and is reconciled with the
server's user message without a duplicate; rejection restores the draft; a
steer never becomes a new turn. Ctrl-C first clears a non-empty draft (Ctrl+Z
restores it), then interrupts the active turn, and with no turn active or
starting exits like `/quit`. Composer notices (command, paste, editor and
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
clicking it expands the assignment in place, and clicking again collapses it.
An unanswered assignment stays in full.
Transcript blockquotes use a vertical rail rather than literal `>` markers,
including on wrapped continuation rows, and retain inline Markdown styling.
Child answers link (`↩ re:`) to their retained assignment, not to a
previous answer or similar text. An answer to its thread's latest assignment
omits the link, since that assignment is directly above; an answer to an earlier
assignment in the same thread keeps it. Main's ordinary replies link to the user input
in their own turn. While Main is working, its latest ordinary reply is pinned
at the top of Main only when the original is entirely outside the unpinned
transcript viewport. Any visible part of the original suppresses the pin, avoiding
duplicate messages on screen. The copy
is bounded to leave activity visible; the full reply and its links remain in the
transcript. Very short panes omit the copy. A newer reply replaces it, and ending
the Main turn (completion, interruption, or failure) removes the pin.
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
clipping marker above it, rather than the inline commentary excerpt limit.
App-server still owns tool execution and agent lifecycle. Legacy
received envelopes are not injected into either parent's or child's provider output.

Native UI subtle and dimmed text share the composer model-name foreground
(RGB 115, 115, 116), rather than terminal-dependent faint intensity. This includes
metadata, secondary labels, separators, and diff coordinates; semantic status
colors, syntax highlighting, and animated status ramps remain distinct.

Native public reasoning summaries appear for Main and children, from summary
deltas or completed/history items. Main retains dim italic summary bodies in its
transcript; its active summary heading replaces `Working` in the composer status
until the item finishes or later Main activity supersedes it, without a separate
pinned row. Working and active reasoning shimmer while the turn runs. Other ongoing
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
reasoning stay excluded; the legacy pane keeps its reasoning policy.

Main and Activity follow new transcript content until manual scrollback or an
explicit jump to earlier content. Opening or closing Live/Diff, resizing, and
expanding a snippet do not disable following. Scrolling to the last full viewport
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
requests as app-server would; an interrupt ends every running turn and the
remaining playback.

The native client replaces the wrapped Codex terminal. The dashboard remains
available at the invocation URL. Redirected and noninteractive commands do not
start the native UI.
