# Session preferences

## Requirements

- Persist and restore per-session applied settings (model, reasoning effort, service tier) and
  terminal renderer preferences (pane visibility/split, focus, interrupt lock, predictions) on
  resume without saving drafts, transcripts, credentials, or approval grants.

## Current approach (replaceable)

- Stores private atomically replaced JSON records (≤4 KiB, outside replay quotas/expiry) under
  `$XDG_STATE_HOME/mekugi/ui` keyed by workspace and thread hash.
- On resume, merges retained settings with newer host rollout evidence before invocation defaults
  apply; explicit CLI overrides win per field. Includes nullable-reasoning recovery tracking for
  explicit host-default effort/tier resets.
- Coalesces renderer layout writes (250 ms debounce, flushed on exit/switch) while flushing
  applied settings immediately; invalid or missing records produce a nonfatal notice and fall back
  to defaults without blocking resume.
