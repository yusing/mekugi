# Web UI

## Purpose and ownership

Users create, follow and control Mekugi threads on explicitly connected machines
from desktop and phone browsers, and continue those threads from native terminals.
`mekugi web` serves embedded assets only. It has no default machine connection,
credentials, execution runtime, history reader or machine-control proxy.

Each browser connection talks directly to its selected machine's authenticated
connector and shared [Session API](../sessionapi/CONTRACT.md). The server owns live
threads and recorded playback. Closing a browser, UI host or connector leaves those
owners running. Ending a thread or replay releases its server-owned resources.

The page reuses the existing transcript, roster, code rendering and visual tokens.
It is a browser interface to shared semantic controls, not a terminal emulator.
Non-code text uses IBM Plex Sans; code uses JetBrains Mono without ligatures.
Body text uses regular weight;
headings and controls retain their own emphasis. Its dark appearance uses the
terminal palette, and its light appearance the same hues at readable depths.
The sidebar offers System, Light and Dark appearance choices, including before a
machine is connected. System is the default and follows system appearance changes.
The choice is saved for this browser and UI origin, independently of machines and
sessions. If browser storage is unavailable, the choice still applies for the
current page and the page explains that it could not be saved.
Secret prompts and native filesystem/editor integration remain native-only.

## Machines and selection

The page is laid out like a chat application. A sidebar lists threads grouped
under their machine, each machine with its connection state, and opens a new
thread on any of them; on phones it is a drawer. Thread rows show the title,
workspace basename on separate lines. The full workspace remains available on the row. With no machine connected, the
page asks for one. Machine settings, at the foot of the sidebar, add a machine by
name, connector URL and access token, replace a refused token, or remove a
machine. There is no implicit localhost connection. Machines and access tokens are
saved in this browser for the UI origin and reconnect after reload. Use a trusted
browser; removing a machine deletes its saved credential, and clearing the site's
browser data forgets all saved machines. Tokens never enter URLs, cookies or
UI-host requests. Replacing an expired token updates its saved credential and
reconnects only that machine. Storage failures are visible without preventing
connections for the current page.

Connections retain independent lists, subscriptions, errors and selected threads.
The page address identifies connection and the selected logical thread; recorded
history can additionally select an agent within that recording. Equal
thread IDs on different machines do not collide. Switching or removing a connection
releases browser-owned requests, not server work; one unreachable machine cannot
reroute or cancel another's work. Unknown mutation outcomes are reported and never
retried automatically. Reconnection reads a fresh snapshot.

## Thread controls

A new thread starts from an empty-state composer whose first message opens it.
Its pickers choose the machine, workspace, model and effort; start options,
workspaces, models and attachment paths come from the chosen machine. Its menu
also resumes a saved thread or opens a recorded replay. Live creation accepts initial text and ordered uploads/host paths;
later input uses the same attachment semantics. Empty text is allowed with usable
attachments. File acquisition has progress, cancellation and visible errors;
limits and omission notices come from the shared input contract.
Browser-selected, pasted and execution-machine path images show previews with their
names and removal controls before submission. Path previews use the selected machine
and the live thread's workspace, or the selected initial workspace before creation.
Unusable image previews show an unavailable state. Preview
URLs are released when an attachment leaves the rendered draft or its view closes;
retained files can recreate previews when returning to that draft.

The browser opens the thread and attaches its control view before sending the
first message through the same composer path as later input. Upload bytes are
read once. If that first submission is refused, the opened thread stays available
and the editor retains the input. An unknown outcome is never resubmitted on
reconnection. After asynchronous rejection, an intact retained submission offers
Retry, Edit or Discard, without editing individual attachments or rereading their sources.
The pending recovery belongs to that control view; leaving it releases unsent input.

