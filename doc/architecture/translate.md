# Patch rendering

## CTR-TRANSLATE-001 — Patch rendering

One translation renderer owns the complete executor patch representation. It receives
an ordered completed change set, renders every file action and required verification
context, and returns only a complete unambiguous carrier. Rendering never mutates the
workspace or changes the engine's evaluated content.

Root-scoped library translation emits root-relative identities. Normal router
translation instead uses the optional metadata directory defined by
`CTR-BOUNDARY-001`, without adding confinement or falling back to router cwd. The
renderer also serves the restricted literal-file-write adapter; eligibility and
execution stay with the caller, and authorization and application stay with Codex.
