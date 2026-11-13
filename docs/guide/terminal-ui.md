# Terminal UI

Panes, composer controls and dialogs of the native terminal UI.

In a standalone terminal, Main starts at full width, with Agents below. Toggle Diff to split the view;
close it to restore full width. Click a child to switch the displayed transcript.
Live subagents use their own composer, prompts and settings. Main, children and
One-off retain separate drafts; historical views remain read-only. Narrow terminals show one pane.

| Action | Shortcut |
| --- | --- |
| Show Main / toggle Diff / focus Agents | `Ctrl-B`, then `1` / `2` / `3` |
| Resize Main/Diff split | Drag its divider, or `Ctrl-B`, then Left/Right; repeat arrows within 500 ms without another prefix. Agents height fits its content |
| Resize Diff navigator | `Ctrl-B`, then `[` / `]` |
| Browse displayed transcript | `Ctrl-B`, then PageUp / PageDown |

The wheel scrolls under the pointer. Markdown supports colored code, responsive tables,
and terminal Mermaid flowcharts; unsupported diagrams remain source. `/title TITLE`
saves a manual title and skips automatic naming. Otherwise a new session may make a
separate GPT-6 Luna medium-reasoning title request; unavailable credentials or Luna
silently skip it. Notifications follow Codex's `tui.notifications`. See
[terminal controls](../../apps/mekugi/internal/terminalsession/SPEC.md) and
[session behavior](../../apps/mekugi/internal/sessionpresentation/SPEC.md).

## Composer

- Enter steers; Tab queues. A shell-mode command queued while work is busy runs
  after that work settles rather than injecting its result into the active turn.
  Type `?` in an empty draft for shortcuts.
- `/predictions on|off` controls next-message suggestions in the empty Main composer.
  Tab accepts a suggestion for editing; typing dismisses it. Availability depends
  on the OpenAI account, model and remote configuration, and requires Codex
  0.162.0-alpha.20 or newer. Third-party providers are ineligible.
- `/presets` (or Ctrl+B then P) opens slots 1–9 and 0. Recall fills the composer without sending;
  the terminal keeps the previous draft in input history. New and Edit use `$EDITOR`
  in the terminal, like Ctrl+G, then ask for an optional label; Rename changes only
  the label. Without a label, the menu shows the input's first line. New, Edit,
  Rename and Delete manage slots, with confirmation before replacement or deletion.
  Slots use Codex's native `tui.presets` on the execution machine, including its
  active user/profile configuration and labels.
  Existing Mekugi preset files remain untouched and unused. Presets contain text,
  not attachments.
- A breathing count shows running commands from Main and its children. `/ps` opens
  the Background list below the composer, without a dialog. Select with Up/Down
  and press Enter to view the syntax-highlighted command, status, runtime and live
  output in the same area. Left returns to the list; Escape closes, and Enter or
  Space also closes details. Press `x` (or `s`) to stop the selected process when
  the host provides its process identity. Viewing or closing leaves work running;
  stopping closes the process view and reports failures.
- `@` attaches files; `@!` includes ignored files. VCS metadata is excluded;
  unreadable/oversized content has omission notices. Directories attach two-level
  trees honoring `.gitignore`, bounded to 256 entries/16 KiB with truncation notices.
- Enabled `$skill-name` references attach instructions, using `skills-mgr` when available.
  Use it for managed skills, `/skills` for Codex toggles; click receipts for submitted contents.
- Select transcript or saved Diff text and press `r` to mention it.
- Attached file, upload, selection and skill text can include `@path` or
  `@"path with spaces"` to attach referenced files. Relative references resolve
  beside a native file, or from the workspace for uploads, selections and managed
  skills. The selected source counts as depth 1; depth 3 is the last acquired level.
  Deeper or failed reads show omission notices. Directory trees are not expanded.
- `/oneoff TASK` opens an isolated work-capable snapshot with a One-off tab beside
  Main, inheriting Main's tools and permissions. It selects One-off immediately and
  keeps completed results available. Main stays usable and receives no one-off
  transcript; file edits and other external effects still affect the shared
  workspace. Only one One-off is open; creating another offers Replace or Cancel.
  Esc interrupts active work without closing the conversation or clearing its draft.
  While idle, Esc closes it. One-off and BTW retain native history in
  internal threads hidden from normal session selection, archived when their
  operations close. Closed slots are not restored on resume.
- `/btw QUESTION` opens an independent temporary answer dock on Main. It stays
  alive but hidden while viewing One-off. Return to Main for `/oneoff`, `/fork`
  and `/btw`; repeat `/btw` for a follow-up. Esc on Main closes only that dock.
  If its answer requires compaction, close the dock, compact Main and ask again.
- `/model`, `/effort` (or `/reasoning`), and `/tier` open pickers or accept values.
  Combine choices with `/model MODEL --reasoning=high --speed=fast`; omit MODEL
  to keep it, and use `--speed=standard` to clear the tier.
  `/tier default` clears the tier. Changes reach upcoming steps without editing config.
  Model and reasoning controls, including Shift+Up/Down, target the selected Main
  or One-off conversation independently. Service-tier controls target Main.