The waiting-message bar offers Edit on its right. It withdraws locally waiting
input into the requesting view for editing without interrupting the active turn.
The queue bar also offers Steer to send queued messages into the active turn
without changing the composer draft.
Editable text stays separate from retained attachment labels and snapshots; Send
explicitly resubmits ordinary input; queued input returns to the next-turn queue.
Retry intact also preserves the original delivery mode. Discard releases it. Already transmitted
input remains host-owned. The composer's Send and Stop controls share one alignment;
Tab queues input without a separate Queue button.

The composer follows the terminal's: `/`, `@` and `$` show the server's
completions for keyboard or pointer selection; a chosen file or image also becomes
a host-path attachment, and a chosen skill stays a `$` mention, a leading `!` makes the input
shell input, Enter sends or steers and Tab queues while Main works. Ordinary
text, shell input and slash commands reach their existing server owners. Semantic menus, settings, status, resume, rewind, side
questions, approvals, ordinary question replies, history and details use the
per-client view API. Dialogs show loading, errors and refused stale actions without
replacing another client's selection. Prompt drafts are keyed by question identity;
accepted admission does not permanently disable a prompt still awaiting resolution.
Written question answers accept pasted images using the same acquisition and preview
controls. Each question retains its own text and images through refused submission;
the captured control view and question IDs keep asynchronous acquisition targeted.
Images accompany that question's answer through the shared native answer delivery.
Choosing an offered option or skipping leaves parked written input unsent.

Slash suggestions omit controls already beside the composer (model, reasoning effort
and service tier), terminal interruption locks, and the title and response-copy actions.
The header's pencil opens a title editor. Assistant responses in Main, delegate and
side conversations, including BTW answers, have a copy button beside each message.
It copies the complete authored Markdown, including collapsed supporting details.
Copy success or clipboard failure is shown without changing the draft.

Selecting text in the live transcript or activity details offers Reference above the
pointer, or above the selection endpoint for keyboard selection, within the viewport. It adds a
removable reference to the captured input destination and focuses its composer without
replacing the draft. The selected text and any file provenance travel through the same
quoted-selection attachment framing as the terminal, rather than becoming an ordinary
instruction. A changed draft refuses a late insertion; references stay isolated between
threads and side-conversation inputs.

While Main is displayed, the composer shows its model and reasoning effort as
pickers. Each opens the thread's own `/model` or `/effort` menu above the composer;
choosing applies the setting and leaves the draft unchanged.
Beside reasoning effort, a Fast toggle, labelled with a flash icon and its name,
applies the thread's priority service tier or restores its default tier. Its
yellow ink and tint reflect the confirmed fast setting; refusal leaves the
setting unchanged and shows the server's error. Choosing or closing a picker
hides its menu at once; a menu the server opens in response shows once the
choice settles. Where the composer's controls do not fit one row, the pickers and
Fast take their own row above the other actions, and Stop and Send keep the
trailing corner.
Reopening a picker or
revisiting a completion query shows cached choices while the current control view
refreshes them. Choices become actionable after that refresh; cached menu identities
are never submitted. Caches belong to the attached view and are discarded on
disconnection or thread changes. Menu motion respects reduced-motion preferences.

Running commands stay visible above the composer and leave when the authoritative
host invocation completes. Each command offers Stop when the host exposes an
individual process identity. Commands without one, including user-entered shell
commands, show a disabled Stop with an explanation; a later process identity enables
it. Stop targets that captured invocation and never interrupts the whole turn.
The browser's `/ps` directs attention to these persistent controls; the native
terminal retains its process dialog.

`/session` opens session metrics with Overview, Transport and Exchanges views,
exchange navigation and refresh, using the same fields as the terminal. `/status`
groups labelled values without individual row separators. Automatic web links and
Markdown web links open separately. Markdown machine-local file links use shared
file details on a connected live thread on the selected execution machine; other
views explain that requirement. Raw HTML and unsafe schemes remain
inactive, and untrusted images never load automatically.

Stopping interrupts Main. Explicit End names the thread and confirms that active
work stops. A continuation command uses `mekugi resume ID` on the selected machine;
it attaches to the active owner or resumes saved history after the owner ends.
An ended thread keeps its last state visible. If the same logical thread resumes
on that machine, its open browser view reconnects and restores live input without
navigation or a page reload.

