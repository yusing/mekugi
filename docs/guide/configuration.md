# Configuration and metrics

Settings file, launch options, metrics and integrations.

## Mekugi settings

Create `mekugi/config.toml` under `$XDG_CONFIG_HOME` (default `~/.config` on Linux,
`~/Library/Application Support` on macOS). Optional sections are read at startup, never
rewritten:

```toml
[features]
shell_guard = true
deduplicate_output = true
child_reply_summary = true
debug = false

[service_tiers]
"gpt-6-astra" = "fast"
```

Feature keys set defaults for the matching hyphenated CLI flags in every launch,
including `mekugi codex`, independently of provider credentials. Each feature uses
an explicit CLI value first, then its file value, then the built-in default shown
above. Missing files, sections, and keys keep built-in defaults. Unknown keys and
non-boolean feature values fail startup without printing configuration contents.

For example, set `deduplicate_output = false` to keep repeated model input by
default, then run `mekugi --deduplicate-output codex` to enable it for one launch.
Bare boolean flags mean true; use `--debug=false` or `--shell-guard=false` to
explicitly disable a feature. Repeated flags use the last value. Flags must precede
`codex` and `--`; arguments after the delimiter belong to Codex. Overrides apply
to that invocation and its child threads, never write the file or saved session
settings, and are preserved in the printed resume command. A fresh launch or
resume reads the current file again.

Child replies normally begin with a short authored Summary, followed by supporting
sections, in one response. Main shows the complete summary; **Open reply** shows supporting
details without repeating it. Full-copy actions keep the original response.
Plain or malformed replies keep the excerpt fallback. Set
`child_reply_summary = false` or launch `mekugi --child-reply-summary=false codex`
to use excerpts for all replies and omit the child-authoring rule. This adds no
separate summarization request and leaves parent input and child transcripts intact.

Exact model IDs select tiers: `auto`, `default`, `fast` (sent as `priority`),
`priority`, or `flex`, subject to provider support. `/tier` choices override defaults
for that thread/model during the invocation. The composer shows the effective tier;
`/session` shows the provider-returned tier.

## Codex profile imports

Use `mekugi codex -p work` or `--profile work` to import
`work.config.toml` from `$CODEX_HOME` (default `$HOME/.codex`). The same import
works with standalone launches, headless and wrapped exec/resume/fork. Names
contain only ASCII letters, digits, underscores and hyphens. Missing, malformed
or non-regular files fail before inference without printing their contents.

```toml
model = "your-upstream-model"
model_provider = "custom"
model_reasoning_effort = "high"
model_context_window = 200000
model_auto_compact_token_limit = 180000
[model_providers.custom]
name = "Custom Responses"
base_url = "https://provider.example/api/v1"
wire_api = "responses"
env_key = "CUSTOM_API_KEY"
requires_openai_auth = false
supports_websockets = true
```

Set the provider's credentials in your environment, then run:

```sh
mekugi codex -p work
mekugi headless --yolo -p work < prompt.txt
mekugi codex -p work exec "Explain this repository"
```

Imported values become initial invocation-local `-c` overrides. Explicit CLI
settings win; managed host policy remains authoritative. Relative paths use the
invocation workspace. This is not native profile layering or profile writeback.
Source configuration, credentials and catalogs are never rewritten. Existing
configuration writes still target Codex's ordinary user layer; imported presets
are protected from ineffective lower-layer writes.

A selected custom provider keeps its original provider/model identity and native
Responses HTTP/SSE or configured WebSocket transport. Supply its provider ID and
base URL in the import or explicit overrides. Bare `mekugi` requires this explicit
selection; `mekugi codex` retains native OpenAI when no profile selects another route. Other
provider fields retain normal host merging. Codex owns environment, literal,
command-backed and requested managed-login authentication, caching and refresh.
Unknown model IDs need no built-in discovery or OpenAI login. Custom-route prices
remain unknown. Static or remote catalogs retain their metadata apart from the
required stock JavaScript execution mode.

