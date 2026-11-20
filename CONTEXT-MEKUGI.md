# Owner map

Use `docs/reference/modules.md` (generated) for feature modules, `docs/index.md` for specs and
contracts, and `graphify` or source search for file-level lookup. Native Rust runtime owners live in
`runtime/codex/AGENTS.md` (built/tested via `runtime/build.py`; see
`docs/contributing/development.md#native-app-server`).

## Go vs native runtime split

- **Native (`runtime/codex`)**: root/child execution, coordination (`thread_manager`, `agent_host`),
  model templates (`models-manager`), helper/child-reply guidance (`core/src/mekugi_guidance`),
  context usage milestones (`token_budget.rs`), source acquisition & reload (`context_sources*.rs`,
  `compact_token_budget.rs`, `ext/skills`), OS/Git environment facts (`environment_facts.rs`),
  `SESSION_TMP` (`exec_env.rs`), and model-input output deduplication (`output_deduplication.rs`).
- **Go (`apps/mekugi/internal/` & `modules/`)**: server/session lifetime, terminal & browser UIs,
  inference gateway & custom Responses routing, passive edit capture & `mchanges`, authenticated
  frontends (`mcat`/`mrun`/`mread`/`msymbol`/`inspect_file`/`mchanges`), shell guards & private
  secret dialogs, retained session history, inspection, replay, usage/diagnostics.

## Application owners (`apps/mekugi/internal/`)

| Owner | Responsibility |
| --- | --- |
| `composition` | Selected descriptors/bindings; `contracts` holds shared interfaces; `testsupport/moduletest` holds harness |
| `launch` | CLI dispatch, profile imports, invocation/server/connector startup, native attachment & host wiring |
| `frontends` | Authenticated executable catalog/PATH, worker pinning, leases, and recovery delivery |
| `headless` | Noninteractive automation client and JSONL host evidence |
| `inferencegateway` | Responses HTTP/WS admission, pinned custom routing, catalog/auxiliary forwarding, service tier |
| `requestexecution` | Shared inference attempt, delivery, cancellation, failure handling, and instruction retention |
| `executionobservation` | Stock-tool and synchronous pre-tool capture windows, completion, and publication |
| `sessioncontrol` | Sole host app-server connection/request ledger; submission, compaction, approvals, prompts, presets, side threads |
| `sessionpresentation` | Scoped live/replay admission, turn/input/activity reduction, detached publications, metrics projection |
| `sessionapi` | Server-owned runtime admission, versioned Unix/WS control protocol, detached projections, HTTP resources |
| `webui` | Embedded Svelte browser client and assets-only host (`DESIGN.md` owns visual/interaction rules) |
| `connector` | Remote bearer-token/origin admission and WebSocket/HTTP forwarding to the local server |
| `terminalsession` | Terminal attachment, input precedence, geometry, frozen selection, detached views, and frame delivery |
| `sessionreview` | Authorized retained membership, async immutable review hydration, and rewind boundary selection |
| `recordtransactions` | Cross-owner publication, retained-input adoption, and fork initialization |
| `inspectioncli` | Offline `inspect-session`, `inspect-sessions`, and `inspect-storage` command dispatch |

## Key non-obvious boundaries

- `backends/codex/{appserver,binding,rollout,responses,shellhook,shellapproval,messagehistory,nativetrace}`
  owns Codex RPC, protocol decoding, rollout reads, Responses framing, and shell hook protocols.
- `providers/openai` owns native OpenAI credentials, transport, and composer predictions.
- `apps/mekugi/cmd/workspace` owns workspace enumeration, import/boundary checks, `make docs-check`,
  `make check-generated`, and `make integrate` (`docs/contributing/development.md#integrate-reviewed-work`).
- Shared cross-boundary contracts live in `docs/contracts/`: `coordination.md`, `composition.md`,
  `change-evidence.md`, `record-publication.md`, `responses-transport.md`, `provider-routing.md`.
- Controlled model comparisons live in https://github.com/yusing/codex-setup-ab (use only for comparison tasks).
