# Session API

## Requirements

- Expose reusable, renderer-neutral session control beyond the browser, with terminal
  semantic-control and dialog parity.
- Keep threads owned by the shared server: client or connector closure leaves active work running;
  server shutdown ends active work while saved Mekugi sessions remain resumable.
- Support uploaded attachments and execution-machine paths with a 32 MiB decoded-upload budget per
  message.
- Preserve queued edits across browser and native clients, including retraction for editing and
  steering queued input into an active turn without losing text or attachments.

## Current availability gap

Secret prompts (`sudo`/GPG) and native-local filesystem/editor actions remain native-only; see
[CONTRACT.md](CONTRACT.md).

## Current approach (replaceable)

- Serves JSON-RPC 2.0 over WebSocket (private Unix socket `mekugi/server/control.sock` with
  kernel-confirmed same-user peer checks for local native clients; forwarded via
  [Connector](../connector/SPEC.md) for remote clients) plus authenticated HTTP resource endpoints
  for full content and images.
- Tracks runtimes by confirmed logical thread ID (never PIDs). Native children register under the
  same managed invocation; each runtime publishes detached snapshot/patch streams and shared family
  navigation/review projections.
- Local auto-start coordinates via a `.startup.lock` and readiness probe without spawning competing
  servers.
- Wire methods, envelopes, error statuses, and budgets are specified in [CONTRACT.md](CONTRACT.md).
