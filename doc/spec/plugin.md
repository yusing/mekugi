# Authenticated executable frontends

## REQ-PLUGIN-001 — Authenticated executable frontends

Mekugi exposes `mcat`, `inspect_file`, `msymbol`, `mread`, `mrun`, and
`mchanges` through session-private executable frontends. Their implementations
and descriptions are compiled into Mekugi. Passthrough exposes none.

Codex starts each command through stock execution and owns its cwd, environment,
sandbox, permissions, terminal, signals, and continuation. Frontends receive
unchanged argv and caller stdin. They return stdout, stderr, and exit status
once, without replaying an effect to inspect it.

### Identity and isolation

Before exposing commands, Mekugi validates the complete compiled catalog and
pins its worker executable for the invocation. Each frontend is authenticated
against that catalog and executable. A missing, expired, or altered snapshot
fails rather than selecting a different implementation.

An installed binary update cannot change active workers. Concurrent invocations
have separate PATH entries and cleanup removes only the owning invocation's
resources. No global commands or persistent Codex configuration are changed.

### Output and recovery

All frontends accepting `--max-tokens N` also accept `--max-tokens=N` before
their operand terminator or command boundary. Missing, invalid, out-of-range,
and repeated token budgets use the same diagnostic:
`--max-tokens requires one integer from 1 to 15500 and cannot repeat`.
Continuation commands use `next_call: COMMAND`, never an equals-sign attribute.

Readers and output formatting run in the host-started frontend process. Semantic
queries may start language-specific resolvers with invocation-owned cleanup.
Recoverable omitted output is retained before its `mread` reference is exposed.
Publication failure preserves admitted stdout and existing stderr, appends an
explicit recovery-unavailable diagnostic, and emits no recovery reference. An
existing nonzero status is preserved; a zero status becomes nonzero. Completed
evidence can be read after restart;
replay never starts old workers or restores live process handles.

### Removed plugin loading

Configured JavaScript plugin declarations are no longer loaded or executed.
Existing files in the user configuration's `mekugi/plugins` directory are left
untouched. Native frontends require neither Node.js nor Bun; semantic resolvers
retain their own prerequisites.

Acceptance:

1. All six commands dispatch through the authenticated pinned executable.
2. Replacing the installed binary cannot change an active invocation.
3. An altered catalog or mismatched frontend/executable identity is rejected.
4. Stock execution preserves argv, cwd, environment, stdin, output, status,
   terminal behavior, cancellation, and yielded continuation.
5. Concurrent invocations cannot replace or remove each other's frontends.
6. Recovery references are usable only after their omitted output is retained.
7. Native reading and formatting work without a JavaScript plugin runtime.
