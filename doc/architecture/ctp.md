# Router-owned compact provider representation

## CTR-CTP-001 — Router-owned compact provider representation

One request-scoped router component owns CTP/2 encoding after ordinary Mekugi projection
and decoding before ordinary response restoration. It preserves the selected native
instruction carrier, transforms only the strings allowed by `REQ-CTP-001`, and discards
its dictionaries and visible-output sources on every terminal path.

Each eligible string is evaluated independently. Prior-output references use only content
visible in that request, so appending history preserves existing sources while compaction
or branching removes unavailable ancestry. Tool identity and newly emitted executable
payloads remain native.

CTP/2 owns no tool registry, edit semantics, cross-request history, provider usage, or
metrics. The transport capturer observes the native and encoded boundaries and measures
actual differences without receiving dictionaries or text. Wire rendering substitutes
only changed fields instead of reserializing unrelated request content.
