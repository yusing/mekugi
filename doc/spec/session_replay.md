# Session UI replay

## REQ-SESSION-REPLAY-001 — Offline timed presentation

`mekugi replay-session --session PATH` reconstructs native UI presentation from
retained Codex rollout records. It creates no Codex process, provider transport,
command executor, live observer, journal delivery, or replay-store writer.
Playback keys control presentation only; recorded content cannot submit intent.

The first session metadata record identifies the file. Copied ancestor metadata,
items, and turn lifecycles in child rollouts do not acquire the child's identity.
Referenced child rollouts are discovered by literal thread ID among sibling files;
missing or ambiguous children are disclosed rather than replaced by invented data.
Unrecognized item variants are counted and disclosed. Known items require an ID,
turn, and valid epoch-millisecond start/completion timestamps. Malformed input
fails with a source-qualified error. Inputs are regular files, bounded to 64 MiB
each, 256 MiB across rollouts, 128 threads, and 500,000 generated item events.
Individual records are bounded to 16 MiB and playback duration to 24 hours.

Supported retained items include user and agent messages, reasoning summaries,
commands and output, file-change activity, agent lifecycle and collaboration,
compaction, and image-view activity. Encrypted/raw reasoning is never projected.
Question and answer presentation is limited to retained semantic items; input
keystrokes, resize events, journal publications, and live diff previews are absent.

Events are stably merged by recorded time. Item start and completion remain fixed;
agent text and command output are not exposed at item start. Synthetic UTF-8 streaming deltas
are seeded and lie strictly inside each item's interval, concatenate to its retained
text, and do not extend command/provider walls or remove user waiting gaps.
Zero-duration items complete without synthetic streaming. Optional `--debug-dir`
reads provider-boundary records from `capture.jsonl` for loaded threads only:
request start is `captured_at - duration_ms`; completion is `captured_at`.

The playback clock advances recorded time at `--speed` (finite, 0.1–100, default
1.0). Event timestamps and displayed durations use recorded time, not accelerated
wall time. Pause freezes the playback clock. Backward seeking rebuilds isolated
presentation state and cannot retain future content. `--from` and `--until` select
a positive interval within the recorded duration; earlier events reconstruct
state before the interval. Seeking skips intermediate paints and flushes completed
output; initial visibility and collapse timing are not guaranteed to match
continuous playback. Interactive completion holds the final frame; headless
completion exits. Cancellation and rendering failures propagate.

The renderer is the native UI renderer, including its presentation caches and
bounded output retention. The playback bar identifies simulated streaming at all
times and exposes pause, speed, seek, restart, and quit controls. Rendered-output
snapshots cover running, paused, completed, and narrow playback states.

`--headless` uses `--width` and `--height` (160×48 by default, bounded to 20–1000
columns and 8–500 rows). It emits no terminal bytes and returns a JSON timing
summary on stdout; loading/coverage/progress notices use stderr. Interactive
mode requires terminal stdin/stdout and restores terminal state on exit.

`--cpu-profile` and `--heap-profile` create new mode-0600 files and reject existing
destinations. CPU sampling covers playback, not initial reading or pre-interval
state reconstruction, but includes cold first-frame rendering after a seek.
Frame timings include output pacing, layout, and writes;
write time is also measured separately. Headless writes cannot establish terminal
latency. Accelerated playback preserves event times but changes batching and is
not evidence of original wall-time frame latency.

Exit status is 0 for successful playback/quit/help, 2 for invalid CLI arguments,
and 1 for input, profiling, cancellation, or rendering failures.
