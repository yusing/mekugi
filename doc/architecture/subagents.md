# Router-owned third-party provider bridge

## CTR-SUBAGENTS-001 — Third-party native subagent routing

`cmd/mekugi` owns the private session catalog lifecycle: loading the selected catalog through
Codex, pinning it with invocation-only configuration, and cleaning it up after the child exits.
`internal/router/grok_catalog.go` owns deriving Grok metadata from the native v2 template.
The HTTP models endpoint forwards native catalog responses without augmentation.
`internal/router` owns provider selection, plaintext collaboration projection and Grok
protocol/authentication adaptation. Codex remains the only owner of agent
creation, delivery, tool execution, sandbox/approval enforcement and lifecycle state.

The collaboration bridge runs after ordinary Mekugi request preparation and before CTP serialization.
Response restoration reverses that order: CTP, collaboration identity restoration, then ordinary
Mekugi handling and subagent commentary. The bridge uses request-local tool identities and the
existing Codex history, not a second agent registry or transcript store. Only the private bridge
namespace is restored; arbitrary tool arguments or user text are not namespace-rewritten.

The provider client selects Grok only for its explicit qualified model and authenticated native-child
boundary. Grok's adapter translates supported Responses content into Chat Completions, and returns
ordinary Responses JSON/events to the existing transformation pipeline. It never executes model
tools, forwards OpenAI credentials, decrypts opaque history, or substitutes a CLI agent runtime.
Text and heartbeat progress remain auxiliary to complete, validated executable tool calls.
Each streamed call owns one append-only argument buffer until terminal validation. The adapter
preserves content-part boundaries and accepts no choice mutation after the first terminal reason.

Grok authentication has its own HTTP client, fixed credential destinations and redirect policy.
Refresh coordinates with the CLI using the same advisory lock and atomic credential replacement;
it preserves unowned JSON fields and never takes over interactive login. OAuth discovery must retain
the trusted Grok issuer and token host. The router reads current credentials for each request so
independent logins and refreshes take effect without a router restart.

The `capturer` package remains the sole metrics owner. Its provider transport observer also accepts
Chat Completions, counts real wire bytes, reconstructs terminal assistant output and records actual
function-call shapes. The router maps provider usage into its existing terminal observation seam;
neither the adapter nor the bridge maintains parallel metrics or durable conversation content.

Chat finish-reason recognition is owned by `internal/chat` and shared by the Grok adapter
and capture. Recognized status is evidence, not executable-call validation: the adapter
still seals choice data, requires stream termination, and validates every parallel call
before emitting executable items. Capture observes actual Chat traffic rather than the
synthesized Responses stream. Responses event names and families come from
`internal/responses`; neither shared protocol package owns agent or transport lifecycle.
