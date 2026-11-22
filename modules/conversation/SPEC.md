# Conversation

## Requirements

- Provide unified composer editing across Main, live delegates, One-off, questions, and approval
  denials with atomic file, skill, selection, and image attachments (including image previews for
  host paths, uploads, and paste) and undo/redo/external-editor support.
- Queued composer input must not cancel an active logical agent wait.
- Support optional `mcat` recall of prior selections, skills, files, and submitted-image
  references via retained read-only files after context resets/compactions without replaying image
  pixels or re-reading unselected file bodies; deduplicate identical supplied source content.
- Support isolated `/oneoff` work-capable forks and read-only `/btw` side questions without
  polluting Main's transcript.

## Current approach (replaceable)

- **Transcript & drafts**: correlates live, restored, and replayed messages and public reasoning
  summaries into neutral values for Activity; owns grapheme-aware draft editing, undo/history (100
  entries + shared Codex `history.jsonl` recall), transient next-message predictions, and queued
  input promotion/restoration.
- **Attachments, presets, and skills**:
  - Forwards `@` / `@!` file/directory references (name-only 2-level gitignore-aware directory
    trees, ≤256 entries / 16 KiB) and `$skill` references for native acquisition; appends supplied
    selections/text uploads (≤96 KiB selection, ≤1 MiB combined text budget) as fenced Markdown.
  - `/presets` manages 10 text-only slots backed by Codex's native `tui.presets` (also surfaced
    in `$name` completions); `/skills` manages native Codex skill toggles while `skills-mgr`
    resolves managed skills.
- **Questions, approvals, and side threads**: shares one family question channel (sync before async;
  optional answer images/selections, option notes and recommended defaults) and per-thread approval records while keeping secret editors
  private to the terminal adapter. `/oneoff` opens one ephemeral work-capable fork (archived on
  close, not restored on resume); `/btw` opens a separate read-only answer dock on Main with tools
  disabled.

  Async answers commit independently by runtime question ID; unanswered siblings remain pending.
  Blocking responses include every question. Host replies use compact choices, written answers, or
  explicit skips; transcript and replay resolve choices against retained question options.
  Terminal previews follow option focus (side-by-side at ≥80 columns, stacked below otherwise);
  Alt+Shift+Up/Down scrolls plain-text previews, Tab adds a note in either mode. Recommendations are
  separate indicators and never submit an answer.
