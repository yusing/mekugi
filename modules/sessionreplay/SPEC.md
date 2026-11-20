# Session replay

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): provide read-only
offline replay of retained sessions through the shared UI presentation without starting Codex,
hitting providers, mutating storage, or re-executing recorded effects.

## Current approach (replaceable)

- Exposed via `mekugi replay-session --session ID` (and through the Web UI via `session/start`
  replay). Discovers Main and sibling child rollouts under `$CODEX_HOME/{sessions,archived_sessions}`
  by verified thread identity and feeds decoded observations (messages, public reasoning, commands,
  file changes, MCP calls, agent lifecycle, hooks, compaction) into the shared presentation
  reducer.
- Reconstructs a deterministic recorded clock with seeded synthetic UTF-8 streaming (explicitly
  labeled simulated), pause/resume, speed presets (0.1x–100x), `--from`/`--until` ranges, ±10s
  seek (backward seek resets attachment cleanly), optional `--debug-dir` provider timing overlay,
  and `--headless` timing output.
- Bounded to 256 MiB decoded corpus, 128 threads, 500,000 events, and 24 hours playback duration.
  Private reasoning, live previews, and original keystrokes are absent.
