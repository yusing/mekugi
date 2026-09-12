# Current-codebase audit, 12 September 2026

## Scope and result

The audit covered the current codebase, not only a pending diff: goal fulfillment,
features, the user experience of running Codex through Mekugi, agent experience,
AX evidence, router performance, observability/debuggability, test waits,
instructions, architecture, and project structure.

Twelve independently inspected slices address the confirmed findings below.
The priority remained lower cost, clearer and recoverable workflows, and
evidence-backed performance, while preserving Codex execution authority.
No broad package restructuring, UI redesign, installation, or live paid-provider
experiment was performed.

Base revision: `291e2db13d53a7f95accee9d172b5a7ee5019894`.
Reviewed implementation head: `6b4ae92`.

## Findings and delivered changes

| Slice / commit | Findings and resolution |
| --- | --- |
| S1 / `038aca0` | **E1–E4, immutable editor state.** Reuse finalized content, unify blocked/reserved path sets, publish the already-validated edit slice, and use the portable verified-row type directly. Preserve conflict atomicity, tombstones, formatting offsets, and line endings. |
| S2 / `b209aef` | **R1–R4, exact source readers.** Map ripgrep byte offsets to logical rows; preserve BOM bytes and parser coordinates; reject implicit transcoding/non-UTF-8 source; bound JSON events before accumulation. Repeated-row references are checked through the edit consumer. |
| S3 / `eb65fd5` | **G1–G4, Grok continuation.** Keep function arguments native JSON through CTP conversion, preserve multipart identities, accumulate streamed arguments without repeated concatenation, and seal terminal choices while admitting usage trailers. |
| S4 / `3c066a5` | **M1–M2, A1–A2, trustworthy evidence.** Measure Chat assistant/tool content through the shared capturer; reject malformed UTF-8 journals before decoding; consume persisted command-completion timestamps without manufacturing timing from missing or invalid events. |
| S5 / `0df6323` | **J1–J2, durable journals.** Authorize relative journal reads from durable thread relationships after restart, retain conflict checks, and skip unchanged durable writes without skipping locked reads. |
| S6 / `554856f` | **H1–H3, Mentor fulfillment.** Account for completed responses before journal successors, prevent older recursion from overwriting newer usage, exclude child non-turn metadata, and remove unused request state. |
| S7 / `d7cc425` | **P1–P2, T1–T3, replay and transport.** Persist exact rendered carriers, publish request reconciliation only after success, decode immutable WebSocket event type outside the pool lock, reuse terminal response maps, and remove an unnecessary SSE clone. |
| S8 / `9e82726` | **S1–S3, shell cost.** Send only mixed-carrier fields consumed by JavaScript, bound retained row spans before joining token-limited output, and remove a sole-caller process wrapper while retaining the authoritative process owner. |
| S9 / `256847d` | **D1–D4, diagnostics.** Preserve known handshake status and synthesized terminal/write failure codes, reject a failed initial debug write, and remove an unused commentary helper. Later auxiliary failures remain non-invasive. |
| S10 / `b11b31e` | **U1–U3, Codex launch experience.** Preserve prelaunch SIGINT through a joined handoff, show content-free catalog progress without affecting success, bound terminal output width, and distinguish cancellation/deadlines from configuration failures. |
| S11 / `1182381` | **I1–I6, instruction integrity.** Replace destructive section/prefix heuristics with complete pinned conflict fragments; preserve caller policy, fences, continuations, and multipart text; verify idempotence; clarify atomicity and carrier guidance; add concrete repository regression rules and remove duplicate check rows. |
| S12 / `6b4ae92` | **V1–V4, repeatable test costs.** Reuse immutable real-description/configured registries while isolating mutable state, shrink pagination data while requiring intermediate pages, and exclude archived benchmark source from root Go discovery with a nested module. Archives remain intact. |

## Validation and measurements

