# Reserved MCP transport

## REQ-MCP-001 — Invocation-local transport

MCP transport remains available for future explicitly registered handlers, with
its component tests. Normal application startup creates no MCP endpoint and
registers no MCP tool. Shell instrumentation uses the synchronous command hook
specified in [execution](execution.md#req-execution-003--guard-remote-vcs-writes).

An explicit transport invocation owns its private endpoint and connections.
The stdio bridge relays protocol bytes and cancellation; registered handlers own
input validation and feature policy. Endpoint shutdown closes owned connections
and removes only its invocation's resources. Concurrent invocations remain
independent. Transport does not schedule work, persist task state, or control
context reset. The internal `mcp-bridge` command remains available; normal launch
does not register it with Codex.

### Acceptance

1. Explicitly registered test handlers communicate through the private endpoint.
2. Cancellation and shutdown release owned connections and endpoint resources.
3. Bridge cancellation succeeds even when its output is not drained.
4. Normal launch neither starts an endpoint nor changes user MCP configuration.
