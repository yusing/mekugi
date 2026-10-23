# Invocation-local MCP transport

## REQ-MCP-001 — Invocation-local transport

Feature bindings supply an SDK server with their registrations;
the transport owns connections, not handler policy or Codex configuration.
Launch starts the declared endpoints and supplies invocation-local Codex configuration.
Shell instrumentation uses the synchronous command hook
specified in [native shell hooks](../../backends/codex/SPEC.md#native-shell-hooks).

An explicit transport invocation owns its private endpoint and connections.
Once connected, the stdio bridge owns its supplied stdin/stdout files, relays
bytes in both directions, and half-closes the connection when stdin ends. Cancellation closes
its connection and files; registered handlers own
input validation and feature policy. Parent cancellation and explicit shutdown
close owned connections and remove only the invocation's resources. Repeated or concurrent stop calls
wait for the same completed cleanup. Concurrent invocations remain
independent. Transport does not schedule work, persist task state, or control
context reset. Launch registers the internal `mcp-bridge` command for each declared
server. Frontend invocations cannot select this launcher
command through their operands.

### Acceptance

1. Explicitly registered test handlers communicate through the private endpoint.
2. Cancellation and shutdown release owned connections and endpoint resources.
3. Bridge bytes and stdin EOF are preserved. Relative socket paths are rejected.
   Cancellation succeeds even when output is not drained.
4. Launch registers only declared contributions and leaves user MCP configuration unchanged.