Focused acceptance and regression checks preceded each slice commit. Independent
correctness and simplification inspections covered the slices and their corrections.
Final inspection of the exact base-to-head range used three bounded scopes:
provider/evidence/state integration; editor/readers/shell/test simplification;
and diagnostics/launch/instructions. All approved without remaining confirmed blockers.

The baseline and final commands were `go test -json -count=1 ./...`.
`-count=1` disables test-result caching, not the compiler cache.

| Measurement | Baseline | Final |
| --- | ---: | ---: |
| Approximate invocation wall interval | 142 s | 79 s |
| Router package execution | 105.910 s | 74.735 s |
| Toolplugin package execution | 38.928 s | 37.877 s |
| Command package execution | 14.310 s | 14.109 s |
| Authenticated pagination test | 10.61 s | 5.35 s |
| Passing test/subtest events | 3,066 | 4,207 |
| Skipped test/subtest events | 22 | 22 |

The baseline had no assertion failures but exited unsuccessfully because two
archived benchmark source packages lacked embedded templates. The final full run
exited successfully with no package or assertion failures. The archive module
boundary corrects discovery; it does not repair or delete historical evidence.

These are single before/after runs, not a cold-cache experiment, repeated statistical
sample, or guarantee. Package durations overlap. Build/cache and scheduling differences
contribute to the wall-time comparison, so the entire wall-time reduction cannot be
attributed to test-fixture changes.

Scoped allocation benchmarks also improved:
- Finalized editor-content access: 57,352 bytes / two allocations to zero.
- Bounded shell capture: approximately 8.4 MB / three allocations to
  144 bytes / one allocation.

These allocation results do not establish whole-session latency or provider-token savings.

Focused evidence includes 189 final router instruction checks, 21 contribution
checks, 59 command-package checks, targeted startup and state/transport race checks,
and reader references applied through the real worker/edit boundary.

The final Bun suite passed all 268 tests across nine files. Tagged native journal
acceptance passed with installed Codex and a local mock provider. Benchmark
build-input exclusion and router configuration checks passed. Stripped temporary
Mekugi and shell-helper binaries built successfully, and the launcher exercised
installed Codex 0.154.0 through `codex --version` without provider inference.

Raw baseline/final Go event logs and focused test output were retained for this
session under `/tmp/mekugi-audit.RCVTgk`; this report preserves the summary after
temporary logs expire.

## Decisions and evidence gaps

- Existing portable core/WASM, registry/runtime, and process-owner boundaries have
  real consumers and responsibilities. A broad package reshuffle would add churn
  without an established improvement.
- The legacy replay fallback remains because existing durable records must resume.
  It is not an accidental compatibility layer.
- Real external-process cleanup and timeout cases remain. The 20,000-row closed-pipe
  fixture was not reduced without a portable pipe-capacity/read-ahead argument.
- Native journal acceptance uses installed Codex with a local mock provider.
  It does not establish command-producing native AX timing: that path is supported
  by producer-source evidence and consuming fixtures only.
- No live Grok-provider run was performed.
- Stable narrow-terminal output is tested, but terminal resize/reflow has no
  terminal-emulator coverage.
- `pjdoc` is unavailable and was explicitly waived. Source/consumer checks do not
  replace that validator or establish runtime instruction reload.
- Three observations from using Mekugi were deferred, not fixed: child completion
  notifications omit result bodies; delegated answer association can lack an
  attachable user question; and invoking the fixed shell helper without piped
  script input produced a low-level deadline error. The last observation does not
  establish failure of the normal piped workflow. The requested local session issue log is
  retained outside commits under the repository's existing exclusion policy.

## Temporary runnable build

The session build includes stripped `mekugi` and `shell` binaries plus a launcher
that prepends their temporary directory to PATH. No installed binary was replaced.

```sh
/tmp/mekugi-audit-build.LXfOpv/run codex
```

The smoke command `/tmp/mekugi-audit-build.LXfOpv/run codex --version` passed.
Temporary files may be removed by the operating system; they are not an installation.
