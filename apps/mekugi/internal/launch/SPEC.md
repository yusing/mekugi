# Invocation launch

## Requirements

- Codex remains the root execution authority with one interactive UI and one stock JavaScript
  execution interface; direct execution mode is unsupported.
- Preserve stock editing, execution, batch/parallel control, live diffs, and captured-change review
  for shell writes without mutating user Codex configuration files.
- Interactive launches auto-start the local server when absent; server-owned threads outlive client
  and connector closure; server shutdown ends active work while saved Mekugi sessions remain
  resumable (product-local resume only).
- Support native OpenAI (`mekugi codex`) and explicit custom Responses profiles (`mekugi -p NAME`),
  plus explicit headless automation (`mekugi codex headless --yolo`).

## Current approach (replaceable)

### Invocation selection and settings

- `mekugi` dispatches authenticated frontend workers and offline inspection before live startup.
- `mekugi/config.toml` under `$XDG_CONFIG_HOME` provides optional `[features]`
  (`child_reply_summary`, `debug`) and `[service_tiers]`. Precedence per feature: explicit CLI flag
  (placed before `codex`/`--`) > config file > built-in default. Config errors never print secrets.
- Bare `mekugi` requires a custom Responses profile (`-p NAME`); `mekugi codex` selects native
  OpenAI. Unsupported flags, redirected ordinary launches, and stock CLI subcommands fail rather
  than falling back to stock `codex`.
- Reasoning shortcuts (`--low`, `--medium`, `--high`, `--xhigh`, `--max`, `--ultra`) map to
  invocation-local reasoning-effort overrides.

### Codex profile overrides

- `-p NAME` / `--profile NAME` reads `NAME.config.toml` from `$CODEX_HOME` once and applies its
  typed TOML values as initial invocation-local `-c` overrides (CLI flags > profile > base/project
  config; managed host policy remains authoritative). Configuration files are never rewritten.
- The family freezes the admitted profile and caller environment (including `PATH` and auth-command
  dependencies); children, Side, One-off, and forked/rewound roots inherit them. Fresh resume
  reloads the profile; live reattachment keeps running settings and rejects a mismatched profile.
  See [provider routing](../../../../docs/contracts/provider-routing.md).

### Host integration and lifecycle

- Launches resolve `mekugi-app-server` and bundled `mekugi-models.json` from `PATH` then beside
  `mekugi` (no fallback to stock `codex`). Native runtime enforces JavaScript-only execution and
  disables `features.sleep_tool`, Codex goals, and collaboration-mode injection.
- Native rollout tracing runs in a private session-scoped temp dir cleaned on exit.
- Native shell guarding (`features.shell_guard`, enabled by default in interactive runs and forced
  off in headless runs via `-c features.shell_guard=false`) runs inside `mekugi-app-server` after
  user `PreToolUse` hooks and prompts the Go host UI over App-Server Protocol v2
  (`shellGuard/requestApproval` and `shellGuard/requestSecret`). `--yolo` disables Codex
  approvals/sandbox without disabling native shell guards or bypassing hook trust
  (`-c bypass_hook_trust=true` controls hook trust).
- Detached servers retain bounded private stderr/exit evidence via a supervisor.
- On attached exit, idle threads release cleanly; busy `/quit` (including a shell still running
  after its turn) offers Continue in background vs Stop; `/detach` leaves execution running. Exit
  restores the terminal and prints unsent notices, cumulative Main token usage, and
  `resume`/`replay-session` commands.
