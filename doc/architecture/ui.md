# Presentation boundaries

## CTR-UI-001 — Presentation and native session integration

Presentation consumes observations and user intent. It does not become an execution,
capture, accounting, or replay authority. Observable terminal behavior belongs to the
[UI contract](../spec/native_ui.md); the session dialog consumes the
[capture-owned metrics contract](../../modules/transportdiagnostics/SPEC.md).

Storage and observation work must leave input, cancellation, rendering, and unrelated
lifecycle delivery responsive. Running work outside the UI loop is insufficient if a
shared queue or lock still blocks independent owners. Unresolved metadata and exhausted
observation capacity must remain visible and bounded without terminating unrelated work.

- `libraries/diffmodel` owns captured file differences, composition, merge results,
  and file-status classification. It operates on supplied values without filesystem
  reads or terminal types. Callers choose display paths; terminal views own colors,
  labels, geometry, selection, and follow state.
- `internal/ui/activity` owns display blocks, Markdown and
  syntax presentation, source-aware Markdown copy annotations and reconstruction,
  agent labels/colors, and reasoning/status animation.
- `internal/ui/diffview` owns file/change navigation and live preview cards, including
  their viewport and reveal animation. It consumes shared `internal/livediff` models
  and router-produced preview snapshots. Rendering a preview cannot finalize evidence.
- `internal/ui/terminal` owns raw-terminal restoration, cancellable input reading,
  mouse decoding, and shared scrolling primitives. Input cancellation preserves
  caller descriptors and their shared file-status flags. Callers retain pane composition,
  focus, follow policy, and the enclosing session lifetime.
- Router-native `/session` presentation consumes a detached capturer snapshot.
  It owns only view, scroll and retained-exchange selection, never a metric store.
- [The Codex backend](../../backends/codex/SPEC.md) owns newline-delimited stdio RPC and the launched Codex process:
  framing, bounded diagnostics, initialization, and shutdown. It does not import the
  router or terminal UI and does not interpret model responses.

The router remains the integration owner for the native session, composer, transcript
and roster state, and app-server event reconciliation. It adapts authenticated activity,
scoped replay/change observations and accounting into display
values. Shared Markdown annotations travel with rendered fragments through wrapping,
record layouts, gutters and viewport clipping. The router resolves them against the
composed visible frame and removes their private transport before terminal delivery.
A selection snapshots those annotations with its rows. A scrollable selection
retains the full rendered document and keeps its own
viewport, so later streaming cannot change selected or newly exposed source rows.
The snapshot retains Markdown annotations, original click targets, pinned agent headings,
and saved Diff gutters and change attribution. Main and child transcripts, saved Diff,
side-answer docks and output dialogs share the selection mouse and scroll model;
the caller retains composition and dismissal policy. Output dialogs defer live
content refresh while their selection is active. Atomic submitted tokens consume the same bound composer
spans through Codex text elements, rather than recognizing lookalike prompt text.
Preview workers and brokers remain with router observation,
not the view package. The launcher still owns invocation configuration and cancellation
before handoff; the native client never executes model-requested tools.

[Notifications](../../modules/notifications/SPEC.md) owns bounded recovery state,
claims and attached feedback. Terminal integration owns rendering, actual display
receipts, desktop signals and lifecycle titles.

Live host operations have one lifecycle owner, separate from presentation status,
elapsed time, and activity items. Input admission derives from pending submission,
compaction, interruption, shell, replacement, and settings records, not displayed
labels. Each record distinguishes RPC acknowledgement from the host observations
that settle it. Confirmed session replacement retires the source operations as a
unit while retaining queued composer input and the target's resume intent;
rejected replacement leaves the source lifecycle intact.

[Session preferences](../../modules/sessionpreferences/SPEC.md) owns applied and
recovery evidence, resume selection and persistence. The terminal adapter owns
layout validation and application. Host start/resume results and matching
`thread/settings/updated` notifications supply authoritative settings; pending
requests and acknowledgements do not.

Resume obtains identity/workspace through metadata-only `thread/read`, then reads
preferences and the host-selected rollout before `thread/resume` can checkpoint
invocation defaults. The backend validates rollout identity and decodes bounded
history; the preference owner combines that evidence. The application preserves
explicit launch overrides and lets Codex validate the effective settings.
To restore nullable reasoning, the client copies the returned collaboration mode,
changes only reasoning effort to null, and submits `thread/settings/update`:
the RPC's plain `effort:null` means no change. Input and snapshot replacement stay
gated until the authoritative notification arrives. No user configuration is written.

Queued manual compaction is native-client intent, not replayed lifecycle evidence.
The composer tracks continuation intent only for a command queued while Main was
busy. Both the compact RPC acknowledgement and successful compact-turn completion
must precede a new `turn/start`. Waiting user input takes precedence over the one
automatic continuation message. Interrupt cancels that intent immediately; replay
and session switching never revive it. Codex still owns compaction and execution.

Dependencies point from router integration into the client and presentation packages,
not back into router internals. Shared diff rendering remains in `internal/livediff`;
UI extraction does not introduce a second capturer, history store, or transcript.