The family freezes the admitted profile and caller environment. Children and
side/forked threads inherit them; later file edits do not reconfigure running
sessions. Fresh stopped-session resume reloads the selected file; imported fields
beat saved settings, and explicit CLI fields beat the import. Live attachment
keeps the running settings and reports this for a matching selector; a different
profile identity fails. Continuation hints retain the selector, not expanded
profile contents. Literal secrets in imported argv have normal process-inspection
exposure; prefer environment or command-backed authentication.

Custom routes require the Responses wire protocol and endpoint substitution;
AWS signing, gateway OAuth, incompatible managed policy and built-in OpenAI remote
catalog overrides are unsupported. Stock execution integration remains required.
`mekugi codex` without a profile retains stock tools, including web search, on
the normal Codex route.
See the
[routing contract](../contracts/provider-routing.md#profile-defined-responses-routing).

## Options

| Flag | Default | Purpose |
| --- | --- | --- |
| `--ansi-faint` | `auto` | Detect mosh; `on` uses ANSI faint, `off` fixed muted colors |
| `--shell-guard` | `true` | Guard UI remote writes and sudo commands; securely answer GPG passphrase requests, even with `--yolo` |
| `--deduplicate-output` | `true` | Reference duplicates in model input; full results/evidence stay intact |
| `--child-reply-summary` | `true` | Show child-authored summaries in Main, with the original behind Open reply |
| `--timeout` | `10m` | Wait for response start |
| `--stream-idle-timeout` | `4m` | Limit provider-message or HTTP-byte gaps |
| `--capture-output PATH` | Disabled | Append sanitized JSONL metrics |
| `--debug` | `false` | Record private [diagnostic evidence](../contributing/development.md#diagnostics) |

The boolean defaults above apply when no matching [feature setting](#mekugi-settings) exists.
Use `--ansi-faint=off` if a multiplexer hides mosh.

With output deduplication enabled, transcript rows mark replaced output as
`repeated` or `N lines repeated`, and the `/session` Features tab shows occurrences and
estimated saved tokens. See the [projection contract](../../modules/outputdeduplication/SPEC.md).

## Metrics

`/session` shows launch-wide usage/retries/coverage/throughput/cache/transport.
Its Features tab compares the displayed thread's use of model-facing features with the
sum over all of its descendant agents. Each family lists its total beside the uses and
failed attempts within it: compactions, spawns and their preloads, delegate final-answer
edit ranges, `send_message` and `btw`, `wait_agent` all mode and output deduplication.
Left/Right or Tab switches tabs; Up/Down/PgUp/PgDn scrolls, `[`/`]` selects exchanges,
`r` refreshes, Esc closes. Launch totals do not restore; the roster retains thread
usage. Throughput divides authoritative output tokens (including reasoning) by measured
request seconds, including latency/retries, excluding tools/idle time. Missing
measurements are unavailable; costs are API estimates, not subscription charges. Local
estimates are not billing figures. See [metrics](../../modules/transportdiagnostics/SPEC.md).

```sh
curl -sS "${MEKUGI_BASE_URL%/v1}/api/metrics"
mekugi --capture-output capture.jsonl codex
```

Captures exclude prompts, patches, and credentials.

## Troubleshooting and integrations

- **Instructions:** wrapped sessions disable `/goal`; `<!-- mekugi:omit -->` through
  `<!-- /mekugi:omit -->` omits marked blocks without changing files/unmarked policy.
  With `skills-mgr`, managed selections attach instructions and the stock catalog is
  disabled for the session. See [guidance](../../modules/agentguidance/SPEC.md).
- **Recovery:** see [compaction](sessions.md#compaction-and-recovery) and [storage](sessions.md#replay-storage).
- **Frontend commands:** [compiled helpers](../../apps/mekugi/internal/frontends/SPEC.md) run through stock
  execution. JavaScript plugin loading has been removed; existing plugin files
  are left untouched.
- **Environment:** router/executor must share paths/runtime directory; `MEKUGI_RUNTIME_DIR`
  overrides the temporary-directory default. Startup errors print before Codex;
  session failures show commentary/stderr. Finish sessions before replacing older
  installations; preserve unrelated settings/authentication.