- `/permissions` lists Main's permission profiles. Select one, or use
  `/permissions PROFILE --approval-policy=on-request --reviewer=user`.
  Changes apply from the next turn, after Codex confirms them. Queued input waits
  while choosing; Esc cancels. Running commands and existing approvals are unchanged.
- `/compact` resets context: Enter requests it now, Tab queues until idle. Native
  admission may defer reset while a compact-and-wait execution is unresolved. `/clear` starts
  fresh while idle, preserving sessions/files. See [compaction](sessions.md#compaction-and-recovery).
  `/btw` requests use low reasoning when the model advertises it,
  preserving already-lower efforts and conversation settings.
- `/lock` prevents Esc/Ctrl-C interruption and Ctrl-C exit; `/unlock` restores them.
  The lock survives resume; explicit commands and steering still work.

Alt+Up or Shift+Left restores locally waiting input. Esc interrupts then resends only
uncommitted steers; committed input is never resent and Tab-queued input stays queued.
Without steers, Esc interrupts without resending. Ctrl-C clears the draft first, then
interrupts/restores input. Picker/help/selection dismissal takes precedence. Copying
needs OSC 52; Linux image paste needs `wl-paste` or `xclip`. See [input
details](../../apps/mekugi/internal/terminalsession/SPEC.md#composer-controls).

## Live diff pane

Source-content edits stream, including patches, `cat` heredocs, and supported Python
writes. Copies, removals, formatters, and other command effects stay in saved changes.
Only completed edits become saved changes. Main shows live edits from its session
family with source attribution; a selected child shows only its own edits.
`/live` toggles them without stopping capture;
new launches enable them again. In saved Diff, `s` opens the navigator, `/` filters,
`n`/`p` changes files, `[`/`]` jumps hunks. Tab opens Changes by caller. Click an Edit
for its hunk; `Ctrl-B e` pins files, `Ctrl-B r` resumes following.
See [saved-review controls](../../modules/changehistory/SPEC.md#terminal-review) and
[live-view controls](../../modules/livepreview/SPEC.md#terminal-live-views).

## Agents pane

Click an attachable child to read its full transcript; click Main to return.
Each runtime keeps its own composer draft, prompts and settings. The child composer
shows its role, model, reasoning effort and service tier. Historical views remain
read-only. The One-off tab selects a separate editable conversation.
In Agents, arrows navigate threads and `a` returns to Main. In the transcript,
Esc resumes following. The roster shows active time, round trips,
throughput, usage, estimated API cost, and composed captured edits. Usage/rates survive
resume while retained; `≥` marks lower bounds, `?` uncertain edits, unavailable metrics stay blank.
Main's throughput appears beside composer context. Herdr is optional; redirected
sessions keep Codex input/output. See [activity details](../../modules/activity/CONTRACT.md).

## Herdr app panels

A Herdr app layout with Mekugi panels shows the roster across the bottom and
saved changes and live edits in its right-hand diff pane. The panels follow the
active Herdr space, including spaces on another machine. Selecting an
attachable agent switches that space's Mekugi terminal transcript and composer; each thread
keeps its draft. Herdr's ordinary agents sidebar remains separate.

With capable companion panels, local file links open in the Files sidebar and
edit details open in the diff pane, including an original-diff fallback when no
saved entry exists. Otherwise Mekugi keeps its ordinary dialogs.

While the native panels are connected, Mekugi hides its duplicate terminal roster,
diff pane, live-edit dock and bottom navigation bar. Disconnecting the panels restores the terminal
layout without changing saved pane preferences or stopping the session.

The companion removes Mekugi's blue terminal frame and its padding, while its
native status mod retains the pane identity, full session title, loaded skills,
pending-question count and terminal scroll/new-line indicators. Hover the title
for its full text; open the skills control for the full loaded-skill list.
Standalone terminals retain their ordinary framing.

Each execution machine needs matching Mekugi client, shared-server and helper
builds, and the Herdr layout needs the matching native mods. After a protocol
upgrade, restart the shared server and reopen its clients. Let active work finish
first: server shutdown ends running work. The panels read captured Mekugi changes, independently
of the Git pane's working-tree changes. See the [native panel integration](../../apps/mekugi/internal/herdr/SPEC.md)
for integration details.

## Output dialog

Click output/errors/excerpts for retained content; `Ctrl-B !` opens the newest error.
With `mekugi-exec`, supported Bash and Linux dash-backed sh command lists show each
command's streamed output, exit status and timing; missing boundaries are labeled
combined. Supported single commands and pipelines, including Bash timed commands,
show measured command time excluding startup and matching. Host-omitted text cannot be recovered. Left/Right switches tabs;
`/` searches, `n`/`N` moves matches, `y` copies, Esc/`q` closes. Select text to copy portions.
Local links read current UTF-8 files up to 8 MiB; relative paths use the workspace,
`:line` scrolls, reopening rereads. See [dialog controls](../../modules/activity/CONTRACT.md)
and [command tracking](../../modules/commandsegments/SPEC.md).

## Image previews

Viewed and generated images appear in the transcript on Kitty- or Sixel-capable
terminals. Expand activity with Ctrl-E to show viewed images; generated images
accompany their saved paths. Unsupported terminals, unavailable files and terminal
multiplexers retain readable text entries. The [browser UI](remote.md#browser-ui)
also previews attached, viewed and generated images.
