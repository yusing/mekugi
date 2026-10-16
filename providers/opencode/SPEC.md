# OpenCode configuration, metadata and inference transport

## Ownership

Own service credentials, catalog acquisition, immutable request snapshots, model
capabilities, price metadata, and authenticated inference transport. The application
selects the cache path and loads invocation configuration. It also owns Codex
catalog projection, protocol policy, and usage accounting described by the
[OpenCode route](../../doc/spec/opencode.md).

## Catalog and credentials

OpenCode Go and compatible Zen models are available to main agents and native
subagents under `opencode-go:<model>` and `opencode-zen:<model>`. Available IDs come
from each service's public `/models` endpoint. Model descriptions, API format bindings,
reasoning capabilities and USD-per-million-token prices come from
`https://models.opencode.ai/api.json`, the catalog used by OpenCode itself.
Descriptions are passed through from upstream without Mekugi-written additions;
missing descriptions remain empty. Per-model SDK bindings override the provider binding: OpenAI-compatible Chat,
Anthropic Messages and OpenAI Responses are supported. Other formats are not
advertised or silently approximated. Only text and supported images are advertised.

Online data replaces the available model set and overrides bootstrap values.
New IDs, changes among supported API formats, and price changes do not require a
Mekugi release. The immutable in-memory snapshot gives fast lookup; a versioned
cache at `mekugi/opencode-models-v2.json` beneath the platform user cache directory
survives restart. Startup and inference selection refresh data older than one hour.
Fresh cache hits make no network requests. Refresh has an eight-second deadline,
cancelable coalescing, and a one-minute failure backoff. Invalid or failed refreshes
retain the last usable snapshot; disk writes use temporary files and atomic rename.
The built-in snapshot is only an offline bootstrap when no valid cache is available.
It embeds the checked-in provider metadata and `/models` responses in
`snapshot`; `go generate ./providers/opencode` refreshes those
source records without a hand-maintained model or price table. The generator keeps
provider-owned metadata and model records, but omits the `/models` response-time
`created` value so repeated captures of an unchanged catalog are byte-reproducible.

Live legacy aliases missing upstream metadata retain their last known metadata;
unknown API formats are never guessed. Missing context limits and prices remain unknown.

Requests pin format and prices so concurrent refreshes cannot alter in-flight
translation or accounting. Public catalog requests carry no credentials and cannot
redirect; metadata cannot change credential destinations. The Codex picker is a
startup snapshot: restart the wrapper to expose newly discovered models there.
Runtime routing and subsequent request prices refresh without restarting.
OpenCode's prose endpoint documentation currently disagrees with its machine-readable
catalog for some Qwen models; routing follows the online catalog, not a pinned
exception. Authenticated acceptance of those conflicting bindings is not established.

Configuration is read once per invocation from `mekugi/config.toml` beneath the
platform user configuration directory. `[providers.opencode_go]` and
`[providers.opencode_zen]` each accept `api_key`. Missing configuration is allowed;
invalid or unknown settings fail startup without quoting secrets. `OPENCODE_API_KEY`
overrides both file keys, then `OPENCODE_GO_API_KEY` and `OPENCODE_ZEN_API_KEY` override
their respective service. Keys are trimmed; an explicitly empty environment value
disables that service. Nonempty keys enable catalog registration and routing in
standalone `mekugi` only, not `mekugi codex` or `mekugi grok`. No new command-line flag, credential-store write, or Codex config
rewrite is introduced.

## Inference

Go requests use `https://opencode.ai/zen/go/v1` with `/chat/completions`, `/messages`
or `/responses` according to the pinned model's format. Zen uses the same formats
beneath `https://opencode.ai/zen/v1`. Chat and Responses use the selected
service's Bearer key; Messages uses `x-api-key` and `anthropic-version: 2023-06-01`.
A provider-scoped hash of the stable thread ID is sent as `x-opencode-session`
for routing/cache affinity. It survives restart without sharing identities
between services or threads; absent thread identity does not borrow another session.
Other Codex account/session headers remain excluded. Redirects are not followed,
and there is no authentication retry.

The transport sends already-encoded bytes. Final rejected responses and accepted
body lifetime use the shared [HTTP exchange](../../libraries/httptransport/SPEC.md).
Codex response framing and price accounting remain with their respective owners.

## Lifetime and failures

A catalog belongs to one invocation. A request pins its service snapshot before
translation and retains it through accounting. Returned metadata is read-only.
Refreshing a catalog publishes a new snapshot without altering pinned requests.
Credential destinations are fixed service properties, never catalog metadata.

## Acceptance

Offline tests cover bootstrap validity, online replacement and legacy aliases,
cache restart and failure backoff, canceled refresh waiters, immutable snapshots,
metadata-only entries, unknown formats, and context-tier price selection.
Application tests cover configuration precedence, actual request translation,
Codex catalog projection, and in-flight accounting after model removal.
