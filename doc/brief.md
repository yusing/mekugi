# mekugi brief

## Problem

Agents describe edits with line-oriented diffs that repeat old content and unchanged
context. Repeating that context increases output, while ambiguous mutation boundaries can
discard a complete atomic script and consume extra model turns.

## Outcome

Provide an atomic edit tool whose mutation commands carry compact, verified targets.
The agent emits replacement or inserted content once; mekugi resolves the target against
an immutable invocation baseline and constructs the ordinary `apply_patch` representation
internally. A routed reader emits copyable line-and-content references that disambiguate
repeated lines, detect stale inspection, and read several already-known files or ranges in
one ordered call without requiring old regions to be re-emitted. A routed ripgrep wrapper
emits those same verified references for complete matching and requested context lines so an
agent does not need to repeat an exact search result through the reader before editing it.
A successful edit report projects compact current rows for each effective content command.
Those reported references can be used directly in the next invocation after line shifts and
language formatting. A focused routed read remains necessary when the report does not contain
the exact next target.

A router-local tool plugin system loads TypeScript-authored, compiled JavaScript declarations
from the user configuration directory, exposes their OpenAI custom-tool specifications, and
translates model calls into ordinary Code Mode tool-call carriers. Common exec translations
use a router-owned wrapper, while tool implementations run only when Codex executes the
translated carrier under its normal sandbox and permissions. An in-process transport capturer
measures provider usage, payload transformation, and actual provider-versus-delivered tool shapes
without adding a proxy or listener.

The mandatory built-in `shell` plugin accepts one free-form script and exposes its normalized interpreter
and exact body as `shell <interpreter> <program>` in the translated Codex exec carrier. The fixed,
shared `shell` helper uses `CODEX_THREAD_ID` to read the current router runtime path, then replaces
itself with that authenticated worker. The executor preserves standard input for program
data. A compact shebang selects the interpreter, while a missing shebang selects `bash`.
Bash and sh basenames, including direct paths, use the router worker's `mvdan/sh` Bash or POSIX
evaluator. That evaluator dispatches hcat, hgrep, hsymbol, and inspect_file directly from the
authenticated snapshot without executable frontends or another router worker. Optional directives
use one `#!key=value` syntax: `#!cmd=` wraps that canonical shell helper
command in one user-supplied command template, while `#!params=` forwards a JSON object, except
`cmd`, through the typed exec carrier. A present `login` value must be `false`. The router always
rewrites the custom `exec` tool either directly inside an `additional_tools` item for app-server
traffic or inside that item's `functions` namespace for CLI traffic. It removes that owner's
`exec_command` section and introductory `tools.exec_command` example, derives the request-specific
parameter shape, omits `cmd`, and appends only that sanitized shape to `shell`.
The supported Code Mode and native carrier shapes are defined by
[the plugin contract](spec/plugin.md); direct `functions.exec` entries remain unsupported.

Yielded execution follows the result/continuation contract in [REQ-SHELL-001](spec/shell.md).

The historical benchmark remains the end-to-end authority: correctness must match the
native edit path, output tokens must be lower, and input, reasoning, request count, and
wall time must remain close to control.

## First-draft scope

- Multiple UTF-8 files opened in sequence by `in PATH` or created by `new PATH`.
- Private hcat commands accept one existing file and an optional range; shell scripts batch reads
  as separate hcat commands.
- Private hgrep commands accept familiar ripgrep matching, context, and file-selection arguments
  and emit complete UTF-8 result rows as copyable path-and-`LINE:HASH` results.
- Mutation-owned complete-line, inclusive line-range, and anchored literal targets.
- Replacement, insertion immediately before a line or text destination, EOF append, and
  deletion.
- Optional positive multiplicity for repeated anchored literal mutations.
- File creation, movement, and deletion.
- One immutable baseline per touched existing file for the complete script; overlapping
  mutations reject instead of rebasing or guessing.
- Basic `Apply` validates, stages, and commits a complete change set, returning only an error.
- `ApplyForHost`, `ApplyForHostRoot`, and `TranslateForHostAt` return
  `HostTranslation` with the rendered report, final state, and diagnostics.
