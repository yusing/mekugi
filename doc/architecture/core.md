# Virtual workspace and immutable-baseline edit planning

## CTR-CORE-001 — Virtual workspace and immutable-baseline edit planning

The edit engine is the sole owner of command evaluation, logical file lifecycle,
immutable invocation baselines, target resolution, conflict detection, ordered
splices, language-aware finalization, and completed net changes. Apply and
translation consume the same completed result; neither path reimplements the
semantics defined by `REQ-SCRIPT-001`, `REQ-FILE-001`, `REQ-SELECT-001`, or
`REQ-EDIT-001`.

Each touched file retains its original identity and content, current logical path,
pending lifecycle action, and accepted splices. Finalization renders those splices
once, applies supported indentation and language processing, and records the mapping
needed for result projection. No intermediate edit becomes another baseline or target
source within the invocation.

Verified-row identity is shared by readers, target validation, repair context, and
successful state projection. The engine owns target selection and the restricted
pending-coordinate fallback; the shared row component owns only source-byte and
logical-row identity. Syntax adapters report diagnostics through one reducer without
acquiring filesystem, transaction, or output ownership.

The engine receives original files through an authorized workspace boundary and
performs no external writes or process output. Cancellation or failure returns no
completed change set. Successful completion returns the ordered net changes and
projection metadata needed by the state, translation, review, and application owners.
