# Session API contract

Wire contract for [Session API requirements](SPEC.md#requirements). Endpoint paths, protocol
version, field names, and queue bounds below are replaceable across coordinated consumer updates;
the 32 MiB decoded-upload budget is a required message constraint.

Generated TypeScript wire types (`protocol.ts`) are authoritative for exact payload schemas; run
`go run ./apps/mekugi/cmd/sessionapi-schema` to regenerate and `make check-generated` to verify.
TypeScript types do not grant endpoint authority.

## Transport, version, and authority

- **Protocol**: JSON-RPC 2.0 text messages (one object per message; no batches or client
  notifications) over WebSocket subprotocol `mekugi.control.v9`.
- **Endpoints**:
  - Local native: `/api/native/control` over private Unix socket with kernel-confirmed same-user
    peer admission.
  - Remote human: `/api/control` via [Connector](../connector/SPEC.md).
  - Readiness: `GET /api/version` requiring `Mekugi-Protocol: 9` on request and response (409 on
    mismatch; never spawns a competing server).
- **Initialization**: first WS message must be `initialize` with `{"version":"9"}` (plus `token` on
  remote connections, stripped by the connector before forwarding). Response includes `serverTime`
  (Unix ms).
- **Authority**: the endpoint determines authority; caller fields never elevate access. Remote
  connections cannot invoke native-only methods (`native/start`, `source/skills`, `session/redraw`),
  use native attachment IDs, or answer secret questions (secret content is redacted in all remote
  projections).

## Methods

Request params accept optional `thread`, `startup`, `subscription`, and `body`. IDs name confirmed
logical threads, never PIDs.

| Method | Scope / purpose |
| --- | --- |
| `session/options` | Execution-machine recent workspaces and offered models |
| `session/list`, `session/read` | Top-level session list / single confirmed thread entry (includes `parentThread` for children) |
| `session/start`, `session/end` | Create live, resume saved, or start read-only `replay`; end/join owned runtime |
| `session/subscribe` | Stream session list (no `thread`) or shared thread publications (`snapshot`/`patch`/`exit`) |
| `session/page` | Revision-bound retained feed, roster, saved-review, or live-preview metadata page |
| `session/input`, `session/shell` | Submit prompt + attachments, or run host shell command |
| `session/answer`, `session/decide` | Answer pending non-secret questions / decide pending approvals (approve-once or deny) |
| `session/interrupt`, `session/control` | Interrupt active work / control recorded replay (`toggle-pause`, `faster`, `slower`, `back`, `forward`, `restart`, `seek`) |
| `view/attach`, `view/intent` | Attach per-client semantic view stream and dispatch versioned semantic controls/dialogs/recovery |
| `native/start`, `source/skills`, `session/redraw` | Native endpoint only: atomic start+subscribe, native skill metadata, redraw |
| `subscription/cancel`, `request/cancel`, `publication/ack` | Cancel stream or pending request; acknowledge in-flight `delivery` ID |

## HTTP resource endpoints

Large payloads and images stay outside the control WebSocket via separate HTTP requests (same-user
Unix socket with `Mekugi-Protocol: 9`, or connector with `Authorization: Bearer` + admitted Origin):

- `POST /api/resources/content`: fetch complete deferred publication/detail content by published
  reference.
- `POST /api/resources/image`: fetch raster bytes for a published entry's bound image identity.
- `POST /api/resources/attachment-image`: fetch raster bytes for a workspace-resolved host path
  preview (returns 415 for non-raster files; never fetches URLs).

## Delivery, errors, and budgets

- **Events**: `subscription/event` carries `{subscription, kind, data}` (`sessions`, `snapshot`,
  `patch`, `exit`) plus optional `delivery` ID; `subscription/closed` terminates a stream.
  Explicit `exit` carries the joined outcome; EOF without `exit` is transport loss.
- **Error semantics**: `error.data.status` carries HTTP-style status (`400` malformed/invalid,
  `403` native-only method/field on remote, `404` missing thread or resource, `405` disallowed on
  replay, `409` admission conflict or version mismatch, `413` body/upload limit, `415` non-raster
  on `attachment-image`, `500` internal failure, `502` startup failure, `503` unavailable);
  `error.data.outcome` is `not_admitted` or `unknown`.
  - Stale page/resource reads return JSON-RPC code `-32002` (`RPCReadSuperseded`, status 409):
    clients discard silently and wait for the next publication rather than showing an error.
  - **No automatic mutation retry**: after transport loss, unresolved mutations have `unknown`
    outcome and are never retried automatically; reconnect opens fresh subscriptions/snapshots.
- **Transport budgets**:
  - Max WS server-to-client message: 64 MiB.
  - Shared publication / page window: 32 KiB envelope, ≤50 entries/page, one acknowledged
    `delivery` in flight at a time (oversized entries/sections carry content references for
    `/api/resources/content`).
  - Request body caps: 1 KiB default; 64 KiB for `session/decide` and
    `POST /api/resources/attachment-image`; 1 MiB for `native/start` and `session/shell`; 32 MiB
    decoded uploads (base64-expanded) + 1 MiB text/metadata for inputs, answers, and semantic
    intents.

## Question answers

Question IDs come from the runtime. `QuestionCall.blocking` requires a complete answer set;
async calls admit any nonempty subset, leaving the rest pending. Secret batches remain atomic
and native-only. `QuestionAnswer.choice` is zero-based; `note` attaches to a selected option,
`text` supplies a written answer, and `skip` explicitly skips that question. Options carry optional
descriptions, previews, and a recommendation; recommendations never submit an answer.

## Attachment input

Ordered attachment descriptors on inputs, intents, and written question answers:
- `{"source":"path","path":PATH}`: resolves against the selected thread's execution-machine
  workspace.
- `{"source":"upload","name":LABEL,"data":BASE64}`: content bytes determine image/text handling;
  `name` is a display label, never a filesystem path.
- `{"source":"selection","name":DESCRIPTION,"data":BASE64,"path":SOURCE,"inline":BOOL}`: quoted
  UTF-8 screen selection (optional `path` is provenance only, never read as a file; `inline:true`
  binds an exact `[Selected DESCRIPTION]` marker in `text`).

Combined decoded uploads and selections are bounded to **32 MiB** per message; unreadable or
oversized snapshots emit explicit omission notices rather than silent truncation.

## Current approach (replaceable)

- Local native frames use a `retained` owner bitmask (`main`, `agents`, `review`, `family`) and
  `entryDeltas` sequence references folded across bounded server/client queues; remote human frames
  use full detached projections and redact secrets.
- Server-owned family views derive navigation and aggregate review once per workspace independently
  of connected viewers; disconnected clients never stop execution, and reattaching cancels deferred
  idle release.
- Remote unsent submissions stay with the originating live view (`recovery`: `retry`, `edit`,
  `discard`) and clean up on view detach.
