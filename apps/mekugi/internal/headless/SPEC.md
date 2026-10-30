# Headless application client

`mekugi codex headless --yolo` runs one stdin prompt in a new thread for automation
and offline comparisons. [Launch](../launch/SPEC.md) owns argument and authorization
admission, invocation-local routing, and diagnostics on stderr. This adapter uses
the same server-owned runtime and Thread Control as the interactive client, with
a task-owned session family and no renderer. It owns the JSONL session stream and
exits after Main's logical work completes. Codex retains execution, compaction,
continuation, permissions, and process authority.

The prompt must contain non-whitespace text and fit within 16 MiB. Its original
text is submitted once through the ordinary session input interface. Host messages
from the family are emitted as JSONL objects before their interpretation,
preserving their identities and payloads. A final `mekugi/headless/completed`
event identifies Main only after its logical work and required descendants settle.
Host turns that yield for coordination or compaction do not complete the command.
Unknown or duplicate responses cannot issue another operation or settle a pending
request.

Interactive host requests and asynchronous Main questions fail with instructions
to continue interactively. Failed or interrupted Main turns, lost connections,
output failure, and cancellation fail the invocation. Turn outcomes also reconcile
the invocation's [notices](../../../../modules/notifications/SPEC.md), including
outcomes outside Main. Host diagnostics stay out of stdout. Completion and failure
join the task-owned family's shutdown before returning; no observation replays
backend effects.

The adapter consumes admitted session lifecycle and raw host observations.
[Session control](../sessioncontrol/SPEC.md) owns requests and their correlation;
the Codex backend owns stdio and child-process cleanup. Terminal presentation and
router request adaptation are separate consumers.

Acceptance covers prompt admission, exact one-shot request correlation, unchanged
prompt submission, thread/turn filtering, questions and approval rejection,
notice reconciliation, JSONL ordering, startup diagnostics, cancellation, output
failure, family shutdown, preload delivery, logical waits, and the redirected
launcher entrypoint.