- Router-only target correction through `functions.hpatch_recover` with hashed command handles; an unchanged target rejects before reevaluation, and ordinary `functions.hpatch` and root APIs have no recovery mode.
- Capture-owned provider usage, cache, protocol, tool, HPATCH-delivery, and completeness metrics.
- Historical-commit benchmark tasks with hidden graders, paired randomized attempts, and
  structured artifacts.
- Router-local tool plugins discovered from `mekugi/plugins` beneath the platform user
  configuration directory, with complete-registry startup validation.
- Model-visible custom-tool declarations using unconstrained string input or OpenAI-supported
  Lark and regex grammars, typed translation into Code Mode carriers, and executor-side tool
  implementations.
- Actual provider-emitted and Codex-delivered tool payload measurements in the structured capture
  endpoint, without synthetic stock commands or executor results.
- The repository `plugins/shell.mjs` source is embedded as the mandatory built-in shell, with
  optional interpreter, command-template, and JSON parameter directives.

## Public surface

- Basic root Go API: `Apply` validates the complete script before ordered filesystem
  updates and rollback attempts. It is not crash-atomic or reader-isolated; an
  application error does not imply that no writes occurred.
- Host APIs: `ApplyForHost`, `ApplyForHostRoot`, and `TranslateForHostAt` return
  `HostTranslation` for report, state, and diagnostics.
- `mekugi --mode mekugi|passthrough codex`: expose model-visible hpatch and shell tools with
  private shell-internal hcat, hgrep, hsymbol, and inspect_file commands, or the unchanged
  control path. `mekugi` mode defaults to native text and Mentor Handoff; CTP/2 is opt-in.
  Passthrough stays native.
- inspect_file outline spans are copyable `LINE:HASH` identities without source bodies.
- `mekugi/plugins` beneath the platform user configuration directory: the configured tool-plugin
  discovery surface; the router has no plugin command-line flags.
- `shell`: a mandatory built-in unconstrained custom tool whose translated exec carrier shows the
  interpreter and exact script body, optionally with `#!cmd=` and request-specific `#!params=`
  assignments.
- `make install`: regenerate the embedded plugin bundle and install `mekugi` plus the fixed
  `shell` helper without installing private command files or changing Codex configuration and
  instructions.
- `mekugi-bench validate --manifest TASK.json` and `mekugi-bench run`: validate and run
  paired historical-commit evaluations.
- Script commands: `in`, `new`, `mv`, `rm`, `type`, and `add`.
- `type` replaces its explicit target; an empty target-bearing value deletes the target,
  including an owned line or range terminator. `add` inserts before a line or text
  destination; `add EOF` appends.
- Immediately after `new`, targetless `type` may initialize the empty file once.
- A target is a copyable hcat row, an inclusive pair of rows, or a row-anchored literal
  with optional multiplicity, as specified by `REQ-SCRIPT-001`.

## Non-goals

- Content movement without re-emitting the moved content; `mv` moves complete files only.
- Selecting content introduced earlier in the same script. Dependent edits use a later
  inspected invocation.
- A guarantee that successful report references cover every possible later target, or retention
  of prior routed-read rows so the engine can predict a later edit.
- Word diffs, translated-patch retention in model-visible history, and caller-selected final
  report ranges.
- Interactive editor UI, binary files, non-UTF-8 files, remote plugin discovery, runtime
  TypeScript transpilation, hot plugin reload, or search semantics beyond the installed
  ripgrep executable.
- Bundling configured example plugins into the router, storing the body in an intermediate file,
  overriding the executor working directory or environment, or printing script source into
  program output.
- A new patch interchange format beyond the compact command script and translated
  `apply_patch` output.
- AST-specific mutation commands or language-specific editing frameworks.
- Remote dataset discovery, hosted benchmark orchestration, exact-reference-patch grading,
  or automatic cost conversion.

## Constraints

Constraints are defined by their interface owners:

- [Script framing](spec/script.md), [verified targets](spec/select.md), and
  [search](spec/grep.md) own syntax and target validation.
- [Output](spec/output.md) owns failure atomicity, public return contracts, language validation,
  and current report references; [state projection](architecture/state.md) owns formatting-aware
  coordinates without duplicate content storage.
- [Plugins](spec/plugin.md) and [their boundary](architecture/plugin.md) own registry validation,
  authenticated frontends, typed carriers, and executor lifecycle.
- [Benchmarks](spec/benchmark.md) and [metrics](spec/metrics.md) own correctness grading and
  measured performance evidence.
