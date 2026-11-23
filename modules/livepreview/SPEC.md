# Live preview

## Requirements

- Stream provisional live diffs while the agent writes stock patches and supported shell edits,
  before host completion.
- Allow preview code content to scroll independently while keeping file headers accessible.
- Previews are strictly display-only: they never execute source, allocate change IDs, or establish
  durable evidence.

## Current approach (replaceable)

- Projects streaming source with producible hunks (stock `apply_patch`, literal `cat`/`tee`
  heredocs, and statically bounded Python/JS/TS file writes) against bounded pre-edit baselines;
  also surfaces observed differences for named/checkpointed writers while later commands run.
  Moves, copies, removals, formatters, and binaries stay in saved Diff rather than predicted
  previews.
- Repeated writes to one path keep their observed preview until the writer settles; matching
  an earlier predicted edit is not a completion boundary.
- Buffers incomplete syntax statements/lines before revealing hunks, collapses large deletion
  blocks to a hidden-line marker while following additions (keeping subsequent rows visible),
  withdraws unprojectable completed patches, and holds each completed card for a brief settling
  window (1.5s) before returning to the transcript.
- Main displays previews for its admitted session family with source attribution; a selected child
  shows only its own previews. State is bounded and attachment-local (not persisted on resume or
  shown in replay), and `/live` toggles visibility without affecting capture.
