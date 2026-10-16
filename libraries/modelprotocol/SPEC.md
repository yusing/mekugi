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
JSON-value decoding preserves number precision and spelling. Comparison ignores
object member order and equivalent string escapes. Duplicate names and invalid
UTF-8 retain the wire's permissive decoding behavior.
Event and item facts retain the protocol's exact identities, including recognized
Codex wire extensions.

Raw input and item views preserve unknown provider fields while exposing their
standard text, message, and tool-call shapes. Content changes retain unrelated
fields; consumers decide which roles or carriers may change. String decoding
keeps the existing permissive wire semantics, including null-as-empty string.
The protocol helpers do not authorize an instruction rewrite or execute a call.

## Submitted attachment envelopes

The shared attachment codec recognizes the bounded durable envelope for submitted
file, skill, and selection snapshots. Encoding preserves the submitted frame text;
decoding validates the envelope, size limits, and recognized frame families.
Consumers share frame recognition and guidance spelling. The codec performs no
filesystem reads, composer actions, or model-input projection policy.

## Ownership and acceptance

The library has no provider selection, credentials, transport, process lifetime,
mutable session state, or retained records. The
[Codex Responses adapter](../../backends/codex/responses/SPEC.md) owns translation
and executable-call validation. Capture measures the actual provider wire.

Package tests pin JSON bytes, event and item classification, terminal precedence,
and absent or unknown completion reasons. Transport, backend, and capture tests
exercise those facts at their respective consumers.
