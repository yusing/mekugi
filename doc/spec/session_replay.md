# Session UI replay

## REQ-SESSION-REPLAY-001 — Offline timed presentation

`mekugi replay-session --session ID` requires a literal session ID, not a path.
It searches `sessions` and `archived_sessions` under `CODEX_HOME` (default
`~/.codex`), using the exact first metadata ID to identify filename candidates.
Missing or ambiguous sessions fail rather than choosing a rollout arbitrarily.
It reconstructs UI presentation from retained Codex rollout records.
It creates no Codex process, provider transport,
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
Observed hook runs come from Mekugi's retained hook records for each loaded
workspace and thread, because Codex does not persist these notifications.
Replay shows their recorded output and outcomes without restarting hooks;
an observation without completion has an unknown outcome and no running state.
Question and answer presentation is limited to retained semantic items; input
keystrokes, resize events, journal publications, and live diff previews are absent.
Retained semantic journal messages remain visible. Authorized child journals use
retained revisions and recorded host turns to reconstruct mounted and unmounted
hierarchies. Latest stored child items or lifecycle cannot supply historical state;
missing records or unproven ancestry are disclosed. Journal progress uses the
recorded clock, including pause and backward seek. Router-generated journal
transport commands are classified only when the strict
generated shell grammar matches the durable translated carrier for the executing
thread and metadata workspace (or its original empty workspace scope). Mutation
transports and their synthetic output are hidden; read and list transports show
their typed journal operation, as in live sessions. Reading
that provenance creates no store, locks, or writers. Missing, invalid, or unrelated
provenance keeps the command visible and discloses an unverified transport candidate.

Events are stably merged by recorded time. Item start and completion remain fixed;
agent text and command output are not exposed at item start. Synthetic UTF-8 streaming deltas
are seeded and lie strictly inside each item's interval, concatenate to its retained
text, and do not extend command/provider walls or remove user waiting gaps.
Zero-duration items complete without synthetic streaming. Optional `--debug-dir`
reads provider-boundary records from `capture.jsonl` for loaded threads only:
request start is `captured_at - duration_ms`; completion is `captured_at`.

The playback clock advances recorded time at `--speed` (finite, 0.1–100, default
1.0). Interactive +/- moves through the same ordered presets in both directions:
0.1, 0.125, 0.25, 0.5, 1, 2, 4, 8, 16, 32, 64, and 100. At the limits, further
steps in that direction do nothing. A custom starting speed moves to the nearest
preset strictly in the requested direction. Event timestamps and displayed
durations use recorded time, not accelerated
wall time. Pause freezes the playback clock. Backward seeking rebuilds isolated
presentation state and cannot retain future content. `--from` and `--until` select
a positive interval within the recorded duration; earlier events reconstruct
state before the interval. Seeking skips intermediate paints and flushes completed
output; initial visibility and collapse timing are not guaranteed to match
continuous playback. Interactive completion holds the final frame; headless
completion exits. Cancellation and rendering failures propagate.

The renderer is the UI renderer, including its presentation caches and
bounded output retention. The playback bar identifies simulated streaming at all
times and exposes pause, speed, seek, restart, and quit controls. Rendered-output
snapshots cover running, paused, completed, and narrow playback states.

`--headless` uses `--width` and `--height` (160×48 by default, bounded to 20–1000
columns and 8–500 rows). It emits no terminal bytes and returns a JSON timing
summary on stdout; loading/coverage/progress notices use stderr. Interactive
mode requires terminal stdin/stdout and restores terminal state on exit.
The p key pauses playback; Space also pauses outside Journal. `Ctrl-E` uses the
focused pane's [disclosure controls](native_ui.md) as in live presentation.
Focused Journal supports selection with j/k or navigation keys, Space disclosure, namespace
switching, copied durable addresses, and d/Enter for read-only details. Its wheel
scrolls without changing selection. Details support scrolling and close with
Escape or q; Ctrl+C always quits replay. Outside details, Escape or q quits.
Other wheel reports scroll Main; unsupported mouse/navigation reports cannot
become playback commands. These presentation controls never submit host intent. Mouse reporting
is enabled only during interactive playback and disabled on exit.

Profiling uses the shared [diagnostic-build listener](router.md#performance-profiling)
through `bin/mekugi-pprof replay-session`. Captures are requested over HTTP while
playback is running; replay creates no automatic profile files and accepts no
`--cpu-profile` or `--heap-profile` flags. The normal command remains offline with
no profiling listener.
See the [shared profiling workflow](../../docs/contributing/development.md#profile-live-sessions-and-replay).

Frame timings include output pacing, layout, and writes;
write time is also measured separately. Headless writes cannot establish terminal
latency. Accelerated playback preserves event times but changes batching and is
not evidence of original wall-time frame latency. Compare the same inputs, seed,
speed, and dimensions; use 1.0x for representative latency.

Exit status is 0 for successful playback/quit/help, 2 for invalid CLI arguments,
and 1 for input, profiling, cancellation, or rendering failures.
