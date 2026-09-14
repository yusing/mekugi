# Router-owned third-party provider bridge

## CTR-SUBAGENTS-001 — Third-party native subagent routing

The launcher owns the invocation-local model catalog snapshot. The router owns provider
selection, plaintext collaboration projection, and third-party protocol and authentication
adaptation. Codex remains the only owner of agent creation, message delivery, tool
execution, sandboxing, approvals, and lifecycle state.

The bridge uses request-visible native history and tool identities rather than another
agent registry or transcript store. It restores only its private namespace and never
executes tools, forwards OpenAI credentials, decrypts opaque history, or substitutes a
parallel CLI agent runtime. Streaming adaptation must complete and validate every
executable call before exposure.

Third-party credentials, refresh, redirects, and provider clients remain isolated from
OpenAI authentication. Protocol classification is shared with capture, while transport
metrics remain capture-owned. Neither shared protocol code nor the provider adapter owns
conversation or native-agent lifecycle.
