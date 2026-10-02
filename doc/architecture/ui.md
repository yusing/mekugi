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
A selection snapshots those annotations with its rows, so later streaming cannot
change the selected source. Atomic submitted tokens consume the same bound composer
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

Claude is a second backend of this same UI. `internal/claude` owns its bounded
private-stdio SDK bridge, native event decoding and runtime shutdown.
`internal/session` carries presentation events and user intent without importing
either runtime protocol. `RunNativeSession` in `internal/router` uses the existing
shell, composer, question dock, activity views, preview dock and Diff controller;
it does not construct a Codex client, proxy or synthetic app-server messages.
`cmd/mekugi` selects this backend before any inference router starts. Runtime
intent dispatch remains separate from Codex lifecycle control. Claude keeps its
native permissions, configuration and execution; presentation owns no journal
transactions, capture store or inference transport. Historical transcript events
restore display only, never live argument buffers, approval requests or processes.
Ordinary resume validates SDK session-history identity and workspace before ready,
publishes that verified identity through the neutral session interface and binds
the optional observation owner so saved captures load before new input. Later
native initialization must match that identity; a mismatch terminates the client.
An explicit resume-and-fork launch uses SDK `forkSession`; the native init event
establishes the fork's new identity, not a fabricated Codex thread. Display history
can come from the verified source session, but observation stores remain scoped to
the actual native session and do not infer ancestry or copy parent captures.
Native command metadata and replacement pushes feed the existing slash picker,
including skills and aliases. File completion uses the shared read-only workspace
scan without Codex search APIs; mentions remain native input, with
`client_composed` unset so Claude retains attachment/configuration expansion.
The optional structured-input capability sends ordered native text/image blocks
through the same composer; image encoding is bounded and does not transform bytes.
Runtime-advertised model/effort values feed the shared settings picker. The optional
session settings capability sends identified invocation-local controls; only the
matching native acknowledgement or failure settles a pending control. Claude's
adapter uses `setModel` and `applyFlagSettings`, never the settings-file writer.
Effort presentation distinguishes a requested override from a policy-limited
effective level; subsequent native initialization supplies the resolved model.
Native usage snapshots replace prior cumulative query totals in the shared status
dialog, with optional fields preserving absence. Task IDs and originating tool-use
IDs correlate Activity and roster rows but confer no agent/store authorization.
Task patches merge independently of root-turn completion; stop acknowledgements
settle the control request, while terminal task events settle the displayed task.
Structured Edit/Write intentions feed the shared bounded source reader and review
renderer. Argument completion remains distinct from tool completion, and neither
promotes predictions into durable captured changes.

An explicitly activated capture companion registers SDK observation callbacks
against a launcher-owned authenticated Unix IPC service. Its connection capability
travels through a private inherited pipe, not arguments, environment or native
settings. `runtime_observation.go` binds native runtime/session/workspace/agent/tool
identities to the existing replay, retention, change-index and capture-order owners.
Its source-path adapter and Codex observation share bounded capture scheduling;
workspace snapshots and endpoint/overlap reconciliation remain shared owners.
There is no dummy proxy, Codex request carrier, second diff store or tool executor.
The observation service has only binding, pre-tool, terminal-tool and terminal-task
operations. Calls persist baselines before acknowledgement, and frozen terminal
outcomes survive publication retries without rereading the workspace. A fresh
process restores saved membership without reopening prior observation windows.
Durable publications feed the existing bounded Diff mailbox; overflow resubscribes
from retained captures. Activity capture cards remain separate from native tool
results. The bridge returns empty observational hook outputs and adds no model
context. Missing MCP caller correlation does not grant writable journal authority.

Dependencies point from router integration into the client and presentation packages,
not back into router internals. Shared diff rendering remains in `internal/livediff`;
UI extraction does not introduce a second capturer, history store, or transcript.
