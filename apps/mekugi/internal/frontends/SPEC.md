# Authenticated executable frontends

## Purpose

Own invocation-private executable exposure and authenticated worker dispatch for
`mcat`, `inspect_file`, `msymbol`, `mread`, `mrun`, and `mchanges`. Command modules
own reader parsing, output budgeting, resolver orchestration, and command semantics.
Codex owns stock execution and its process lifecycle. Passthrough exposes no frontends.

## REQ-PLUGIN-001: Stock execution and isolation

Frontends run through stock `exec_command`, including JavaScript `exec` calls.
They receive unchanged argv and caller stdin, and deliver stdout, stderr, and exit
status once. Codex retains cwd, environment, permissions, sandbox, terminal,
signals, cancellation, and yielded continuation. Observation never replays effects.

Before exposing commands, the host validates the complete compiled catalog and
pins its worker executable for the invocation. Each frontend authenticates against
that catalog and executable. Missing, expired, altered, incompatible-version, or
mismatched snapshots fail rather than selecting another implementation.

An installed binary update cannot change active workers. Each invocation has a
private PATH entry; Bash login startup restores that entry without changing
Codex's login-shell setting. Global commands and persistent user configuration
remain untouched. Cleanup removes only invocation-created frontends, snapshot
files, and leased worker resources, preserving concurrent invocations.

Configured JavaScript plugin declarations are not loaded or executed. Existing
files in the user configuration's `mekugi/plugins` directory remain untouched.
Frontends require neither Node.js nor Bun; semantic resolvers retain their own
prerequisites. There is no JavaScript or WASM plugin runtime.

## CTR-PLUGIN-001: Catalog and composition boundary

Application composition selects the compiled catalog. The Codex binding collects
executable registrations; this owner validates names and implementation availability,
authenticates and publishes invocation snapshots, pins workers, and exposes PATH.
Contributions use portable executable leaf names, reject duplicates, and pair a
command description with its compiled implementation and read-observation policy.

Launch recognizes frontend executable identity before application composition and
before launcher operand dispatch. The recognized worker receives the composed
catalog before authenticating its snapshot and selected command. Frontend operands
never select launcher commands: a filename such as `mcp-bridge` remains an operand.
Descriptors declare commands before construction; execution starts only after
successful activation. Stateful commands receive their constructed module service
through typed composition and an explicit authenticated per-call scope.

The [guidance module](../../../../modules/agentguidance/SPEC.md) renders selected,
validated descriptions and command-owned supplements. The catalog identity binds
the rendered guidance, and its invocation snapshot is published before commands
are exposed. Guidance remains call-local. Guidance assembly and metrics are
optional; their absence leaves authenticated catalog validation and dispatch intact.

Read observation uses the caller-supplied AX output path authenticated in the
invocation snapshot. When that path is empty, it falls back to
`MEKUGI_AX_READ_OUTPUT`. Observation failure cannot prevent execution or change
command outcome. Sanitized measurements belong to
[transport diagnostics](../../../../modules/transportdiagnostics/SPEC.md).

## Output and recovery

All frontends accepting `--max-tokens N` also accept `--max-tokens=N` before
their operand terminator or command boundary. Missing, invalid, out-of-range,
and repeated token budgets use the same diagnostic:
`--max-tokens requires one integer from 1 to 15500 and cannot repeat`.
Continuation commands use `next_call: COMMAND`.

Commands run and format output in the host-started worker. They return omitted
payloads or a pure delivery plan over already captured output or a frozen
retained-evidence selection. For coordinated results, the host reserves reference
identities before command-owned final framing and admission. Plans receive
identities, never a store or execution callback.

Worker recovery uses the public attachment, namespace, lease, and catalog APIs of
[session history](../../../../modules/sessionhistory/SPEC.md). That module owns
retained coordination, publication, and retention; this owner supplies the worker's
authenticated scope and coordinates delivery. The worker holds its session lease
through stdout/stderr delivery and releases it on delivery failure as well as
success. No publication transaction spans command execution or stream delivery.
Command modules do not open private history stores to publish omitted output.

The worker publishes recoverable output before exposing any dependent `mread`
reference. Publication failure preserves admitted stdout and existing stderr,
adds an explicit recovery-unavailable diagnostic, and exposes no recovery reference.
An existing nonzero status is preserved; zero becomes nonzero. A command that has
already changed the workspace may instead supply a pure recovery-failure result
preserving its full mutation report and original mutation status. Cancellation
still suppresses delivery. Neither failure nor cancellation reruns the command.

## Lifecycle and replay

Frontend children stay in the stock command's process group so Codex cancellation
can terminate them. Execution scope distinguishes ordinary callers, the host
process group, and a dedicated authenticated worker. Only the dedicated worker
permits process-wide descendant cleanup. A dedicated Linux worker can reap
resolver orphans after an explicit cleanup result; on other platforms descendants
remain in the stock group until command completion. Resolver lifecycle policy
remains with [symbol query](../../../../modules/symbolquery/SPEC.md).

Completed evidence remains readable after restart under session-history retention.
Replay validates retained facts without starting old workers, rerunning effects,
or restoring processes, live grants, or continuation handles. Host effects and
change evidence remain with the [execution boundary](../../../../doc/architecture/execution.md).

## Acceptance

1. All six commands dispatch through the authenticated pinned executable;
   altered identities and incompatible snapshots fail closed.
2. Installed-binary replacement and concurrent invocation cleanup cannot replace
   or remove an active invocation's workers.
3. Stock argv, stdin, streams, status, cwd, environment, permissions, terminal,
   cancellation, and yielded continuation retain their host behavior.
4. Executable recognition precedes composition and operand dispatch; typed
   constructed commands activate before execution.
5. References become visible only after worker recovery publication succeeds;
   failure preserves output and mutation reports according to their delivery policy.
6. Optional guidance, metrics, and failed read observation do not disable dispatch;
   AX output uses the explicitly supplied path.
7. Restart reads retained facts without reviving processes. Native reading and
   formatting work without Node.js or Bun, and configured plugin files are preserved.
