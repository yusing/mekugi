# Terminal UI

Panes, composer controls and dialogs of the native terminal UI.

Main starts at full width, with Agents below. Toggle Diff to split the view;
close it to restore full width. Click a child to switch the displayed transcript.
Live subagents use their own composer, prompts and settings. Main, children and
Side retain separate drafts; historical views remain read-only. Narrow terminals show one pane.

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

- Enter steers; Tab queues. Type `?` in an empty draft for shortcuts.
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
  their live output, one tab per command. Select a process with Left/Right and
  press `s` to stop it when the host provides its process identity. Viewing or
  closing the dialog leaves work running; stopping closes it and reports failures.
- `@` attaches files; `@!` includes ignored files. VCS metadata is excluded;
  unreadable/oversized content has omission notices. Directories attach two-level
  trees honoring `.gitignore`, bounded to 256 entries/16 KiB with truncation notices.
- Enabled `$skill-name` references attach instructions, using `skills-mgr` when available.
  Use it for managed skills, `/skills` for Codex toggles; click receipts for submitted contents.
- Select transcript or saved Diff text and press `r` to mention it.
- `/side` creates a fresh answer-only snapshot with a Side tab beside Main. Only one
  Side is open; repeat `/side` to choose Replace or Cancel. Select Side to read or
  reply with its own draft.
  Esc closes it and interrupts only its answer. Side, One-off and BTW retain native
  history in internal threads, hidden from normal session selection and archived
  when their operations close. Closed slots are not restored on resume. If
  compaction is needed, compact Main and create a fresh side.
- `/oneoff TASK` runs isolated work in the same side slot, inheriting Main's tools
  and permissions. It selects One-off immediately and keeps the completed result
  available. Main stays usable and receives no one-off transcript; file edits and
  other external effects still affect the shared workspace. Replace or Cancel
  chooses what happens when Side or One-off is already open.
- `/btw QUESTION` opens an independent temporary answer dock on Main. It stays
  alive but hidden while viewing Side/One-off. Return to Main for `/side`,
  `/oneoff`, `/fork` and `/btw`; repeat `/btw` for a follow-up. Esc on Main
  closes only that dock.
- `/model`, `/effort` (or `/reasoning`), and `/tier` open pickers or accept values.
  Combine choices with `/model MODEL --reasoning=high --speed=fast`; omit MODEL
  to keep it, and use `--speed=standard` to clear the tier.
  `/tier default` clears the tier. Changes reach upcoming steps without editing config.
  Model and reasoning controls, including Shift+Up/Down, target the selected Main,
  Side or One-off conversation independently. Service-tier controls target Main.
- `/permissions` lists Main's permission profiles. Select one, or use
  `/permissions PROFILE --approval-policy=on-request --reviewer=user`.
  Changes apply from the next turn, after Codex confirms them. Queued input waits
  while choosing; Esc cancels. Running commands and existing approvals are unchanged.
- `/compact` compacts context: Enter interrupts then compacts, Tab queues. `/clear` starts
  fresh while idle, preserving sessions/files. See [compaction](sessions.md#compaction-and-recovery).
  `/btw` and `/compact` requests use low reasoning when the model advertises it,
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
Claude delegate rows remain noninteractive and show only available metrics.
Each runtime keeps its own composer draft, prompts and settings. The child composer
shows its role, model, reasoning effort and service tier. Historical views remain
read-only. The Side tab selects a separate editable conversation.
In Agents, arrows navigate threads and `a` returns to Main. In the transcript,
Esc resumes following. The roster shows active time, round trips,
throughput, usage, estimated API cost, and composed captured edits. Usage/rates survive
resume while retained; `≥` marks lower bounds, `?` uncertain edits, unavailable metrics stay blank.
Main's throughput appears beside composer context. Herdr is optional; redirected
sessions keep Codex input/output. See [activity details](../../modules/activity/CONTRACT.md).

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
