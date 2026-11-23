# Terminal UI

Panes, composer controls and dialogs of the native terminal UI.

In a standalone terminal, Main starts at full width above two stat lines. Toggle Diff to split the view;
close it to restore full width. Live subagents use their own composer, prompts and settings. Main,
children and One-off retain separate drafts; historical views remain read-only. Narrow terminals show one pane.

The first stat line shows the displayed thread's model, context-window bar,
estimated cost (plus the session total on Main), input (↑) and output (↓) tokens,
Main's throughput, and how much of the account's `5h` and `Weekly` rate limits
is left once Codex reports it, as in `/status`. A limit turns amber with a quarter
left and red with a tenth left, and stays on a narrow line after the cost leaves.
The second names the displayed thread, its working (◐), finished (✓)
and failed (!) subagent counts, the session title, the working directory, the
lines below the view after you scroll back (▼ N below), and the
arrow keys that act on an empty composer: ↓ roster, → diff (or ← main from Diff)
and `ctrl+e` view. Narrow lines shorten the directory and title, then drop the least
important parts; failed and working counts outlast the title. Click a hint to use it.
A notice above the composer offers `[details]` when it hides text and `[×]` to dismiss it.

| Action | Shortcut |
| --- | --- |
| Open the Agents sheet (with subagents) | Down on an empty composer |
| Return from a child to its parent | Up on an empty composer |
| Open Diff / return to Main | Right / Left on an empty composer |
| Show Main / toggle Diff / focus Agents | `Ctrl-B`, then `1` / `2` / `3` |
| Resize Main/Diff split | Drag its divider, or `Ctrl-B`, then Left/Right; repeat arrows within 500 ms without another prefix |
| Resize Diff navigator | `Ctrl-B`, then `[` / `]` |
| Browse displayed transcript | `Ctrl-B`, then PageUp / PageDown |

The wheel scrolls under the pointer. Markdown supports colored code, responsive tables,
and terminal Mermaid flowcharts; unsupported diagrams remain source. `/title TITLE`
saves a manual title and skips automatic naming. Otherwise a new session may make a
separate GPT-6 Luna medium-reasoning title request; unavailable credentials or Luna
silently skip it. Notifications follow Codex's `tui.notifications`. See
[terminal controls](../../apps/mekugi/internal/terminalsession/SPEC.md),
[session naming](../../modules/sessionnaming/SPEC.md), and
[notifications](../../modules/notifications/SPEC.md).

## Composer

- Enter steers; Tab queues. A shell-mode command queued while work is busy runs
  after that work settles rather than injecting its result into the active turn.
  Type `?` in an empty draft for shortcuts.
- `/predictions on|off` controls next-message suggestions in the empty Main composer.
  Tab accepts a suggestion for editing; typing dismisses it. Availability depends
  on the OpenAI account, model and remote configuration, and requires Codex
  0.162.0-alpha.20 or newer. Third-party providers are ineligible.
- `/presets` (or `Ctrl-B`, then `p`) opens slots 1–9 and 0. Recall fills the composer without sending;
  the terminal keeps the previous draft in input history. New and Edit use `$EDITOR`
  in the terminal, like Ctrl+G, then ask for an optional label; Rename changes only
  the label. Without a label, the menu shows the input's first line. New, Edit,
  Rename and Delete manage slots, with confirmation before replacement or deletion;
  press `y` to confirm.
  Slots use Codex's native `tui.presets` on the execution machine, including its
  active user/profile configuration and labels.
  Existing Mekugi preset files remain untouched and unused. Presets contain text,
  not attachments.
