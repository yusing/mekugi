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
[providers.opencode_go]
api_key = "your-go-key"
[providers.opencode_zen]
api_key = "your-zen-key"
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

Child replies normally include a short authored Summary and supporting Details in
one response. Main shows the complete summary; **Open reply** shows supporting
details without repeating it. Full-copy actions keep the original response.
Plain or malformed replies keep the excerpt fallback. Set
`child_reply_summary = false` or launch `mekugi --child-reply-summary=false codex`
to use excerpts for all replies and omit the child-authoring rule. This adds no
separate summarization request and leaves parent input and child transcripts intact.

Exact model IDs select tiers: `auto`, `default`, `fast` (sent as `priority`),
`priority`, or `flex`, subject to provider support. `/tier` choices override defaults
for that thread/model during the invocation. The composer shows the effective tier;
`/session` shows the provider-returned tier.

## Options

| Flag | Default | Purpose |
| --- | --- | --- |
| `--ansi-faint` | `auto` | Detect mosh; `on` uses ANSI faint, `off` fixed muted colors |
| `--shell-guard` | `true` | Guard UI remote writes and sudo commands, even with `--yolo` |
| `--deduplicate-output` | `true` | Reference duplicates in model input; full results/evidence stay intact |
| `--child-reply-summary` | `true` | Show child-authored summaries in Main, with the original behind Open reply |
| `--grok-auth-file` | `~/.grok/auth.json` | OAuth store |
| `--timeout` | `10m` | Wait for response start |
| `--stream-idle-timeout` | `4m` | Limit provider-message or HTTP-byte gaps |
| `--capture-output PATH` | Disabled | Append sanitized JSONL metrics |
| `--debug` | `false` | Record private [diagnostic evidence](../contributing/development.md#diagnostics) |

The boolean defaults above apply when no matching [feature setting](#mekugi-settings) exists.
Use `--ansi-faint=off` if a multiplexer hides mosh.

With output deduplication enabled, transcript rows mark replaced output as
`repeated` or `N lines repeated`, and `/session` shows occurrences and estimated
saved tokens. See the [projection contract](../../modules/outputdeduplication/SPEC.md).

## Metrics

`/session` shows launch-wide usage/retries/coverage/throughput/cache/transport.
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
