# Authenticated executable frontends

## Requirements

- Keep bounded source reads, symbol/outline lookup, bounded command execution, output recovery, and
  change review/undo batchable through stock Codex execution (`exec_command` / JS `exec`),
  preserving stock parallel/batch control, permissions, sandbox, and continuation semantics.
- Require neither Node.js nor Bun for built-in helpers (semantic resolvers retain their own
  language-server prerequisites).

## Current approach (replaceable)

### Catalog, pinning, and isolation

- Exposes compiled Go helpers (`mchanges` and internal `mreload`) on an invocation-private `PATH`
  entry (restored across Bash login startup without modifying user config). Native runtime helpers
  (`mcat`, `mfinspect`, `msymbol`, `mrun`) are served by `mekugi-app-server` via `codex-arg0`.
  Legacy JS plugin loading is removed; existing plugin files stay untouched.
- Launch validates the descriptor-contributed catalog once and pins an immutable worker executable
  image per invocation (shared across server invocations) so installed-binary updates cannot alter
  running workers.
- Each worker authenticates against its invocation snapshot before running; altered, expired, or
  mismatched snapshots fail closed. Workers stay in Codex's process group for cancellation and
  receive unchanged argv, stdin, cwd, and environment.
- Cross-backend delegates share the parent's retained namespace while keeping caller-scoped
  implicit selections (`CODEX_THREAD_ID`).

### Session storage and observation

- Go workers publish retained snapshots and frozen files through
  [Session history](../../../../modules/sessionhistory/SPEC.md) under a session lease before
  returning references. Publication failure preserves admitted stdout/stderr (or full mutation
  reports), emits an explicit diagnostic without a broken reference, and never reruns the command.
- Optional AX read observation writes to the authenticated read-log path without affecting command
  outcomes.
