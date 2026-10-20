# MCP transport

## REQ-MCP-001 — Invocation-local host dispatch

MCP connects host-dispatched calls to explicitly registered Mekugi handlers.
The internal shell-instrumentation hook uses this transport without exposing
an agent-facing tool. Executable frontends remain separate PATH commands.

The invocation owns its private endpoint, server connections, and stdio bridge.
Codex owns the bridge process, tool invocation, permissions, and cancellation.
Registration is invocation-local and does not modify user configuration. Shutdown
closes owned connections and removes only the invocation's endpoint resources.
Concurrent invocations cannot replace or remove each other's endpoints.

Transport and feature policy have separate owners. The bridge relays the protocol;
the registered handler validates its own input and produces its own result.
MCP does not schedule work, persist task state, or control context reset.

The guard's denial semantics remain part of the
[execution contract](execution.md#req-execution-003--guard-remote-vcs-writes).
A protocol error is not proof that Codex blocked an operation. Instrumentation
failure must retain the native denial path, and effective hook registration must
remain verified before relying on the guard.

### Acceptance

1. A session starts the bridge and dispatches the registered hook without any
   feature-state store or context-reset component.
2. The internal hook stays outside the model-visible tool catalog.
3. Successful hook dispatch preserves the composed instrumentation; malformed
   input and instrumentation failure preserve native denial.
4. Cancellation and shutdown release owned connections and endpoint resources
   without affecting another invocation.
5. Launcher registration uses `mcp-bridge` and leaves persistent user configuration
   unchanged.
