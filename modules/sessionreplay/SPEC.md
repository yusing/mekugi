# Session replay

## Purpose

Reconstruct timed, read-only session presentation from retained observations.

## Scope

The module owns offline timing, synthetic streaming, corpus bounds and semantic
playback controls. The application exposes `mekugi replay-session --session ID`
and feeds scheduled observations into the same presentation used by live sessions.
Terminal keys, painting and profiling remain application-owned. See
[terminal replay](../../apps/mekugi/internal/terminalsession/SPEC.md#offline-playback)
and the [profiling guide](../../docs/contributing/development.md#profile-live-sessions-and-replay).

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
file changes, MCP calls, agent lifecycle and collaboration, compaction and image-view activity.
MCP arguments remain available at the recorded start; results and errors appear
only at completion.
Collaboration preserves recorded assignment/message bodies and supplied child model
settings. Only successful completed calls claim delivered assignments or messages;
missing facts remain unknown.
Unsupported item kinds are counted. Retained hooks use each loaded workspace and
thread; unavailable or dropped history is disclosed. Unfinished hook observations
show an unknown outcome without becoming running work. Private/raw reasoning,
original keystrokes, resize events and live diff previews are absent.

The corpus is bounded to 256 MiB across decoded rollouts and retained hook input,
128 threads and 500,000 generated item/hook events. Source admission and individual
record bounds belong to the backend. Playback duration is at most 24 hours.

With a timing contributor, `--debug-dir` selects `capture.jsonl` for provider
intervals from loaded threads only; [transport diagnostics](../transportdiagnostics/SPEC.md)
supplies this optional source. Without a contributor, replay remains usable and
leaves the source unopened. Multiple contributors are ambiguous and fail
construction. Timing does not infer missing provider activity.

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
uses [a fresh attachment](../../apps/mekugi/internal/sessionpresentation/SPEC.md#attachment-lifetime)
before applying earlier facts. Seeking skips paints
and settles completed output, so initial visibility/collapse timing can differ
from continuous playback. Interactive completion holds the final frame; headless
completion exits.

## Lifecycle

Each playback owns a bounded timeline, cursor and recorded clock. The application
supplies elapsed time and resets presentation when seeking backward. Replay starts
no independent timer or background worker and retains no live-session resources.
Its application attachment constructs the scoped presentation, Conversation and
Activity owners and the actual terminal directly. It shares observation reduction,
publication, output hydration and renderer delivery with live sessions, without
constructing a live application controller or request ledger. Backward reset
retires those scopes and preserves only terminal preferences and physical file-read
capacity until an outstanding read finishes.

## Codex connection

The required Rollout binding supplies [Codex's offline decoding and source-identity
verification](../../backends/codex/SPEC.md#supplied-offline-rollouts). An unbound or
incomplete source fails explicitly. Messages and public reasoning share
[Conversation's interpretation](../conversation/SPEC.md#transcript-observations).

## Records

Replay publishes no records. [Session history](../sessionhistory/SPEC.md) owns
retained hook facts; replay reads them without permission changes or creating
storage directories.

## Failure

Malformed input, inconsistent identities, exceeded bounds and explicitly selected
unreadable timing fail the command. Missing hook history remains an explicit
coverage gap. Cancellation and rendering failures propagate. Exit status is 0
for playback/quit/help, 2 for invalid CLI arguments and 1 for read, validation,
profiling, cancellation or rendering failure.

## Acceptance

Acceptance requires deterministic UTF-8 synthesis, recorded-clock controls,
backward-reset isolation, source rejection and operation without optional timing.
Application integration preserves item interpretation, fork ownership, Main/child
history and read-only presentation. Offline replay does not establish live host
behavior or original terminal latency.
