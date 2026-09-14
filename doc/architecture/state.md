# Bounded final-state projection

## CTR-STATE-001 — Bounded final-state projection

The final-state projector consumes only a completed engine result. It owns the bounded
summary and current verified references defined by `REQ-OUTPUT-001`, including command
order, final-path identity, formatting-aware locations, deduplication, and the active-file
fallback. It never rereads the committed filesystem or reconstructs edit state.

Language finalization supplies a source-offset map. The projector uses that shared map
to translate affected extents into final rows, including reordered or deduplicated
language constructs, while retaining its own endpoint and display policies. It does not
own formatting decisions.

One renderer produces the report for both application and translation callers. It
borrows completed content while rendering, retains no duplicate source copy, and emits
references only for successful completed state. The router transports this projection;
it does not calculate coordinates or hashes.