- Type `/ps` or click the running-process count to inspect commands from
  Main and its children without stopping them. One running shell opens directly;
  otherwise use Up/Down and Enter to select a shell. The composer is hidden,
  but your draft stays intact. Only the output frame scrolls; End resumes
  following live output. Press `v` to open the exact invocation and `y` to
  copy it. Left backs out one level and Esc closes the inspector. Press `x`
  (or `s`) twice to stop the selected shell when available; any other key keeps
  it running. Stopping dismisses the inspector so failures stay visible.
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
- `/compact` resets context: Enter requests it now, Tab queues until idle. `/clear` starts
  fresh while idle, preserving sessions/files. See [compaction](sessions.md#compaction-and-recovery).
  `/btw` requests use low reasoning when the model advertises it,
  preserving already-lower efforts and conversation settings.
- `/lock` prevents Esc/Ctrl-C interruption and Ctrl-C exit; `/unlock` restores them.
  The lock survives resume; explicit commands and steering still work.
- `/quit` releases an idle session without deleting its history. When work,
  including a shell still running after its turn, can either continue or be
  stopped, the inline prompt offers **Continue in background** or **Stop**.
  Stop ends work in the selected session and its delegates; click it twice, or
  select it and press Enter. Esc keeps the terminal
  attached. `/detach` always leaves without prompting or stopping work.
  Use `--resume` to reattach to live work, or to reopen saved history after release.

Alt+Up or Shift+Left restores locally waiting input. Esc interrupts then resends only
uncommitted steers; committed input is never resent and Tab-queued input stays queued.
Without steers, Esc interrupts without resending. Ctrl-C clears the draft first, then
interrupts/restores input. Picker/help/selection dismissal takes precedence. Copying
needs OSC 52; Linux image paste needs `wl-paste` or `xclip`. See [input
details](../../apps/mekugi/internal/terminalsession/SPEC.md#layout-composer-and-dialogs).

## Live diff pane

Source-content edits stream, including patches, `cat` heredocs, and supported Python
writes. Copies, removals, formatters, and other command effects stay in saved changes.
Only completed edits become saved changes. Main shows live edits from its session
family with source attribution; a selected child shows only its own edits.
`/live` toggles them without stopping capture;
new launches enable them again. In saved Diff, `s` opens the navigator, `/` filters,
`n`/`p` changes files, `[`/`]` jumps hunks. Tab opens Changes by caller. Click an Edit
for its hunk; `Ctrl-B e` pins files, `Ctrl-B r` resumes following.
See [saved-review controls](../../modules/changehistory/SPEC.md#saved-review-presentation) and
[live previews](../../modules/livepreview/SPEC.md).

## Agents

Down on an empty composer opens the Agents sheet at the bottom of Main when the
session has subagents. Its heading shows the subagent counts and a color legend
for their roles. Up/Down switch the displayed thread without closing the sheet.
Up from the first subagent displays Main. `a` toggles between Main and the last
child; Esc closes the sheet. Click an attachable child to read its full transcript.
With the sheet closed, Up on an empty child composer returns to its parent.
Each runtime keeps its own composer draft, prompts and settings. The child stat line
shows its model, reasoning effort and service tier. Historical views remain
read-only. While One-off is open, the second stat line shows `Main • One-off`
tabs; click either to switch between Main and the separate editable conversation. In the transcript, Esc resumes following. Sheet rows show active time,
round trips, throughput, usage, estimated API cost, and composed captured edits; Main's
row is omitted because the stat lines carry its facts. Usage/rates survive resume while
retained; `≥` marks lower bounds, `?` uncertain edits, unavailable metrics stay blank.
Redirected sessions keep Codex input/output. See
[activity](../../modules/activity/SPEC.md).

## Output dialog

Click output/errors/excerpts for retained content; `Ctrl-B !` opens the newest error.
With `mekugi-exec`, supported Bash and Linux dash-backed sh command lists show each
command's streamed output, exit status and timing; missing boundaries are labeled
combined. Supported single commands and pipelines, including Bash timed commands,
show measured command time excluding startup and matching. Host-omitted text cannot be recovered. Left/Right switches tabs;
`/` searches, `n`/`N` moves matches, `y` copies, Esc/`q` closes. Select text to copy portions.
Local links read current UTF-8 files up to 8 MiB; relative paths use the workspace,
`:line` scrolls, reopening rereads. See [activity](../../modules/activity/SPEC.md)
and [command tracking](../../modules/commandsegments/SPEC.md).

## Image previews

Viewed and generated images appear in the transcript on Kitty- or Sixel-capable
terminals. Expand activity with Ctrl-E to show viewed images; generated images
accompany their saved paths. Unsupported terminals, unavailable files and terminal
multiplexers retain readable text entries. The [browser UI](remote.md#browser-ui)
also previews attached, viewed and generated images.