Automatic resume keeps the current thread usable while opening the destination.
If attachment fails, its draft, uploads and pending recovery remain in place and
the error offers another resume attempt. Successful handoff waits for the
destination's initial view and the current view's input consequences to settle.

## Replay

Replay uses the selected machine's retained session history and existing playback
owner. It is read-only and starts no execution backend. Opening a replay while that
logical thread is live, or already has a playback owner, receives an explicit
conflict. Playback remains available at its end until explicitly closed.

All viewers of that replay share pause, speed, seek and restart. Backward seeks
replace the attachment and publish a complete snapshot. The browser keeps its
agent selection independently. Recorded model text is labelled as simulated.

### Page

A thread shows a centered transcript with the composer for a live session, or
the playback clock for a replay, docked below it. Session identifiers, timing,
usage, the continuation command and End sit behind an information button. The
right panel holds tabs for the agent roster, the session's changes and, once
requested, one row's details; header toggles open the roster or the changes, and
closing the panel also closes the details. A long
status truncates in the header so the title and toggles stay visible, and the details
show it in full. A failure's message stays out of the header line: the header
names the state in small type, and choosing it opens the full message beneath
the header. Each
transcript row offers its details, which open in the panel's Details tab; on narrow
screens the panel opens as a sheet over the transcript.

Main shows live source from its admitted session family; a selected child shows
only its own edits. Source replaces the transcript above the composer, as in the
terminal: one card per edit call with its author, editing tool and diff or text so far. Prompts and docks below stay in
place. Each completed call stays for its own 1.5-second settling interval, and
the transcript returns, at the place it was left, when no cards remain. Show
transcript hides the current cards until another call arrives. Calls already
complete when the transcript opens are not shown again, and side conversations
and replays have no live source.

The Changes tab, and its header toggle, appear for a live session; the toggle
shows the net changed lines. It holds the saved review of one agent, the
displayed agent until an agent picker chooses another or all agents. The picker
lists the roster's agents with the lines each changed and its file count. One
agent's files compose as the terminal's caller filter shows them, with other
agents' changes as baseline. Each file has a section with its workspace path,
net counts, the agents (for all agents) and programs that changed it, and its
composed unified diff with line numbers on both sides; hunk headers are left
out, and a rule separates hunks. Saved and live diffs use the file's syntax
colours, with deletions and additions highlighted independently. Replacement
blocks emphasize changed words across consecutive rows while retaining syntax
colours; pure additions and removals keep their ordinary row fill. Unknown
languages and oversized inputs remain plain, and large or complex replacement
blocks may omit word emphasis. Recently captured changes are marked. Long diffs
start collapsed; large reviews also limit the files initially expanded, with
every remaining file available through its disclosure. What the reader opens or closes stays so as the review
updates. Loading, coverage and failure stay visible; replays have no changes.

User turns sit on the right of the
transcript and agent activity on the left. Bound image, file, skill and selection
markers use their terminal colours; literal lookalikes remain ordinary text.
Large captured creation/deletion and temporary-file groups show their first files and a counted
disclosure for the rest, following Activity's shared folding policy. Opening the
disclosure preserves every retained file and its edit metadata. Large temporary
Create sources start behind a source disclosure; original details remain complete.
Attached images appear above the message, with a visible unavailable state if their source
cannot be read. An image an agent views appears under its View row; consecutive
images one agent views share one row of captioned thumbnails. Empty items have no expansion control. Attachment events show their paths or skill
names and auto-reload state, with columns sized to the visible verbs. Questions
show identical heading and body text only once. Output counts share the
event heading instead of occupying a separate collapsed row. Search patterns use accent ink, separate from their path operands. Compound
labels use spaced segments and command chips instead of separator punctuation;
the programs that made an edit show as chips after its path. Output toggles
keep a control-sized target.
Session details sit beside the thread title. Shell input is syntax-highlighted while editing. A compound command's combined result
follows its classified steps as an outcome and output row, without repeating
what the steps show; a result with no classified steps shows the command that
ran, where the terminal labels it as a shell batch. Command output shows the retained tail. Commands, scripts, tool
arguments and fenced code are syntax-highlighted; output is highlighted when
Activity identifies its source, such as a file read or a diff, or when it is
complete JSON. Unknown languages stay plain, except change statistics as
`git diff --numstat` and `mchanges` print them, which show as a diff stat.
Rows show the time of day in a live session and the offset into the recording
during replay. During replay, desktop keys follow terminal replay: Space or `p`
pauses, `+`/`-` changes speed, `[`/`]` seeks and `r` restarts. At the end of the
recording, Space or `p` restarts, as the play button does.

