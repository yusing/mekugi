# Terminal session

## Requirements

- Provide one interactive terminal UI attached to the server-owned runtime, restoring pane layout,
  focus, roster, Diff, and Activity on resume.
- Keep composer attachment/image/selection placeholders atomic through navigation, deletion,
  undo/redo, and external-editor handoff (`Ctrl-G`).
- Show public reasoning summaries, delegate assignments, active tools, and per-agent usage/cost
  while keeping raw/private reasoning hidden and avoiding duplicate completion entries.
- Provide live streaming diffs and saved captured-change review (including stock shell writes),
  interactive approvals/questions, private masked secret dialogs (`sudo`/GPG), and process
  inspection/stopping (`/ps`).
- Keep input, cancellation, and rendering responsive independently of storage, observation, or
  async review hydration.

## Current approach (replaceable)

### Boundary and renderer model

- `terminalsession` owns terminal byte-stream decoding (keys, SGR mouse, bracketed paste, focus/
  color replies), pane layout, focus, frozen source selection, OSC 52 clipboard requests, and
  incremental frame delivery for both live sessions and offline replay.
- Consumes detached publications from `sessionapi`/`sessionpresentation` and returns versioned
  semantic intents/output demands; painting never performs filesystem/storage reads or backend RPCs.
- Only successfully delivered visible frames mark prompts ready or acknowledge notice surfaces.

### Layout, composer, and dialogs

- **Panes & stat lines**: Main starts full-width above two stat lines (model/context/cost/tokens/
  throughput/remaining `5h` and `Weekly` rate limits on line 1; thread, `Main • One-off` tabs when
  open, subagent counts, title, workspace, `▼ N below` scroll count, and empty-composer arrow /
  `ctrl+e view` hints on line 2). `Ctrl-B 1/2/3` or empty-composer arrows switch Main, split Diff,
  and bottom-docked Agents sheet; live subagents and `/oneoff` keep independent composers/settings.
- **Composer & controls**: Enter steers, Tab queues, Esc interrupts (or closes overlays/docks),
  leading `!` enters Shell Mode, `/`, `@`, `@!`, `$` open completions, `?` opens shortcuts,
  `/presets` manages Codex `tui.presets` slots, `/lock`/`/unlock` guards interrupt keys,
  `/predictions` toggles next-message suggestions, `/btw` opens a temporary read-only dock. Notices
  above the composer offer `[details]` when truncated and `[×]` dismissal; destructive choices
  require confirmation. See [Terminal UI guide](../../../../docs/guide/terminal-ui.md) for full
  user controls.
- **Live changes & dialogs**: `/live` toggles provisional streaming diffs above the composer;
  bottom-docked content/output/file/process dialogs support scrolling, search (`/`), copy (`y`),
  and Original/Model-visible toggling (`o`). Drag-selection freezes the document for off-screen
  scrolling, source-aware Markdown/Diff copy, and `Reference` (`r`) mentions.
- **Images, notifications, and replay**: Kitty/Sixel thumbnails render viewed/generated images
  outside selectable text, and composer drafts preview attached local images in a bordered
  thumbnail strip (using Kitty/Sixel when supported and Unicode half-block truecolor rendering as
  a universal fallback); OSC 7501 (`mekugi`) and OSC 9/BEL report lifecycle status and generic
  desktop alerts (`tui.notifications`). Offline replay (`replay-session`) shares the same renderer
  in read-only mode, plus `--headless` timing output.
