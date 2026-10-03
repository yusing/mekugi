# Presentation boundaries

## CTR-UI-001 — Presentation and native session integration

Presentation consumes observations and user intent. It does not become an execution,
capture, accounting, or replay authority. Observable terminal behavior belongs to the
[native UI contract](../spec/native_ui.md); the session dialog consumes the
[capture-owned metrics contract](metrics.md).

- `internal/ui/activity` owns display blocks, journal-result layout, Markdown and
  syntax presentation, source-aware Markdown copy annotations and reconstruction,
  agent labels/colors, and reasoning/status animation. Its journal
  model is a presentation value, not the durable journal or a delivery receipt.
- `internal/ui/diffview` owns file/change navigation and live preview cards, including
  their viewport and reveal animation. It consumes shared `internal/livediff` models
  and router-produced preview snapshots. Rendering a preview cannot finalize evidence.
- `internal/ui/terminal` owns raw-terminal restoration, cancellable input reading,
  mouse decoding, and shared scrolling primitives. Callers retain pane composition,
  focus, follow policy, and the enclosing session lifetime.
- Router-native `/session` presentation consumes a detached capturer snapshot.
  It owns only view, scroll and retained-exchange selection, never a metric store.
- `internal/appserver` owns newline-delimited stdio RPC and the launched Codex process:
  framing, bounded diagnostics, initialization, and shutdown. It does not import the
  router or terminal UI and does not interpret model responses.

The router remains the integration owner for the native session, composer, transcript
and roster state, and app-server event reconciliation. It adapts authenticated activity,
scoped replay/change observations, accounting, and journal publications into display
values. Shared Markdown annotations travel with rendered fragments through wrapping,
record layouts, gutters and viewport clipping. The router resolves them against the
composed visible frame and removes their private transport before terminal delivery.
A selection snapshots those annotations with its rows. A scrollable selection
retains the full rendered document and keeps its own
viewport, so later streaming cannot change selected or newly exposed source rows.
The snapshot retains Markdown annotations, original click targets, pinned rows,
and saved Diff gutters and change attribution. Main, Activity, saved Diff,
side-answer docks and output dialogs share the selection mouse and scroll model;
the caller retains composition and dismissal policy. Output dialogs defer live
content refresh while their selection is active. Atomic submitted tokens consume the same bound composer
spans through Codex text elements, rather than recognizing lookalike prompt text.
Journal acknowledgements remain with their owner and occur only after the
terminal write succeeds. Preview workers and brokers remain with router observation,
not the view package. The launcher still owns invocation configuration and cancellation
before handoff; the native client never executes model-requested tools.

Live host operations have one lifecycle owner, separate from presentation status,
elapsed time, and activity items. Input admission derives from pending submission,
compaction, interruption, shell, replacement, and settings records, not displayed
labels. Each record distinguishes RPC acknowledgement from the host observations
that settle it. Confirmed session replacement retires the source operations as a
unit while retaining queued composer input and the target's resume intent;
rejected replacement leaves the source lifecycle intact.

The existing native pane-preference owner also retains a complete applied-settings
snapshot and its observation time. Host start/resume results and matching
`thread/settings/updated` notifications are authoritative; enqueue acknowledgements
and pending intent are not. Settings flush atomically without the layout debounce,
including nullable defaults before any turn. The record is keyed by host workspace
and thread; reading a different namespace does not redirect the live writer.

Resume first obtains host identity/workspace through metadata-only `thread/read`,
then reads those preferences and the host-selected absolute rollout validated
against that thread's session metadata. Reads examine complete records in the last
8 MiB, never router cwd or another thread's settings. Newer timestamped
`thread_settings_applied` snapshots override preferences; newer `turn_context`
records supply model/effort without erasing retained tier evidence. Reads precede
`thread/resume`, which can persist invocation defaults. Explicit launch settings
override only matching fields. Codex owns validation and the effective result.
To restore nullable reasoning, the client copies the host-returned collaboration
mode, changes only its reasoning effort to null, and submits `thread/settings/update`:
the RPC's plain `effort:null` means no change. Input and snapshot replacement remain
gated until the authoritative notification arrives. Session preferences contain
no transcript, authorization or live resources; no user configuration is written.
An unfinished nullable restore retains the exact authoritative settings evidence
selected before resume alongside a resume-observation watermark. Fresh-process
retry excludes the transient host checkpoint without discarding newer selected
history or rollout-only evidence. Confirmation clears this recovery record.

Queued manual compaction is native-client intent, not replayed lifecycle evidence.
The composer tracks continuation intent only for a command queued while Main was
busy. Both the compact RPC acknowledgement and successful compact-turn completion
must precede a new `turn/start`. Waiting user input takes precedence over the one
automatic continuation message. Interrupt cancels that intent immediately; replay
and session switching never revive it. Codex still owns compaction and execution.

Dependencies point from router integration into the client and presentation packages,
not back into router internals. Shared diff rendering remains in `internal/livediff`;
UI extraction does not introduce a second capturer, history store, or transcript.
