# Session replay

## Purpose

Reconstruct timed, read-only session presentation from retained observations.

## Scope

The module owns offline timing, synthetic streaming, corpus bounds and semantic
playback controls. The application exposes `mekugi replay-session --session ID`
and feeds scheduled observations into the same presentation used by live sessions.
[Codex](../../backends/codex/SPEC.md#supplied-offline-rollouts) owns rollout decoding;
[session history](../sessionhistory/SPEC.md) owns retained hook facts;
[transport diagnostics](../transportdiagnostics/SPEC.md) optionally contributes
provider timing. Terminal keys, painting and profiling remain application-owned.

Replay starts no backend, provider, command, approval, filesystem observer or
writable store. It restores facts, never processes, handles or authority. Recorded
content cannot submit input. There is no second live transcript.

## Behavior

### Sources and coverage

The command accepts a literal session ID, not a path. The application searches
`sessions` and `archived_sessions` under `CODEX_HOME`, defaulting to `~/.codex`,
and verifies the first metadata identity. Missing or ambiguous Main sources fail.
Referenced children are selected among sibling rollout files by literal thread ID
and verified metadata; missing or ambiguous children are disclosed. Copied ancestor
items and turn lifecycles retain their original ownership.

Supported facts include messages, public reasoning summaries, command output,
file changes, agent lifecycle and collaboration, compaction and image-view activity.
Unsupported item kinds are counted. Retained hooks use each loaded workspace and
thread; unavailable or dropped history is disclosed. Unfinished hook observations
show an unknown outcome without becoming running work. Private/raw reasoning,
original keystrokes, resize events and live diff previews are absent.

The corpus is bounded to 256 MiB across decoded rollouts and retained hook input,
128 threads and 500,000 generated item/hook events. Source admission and individual
record bounds belong to the backend. Playback duration is at most 24 hours.

With a timing contributor, `--debug-dir` selects `capture.jsonl`; its owner supplies
provider intervals for loaded threads only. With no contributor, replay remains
usable and leaves the optional source unopened. Multiple timing contributors are
ambiguous and fail construction. Timing does not infer missing provider activity.

### Recorded clock and synthetic streaming

Events are stably merged by recorded time. Item start and completion stay fixed.
Text and command output appear through seeded UTF-8 deltas strictly inside each
nonzero interval, concatenating to the retained text. Synthesis does not extend
command/provider duration or remove user waiting gaps. Zero-duration items
complete directly. Streaming is explicitly identified as simulated.

Speed is finite, 0.1–100, default 1.0. Relative speed intents use the same ordered
presets in both directions: 0.1, 0.125, 0.25, 0.5, 1, 2, 4, 8, 16, 32, 64 and 100.
A custom speed moves to the nearest preset strictly in the requested direction;
limit steps do nothing. Elapsed wall time is charged at the preceding speed and
pause state before any control changes it. Presentation clocks and durations use
recorded time; pause freezes them.

`--from` and `--until` select a positive interval inside the recorded duration;
earlier events reconstruct the initial state. Seek intents move ten recorded
seconds within that interval. Restart resumes at its start. Backward seeking
rebuilds isolated presentation, including module-owned scope state, before applying
earlier facts. Future content cannot survive that reset. Seeking skips paints
and settles completed output, so initial visibility/collapse timing can differ
from continuous playback. Interactive completion holds the final frame; headless
completion exits.

## Lifecycle

The composed service holds decoder and optional timing registrations only. A
query owns its bounded timeline; one attached player owns its cursor and recorded
clock. The application owns elapsed-time ticks and supplies reset/application
callbacks. No module goroutine, live timer or store lease is started or restored.

## Codex connection

The required Rollout binding supplies decoded observations and source-identity
verification. An unbound or incomplete source fails explicitly. Reads never start
Codex or convert observations back into RPC JSON for presentation.

## Records

None. Existing owner-validated facts are read without publication, permission
changes or creation of storage directories.

## Failure

Malformed input, inconsistent identities, exceeded bounds and explicitly selected
unreadable timing fail the command. Missing hook history remains an explicit
coverage gap. Cancellation and rendering failures propagate. Exit status is 0
for playback/quit/help, 2 for invalid CLI arguments and 1 for read, validation,
profiling, cancellation or rendering failure.

## Acceptance

Descriptor tests use a scripted Rollout binding and real session history without
Codex, terminal or network. They cover timing contribution absence, source
binding, deterministic UTF-8 synthesis, recorded-clock controls and backward-reset
isolation. Application acceptance covers decoded item variants, fork ownership,
Main/child history, source rejection, read-only controls and unchanged renderer
snapshots. Offline replay does not establish live host behavior or original
terminal latency. See [terminal replay](../../doc/spec/native_ui.md#offline-playback)
and the [profiling guide](../../docs/contributing/development.md#profile-live-sessions-and-replay).
