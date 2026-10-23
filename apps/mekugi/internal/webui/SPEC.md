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
Secret prompts and native filesystem/editor integration remain native-only.

## Machines and selection

The page is laid out like a chat application. A sidebar lists threads grouped
under their machine, each machine with its connection state, and opens a new
thread on any of them; on phones it is a drawer. With no machine connected, the
page asks for one. Machine settings, at the foot of the sidebar, add a machine by
name, connector URL and access token, replace a refused token, or remove a
machine. There is no implicit localhost connection. Tokens remain in memory, never
in URLs, cookies, persistent storage or UI-host requests. Replacing an expired
token reconnects only that machine.

Connections retain independent lists, subscriptions, errors and selected threads.
The page address identifies connection, logical thread and selected agent. Equal
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

The browser opens the thread and attaches its control view before sending the
first message through the same composer path as later input. Upload bytes are
read once. If that first submission is refused, the opened thread stays available
and the editor retains the input. An unknown outcome is never resubmitted on
reconnection. After asynchronous rejection, an intact retained submission offers
Retry or Discard, without editing individual attachments or rereading their sources.
The pending recovery belongs to that control view; leaving it releases unsent input.

The composer follows the terminal's: `/`, `@` and `$` show the server's
completions for keyboard or pointer selection; a chosen file or image also becomes
a host-path attachment, and a chosen skill stays a `$` mention, a leading `!` makes the input
shell input, Enter sends or steers and Tab queues while Main works. Ordinary
text, shell input and slash commands reach their existing server owners. Semantic menus, settings, status, resume, rewind, side
questions, approvals, ordinary question replies, history and details use the
per-client view API. Dialogs show loading, errors and refused stale actions without
replacing another client's selection. Prompt drafts are keyed by question identity;
accepted admission does not permanently disable a prompt still awaiting resolution.

While Main is displayed, the composer shows its model and reasoning effort as
pickers. Each opens the thread's own `/model` or `/effort` menu above the composer;
choosing applies the setting and leaves the draft unchanged.

Stopping interrupts Main. Explicit End names the thread and confirms that active
work stops. A continuation command uses `mekugi resume ID` on the selected machine;
it attaches to the active owner or resumes saved history after the owner ends.

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
agent roster is a collapsible right panel toggled from the thread header. Each
transcript row offers its details, which open in the same right panel; on narrow
screens both panels open as sheets over the transcript. User turns sit on the right of the
transcript and agent activity on the left. A compound command's combined result
follows its classified steps as an outcome and output row, without repeating
what the steps show; a result with no classified steps shows the command that
ran, where the terminal labels it as a shell batch. The page follows the system's light or dark
appearance. Command output shows the retained tail. Commands, scripts, tool
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
roster does. Each agent shows its role, its state (working, idle, done or
error), what a working agent is doing now or when a finished one last replied,
its work time, the lines it changed, token usage, cost, turns, skills and
context use. Usage that is only partly known is shown as a lower bound. The
roster heading counts agents by state and totals the session's changed lines,
usage and cost.

The transcript shows Main's activity until a viewer selects another agent in the
roster; it then shows that agent's own activity under its name and state, with a
way back to Main. Selection belongs to each viewer, while playback stays shared.
The page address supports returning to a previous selection with browser history
while the page stays open; reload clears connections and credentials. Seeking back
to before the selected agent started returns to
Main.
Traffic between agents is headed by who sent it to whom: a task, a message, or
an agent's final answer or failure in Main's transcript. Another agent's name in
a heading selects that agent. A task is collapsed except in its recipient's own
transcript. In Main's transcript, an agent's answer or message shows the summary
the agent wrote, with the full reply behind a disclosure, when
`child_reply_summary` is on and the reply has [a summary](../../../../modules/activity/CONTRACT.md);
otherwise it shows the reply's first lines and a button for the rest. The
agent's own transcript shows its replies whole. As in the terminal, each
transcript keeps only its newest entries, so a long session's earliest activity
is no longer shown, and an agent may have no activity left to show.

## Delivery and acceptance

`make webui` builds the Svelte application for embedding. `make webui-check` checks
its binding tests, types, lint and formatting; focused tests cover transport and isolation.
The UI host serves a clear 503 when assets have not been built.

Acceptance covers two explicit machine connections, identical thread IDs, independent
failure/token rotation, new/resumed threads, first/later uploads and host paths,
unknown admission outcomes, prompts and semantic dialogs, shared replay controls,
server-owned lifetimes, and desktop, tablet and phone layouts in light and dark
appearances, with primary actions on screen at touch-sized heights.
Browser checks use actual connector/server routes; deterministic host fixtures do
not establish live-provider behavior or a deployed reverse proxy.