### Agents

The roster lists Main and its agents as a tree by display name, as the terminal
roster does. Each agent shows its role, its state (working, waiting, idle, done or
error), what a working agent is doing now or when a finished one last replied,
its work time, the lines it changed, token usage, cost, turns, round trips,
skills, output rate and context use. Usage that is only partly known is shown
as a lower bound with a trailing plus. A card has a name row, a state row and a
wrapping row of values with muted units, rather than a run of metric symbols;
a context bar runs along its foot. The roster heading counts agents by state
and totals the session's changed lines, usage and cost. Changed lines follow
the saved review as it updates. Waiting agents show the transcript's target names
and optional timeout, separately from idle agents.

The transcript shows Main's activity until a viewer selects another agent in the
roster; it then shows that agent's own activity under its name and state, with a
way back to Main. Live Main and delegate threads use the same selected-thread
publication and control lifecycle. Family navigation comes from the server's
ordinary thread publications without a retained parent view. The selected
thread owns its composer, prompts, settings and Stop; a declared role appears
in its composer. Per-thread drafts and unsettled input remain isolated across
navigation. Historical agents outside the admitted runtime tree
remain read-only. Selection belongs to each viewer, while playback stays shared.
The page address supports returning to a previous selection with browser history
and after reload using the saved machine identity. Seeking back
to before the selected agent started returns to
Main.
Traffic between agents is headed by who sent it to whom: a task, a message, or
an agent's final answer or failure in Main's transcript. Another agent's name in
a heading selects that agent. A task is collapsed except in its recipient's own
transcript. In Main's transcript, an agent's answer or message shows the summary
the agent wrote, with supporting details behind a disclosure, when
`child_reply_summary` is on and the reply has [a summary](../../../../modules/activity/CONTRACT.md);
the disclosure omits the repeated summary and section headings. Otherwise it shows the reply's first lines and a button for the rest. The
agent's own transcript shows its replies whole. As in the terminal, each
transcript keeps only its newest entries, so a long session's earliest activity
is no longer shown, and an agent may have no activity left to show.
Opening a long transcript renders its newest page first. Earlier retained entries
remain available through Show earlier entries, which preserves the reading
position. Incoming updates keep those entries available while reading; Latest
returns to the newest page and resumes following.

## Delivery and acceptance

`make webui` builds the Svelte application for embedding. `make webui-check` checks
its binding tests, types, lint and formatting; focused tests cover transport and isolation.
The UI host serves a clear 503 when assets have not been built.
The root Docker build embeds Bun-built assets; Compose hosts the page separately
from the connector and does not start an execution server. Container setup and
remote TLS requirements are documented in the root README's Docker deployment section.

Acceptance covers two explicit machine connections, identical thread IDs, independent
failure/token rotation, machine restoration after reload, saved token replacement
and removal, storage failures, new/resumed threads, first/later uploads and host paths,
unknown admission outcomes, prompts and semantic dialogs, shared replay controls,
server-owned lifetimes, and desktop, tablet and phone layouts in light and dark
appearances, with primary actions on screen at touch-sized heights.
Browser checks use actual connector/server routes; deterministic host fixtures do
not establish live-provider behavior or a deployed reverse proxy.
