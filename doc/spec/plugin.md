# Router-local tool plugins

## REQ-PLUGIN-001 — Authenticated executable frontends

In Mekugi mode, plugins are loaded from the `mekugi/plugins` directory beneath
the platform user configuration directory. Each direct regular `.js` or
`.mjs` file is a compiled ECMAScript-module declaration, loaded in lexical
filename order. Directories and symlinks are not declarations; relative
module dependencies may be copied into the immutable snapshot. A missing or
empty directory contributes no configured plugins. TypeScript is an authoring
format, not runtime-transpiled plugin input. Passthrough mode loads none.

Each declaration uses `mekugi-tool-plugin/v1`, a stable plugin ID, and one or
more globally named tools. A configured tool provides an exact Responses custom-tool
specification, bounded string parser, `argv` conversion, and executor-side
`execute` implementation. Bundled `mread`, `mrun`, and `mchanges` are plugin-declared tools with
plugin-owned descriptions and pinned native host executors. Only the bundled declaration may
name those executors; configured plugins cannot claim them. The supported specification is unconstrained text
or a Lark or Rust-regex grammar. Standard JSON-schema function declarations
and arbitrary undocumented fields are not plugin declarations. Tool names
must not collide with another declaration, a Mekugi built-in, or a shell
keyword/built-in that would make a basename ambiguous.

The optional `mekugi:core/v1` import provides deterministic quoted-operand,
logical-row, source-format, Go-lexical, Go declaration-outline, shell-header, and interpreter helpers.
`goOutline(source)` returns declaration and declared-name UTF-8 byte offsets,
per-declaration syntax completeness, and syntax-error offsets using the host Go
grammar. It parses supplied text only; it does not resolve imports or read files.
It grants no filesystem, workspace, process, network, credential, or router
transport authority. Unknown `mekugi:` modules reject during startup.

Before opening the listener or exposing any frontend, Mekugi snapshots and
validates the complete registry. Independent declaration, grammar, identity,
name, API-version, wrapper, and implementation failures are reported together.
An invalid registry starts no partial subset and forwards no Responses request.
A snapshot is immutable for the router lifetime; edits to plugin files require
a new launch. Grammar syntax checks are local and bounded, not a promise to
reproduce every provider complexity limit. Rust regexes use installed `rg`
with configuration disabled; Lark supports the documented common-import
subset. Lookarounds, lazy repetitions, extended mode, terminal priorities,
templates, non-common imports, and `%declare` are not supported. Regex-free
declarations do not require `rg` for grammar validation.

For each tool, the router creates a session-private frontend in the snapshot's
`bin` directory. Its basename equals the tool name and points through the
authenticated wrapper to a pinned copy of the running Mekugi executable.
The worker verifies the invoked frontend, snapshot identity, exact manifest,
and executable identity before dispatch. Replacing the installed binary or
plugin source cannot change an active worker. The launcher prepends only its
own `bin` directory to wrapped Codex's `PATH`; no global command, shared
frontend, MCP server, or Codex configuration change is created. Concurrent
sessions cannot replace each other's frontends. Cleanup removes only the
owning session's resources.

Stock `exec_command` invokes configured tools and built-ins under Codex's cwd,
environment, sandbox, terminal, signals, and process lifecycle. The worker
receives argv unchanged after frontend dispatch and keeps the command's stdin
separate from its JavaScript host control stream. Each configured execution
uses an isolated host. The tool returns stdout, stderr, and exit status once;
Mekugi does not rerun an effect to inspect or replay it. Bundled plugin tools
`mread`, `mchanges`, and `mrun` share the same authenticated snapshot and their existing
host stores or process backend. Generated `mcat`, `msymbol`, and `inspect_file` use it too.

An executor may return bounded `omittedOutput` for managed `mread` recovery.
The worker validates and persists this output before exposing a continuation
reference. Storage failure cannot claim that recovery is available.
`failureClass` is private allowlisted metadata on nonzero results, used for AX
without replacing stderr. `terminationReason` may request cleanup of an
invocation-owned resolver process group, but cannot change the completed
semantic result. Ordinary worker cancellation and host process cleanup do not
create private Codex continuation handles.

Replay retains completed observed stock calls and references without invoking
old plugin workers. A frontend or output continuation is a live session
capability, not something reconstructed by replay. The router does not create
carrier mappings for unchanged stock calls.

Acceptance:

1. Valid configured and built-in frontends resolve through the same pinned
   authenticated snapshot and execute under stock `exec_command`.
2. Complete-registry validation fails before serving if any declaration,
   grammar, or symlink is invalid; no valid subset leaks through.
3. An active registry does not change when installed binaries or plugin files
   are replaced. A new launch can pick up new declarations.
4. Configured stdin remains separate from worker control data, and workdir,
   environment, sandbox, PTY, yield, and signals remain Codex-owned.
5. Session-private PATH directories and cleanup cannot cross into another
   session. Missing, expired, or corrupt snapshots reject rather than silently
   dispatching to an unauthenticated implementation.
6. Bounded omitted output is durable before an `mread` reference is exposed;
   the command's exit status and visible output are not rewritten to claim
   success.
