# Presentation boundaries

## CTR-UI-001 — Presentation and native session integration

Presentation consumes observations and user intent. It does not become an execution,
capture, accounting, or replay authority. Observable terminal behavior belongs to the
[native UI contract](../spec/native_ui.md); the dashboard consumes the
[capture-owned metrics contract](metrics.md).

- `internal/ui/activity` owns display blocks, journal-result layout, Markdown and
  syntax presentation, agent labels/colors, and reasoning/status animation. Its journal
  model is a presentation value, not the durable journal or a delivery receipt.
- `internal/ui/diffview` owns file/change navigation and live preview cards, including
  their viewport and reveal animation. It consumes shared `internal/livediff` models
  and router-produced preview snapshots. Rendering a preview cannot finalize evidence.
- `internal/ui/terminal` owns raw-terminal restoration, cancellable input reading,
  mouse decoding, and shared scrolling primitives. Callers retain pane composition,
  focus, follow policy, and the enclosing session lifetime.
- `internal/ui/dashboard` owns the embedded browser interface and its HTTP handler.
  It uses the existing metrics endpoint, without a separate listener or metric store.
- `internal/appserver` owns newline-delimited stdio RPC and the launched Codex process:
  framing, bounded diagnostics, initialization, and shutdown. It does not import the
  router or terminal UI and does not interpret model responses.

The router remains the integration owner for the native session, composer, transcript
and roster state, and app-server event reconciliation. It adapts authenticated activity,
scoped replay/change observations, accounting, and journal publications into display
values. Journal acknowledgements remain with their owner and occur only after the
terminal write succeeds. Preview workers and brokers remain with router observation,
not the view package. The launcher still owns invocation configuration and cancellation
before handoff; the native client never executes model-requested tools.

Dependencies point from router integration into the client and presentation packages,
not back into router internals. Shared diff rendering remains in `internal/livediff`;
UI extraction does not introduce a second capturer, history store, or transcript.
