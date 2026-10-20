# Headless application client

`mekugi codex headless --yolo` runs one stdin prompt in a new thread for automation
and offline comparisons. [Launch](../launch/SPEC.md) owns argument and authorization
admission, invocation-local routing, and diagnostics on stderr. This client owns
the JSONL session stream and exits after a successful Main turn. Codex retains
execution, compaction, continuation, permissions, and process authority.

The prompt must contain non-whitespace text and fit within 16 MiB. Its original
text is submitted once. Host messages are emitted as JSONL objects before their
interpretation, preserving their identities and payloads. A final
`mekugi/headless/completed` event identifies the thread only after its matching
turn completes and every issued request has settled. Unknown or duplicate
responses cannot issue another operation or settle a pending request.

Interactive host requests and asynchronous Main questions fail with instructions
to continue interactively. Failed or interrupted Main turns, lost connections,
output failure, and cancellation fail the invocation. Turn outcomes also reconcile
the invocation's [notices](../../../../modules/notifications/SPEC.md), including
outcomes outside Main. Host diagnostics stay out of stdout. Completion and failure
join host shutdown before returning; no observation replays backend effects.

The client consumes only thread/workspace identity, turn identity/status, and
question presence. [Session control](../sessioncontrol/SPEC.md) owns request
correlation; the Codex backend owns stdio and child-process cleanup. Terminal
presentation and router request adaptation are separate consumers.

Acceptance covers prompt admission, exact one-shot request correlation, unchanged
prompt submission, thread/turn filtering, questions and approval rejection,
notice reconciliation, JSONL ordering, startup diagnostics, cancellation, output
failure, shutdown, and the redirected launcher entrypoint.
