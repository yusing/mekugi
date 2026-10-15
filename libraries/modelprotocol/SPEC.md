# Shared model protocol

## Purpose and scope

Classify shared model wire facts used by transport, capture, and backend
adaptation. Responses event and item identities, terminal evidence, exact JSON
spelling, and Chat completion reasons have one definition across consumers.

## Behavior

Recognized terminal evidence distinguishes completion, steering, interruption,
failure, and incomplete or unknown state. Combining observations preserves
invalid or failed evidence; later success cannot erase it. A recognized wire
terminal does not itself establish a usable response or authorize tool execution.
Unknown Chat finish reasons cannot establish completion.

JSON encoding used by protocol and retained-record consumers preserves legacy
semantics, sorted keys, unescaped HTML, and the absence of a trailing newline.
Event and item facts retain the protocol's exact identities, including recognized
Codex wire extensions.

## Ownership and acceptance

The library has no provider selection, credentials, transport, process lifetime,
mutable session state, or retained records. The
[Codex Responses adapter](../../backends/codex/responses/SPEC.md) owns translation
and executable-call validation. Capture measures the actual provider wire.

Package tests pin JSON bytes, event and item classification, terminal precedence,
and absent or unknown completion reasons. Transport, backend, and capture tests
exercise those facts at their respective consumers.
