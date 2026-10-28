// A local CLI protocol peer. The real Agent SDK owns process, hooks and MCP.
import { createInterface } from "node:readline";
import { writeFileSync } from "node:fs";
import { randomUUID } from "node:crypto";

const lines = createInterface({ input: process.stdin });
const sessionID = process.argv.find((arg) => arg.startsWith("--session-id="))?.slice("--session-id=".length)
  ?? "b0f7cf70-0dcc-4b9d-b5ab-e6264b07bc55";
const pending = new Map();
const contextRequests = [];
let contextPending = 0;
let hooks;
let interrupt;
let releaseContext;
let initialized = false;
let inputCount = 0;
const send = (value) => process.stdout.write(`${JSON.stringify(value)}\n`);
const response = (id, value) => send({ type: "control_response", response: { subtype: "success", request_id: id, response: value } });
const request = (value) => new Promise((resolve, reject) => {
  const id = randomUUID(); pending.set(id, { resolve, reject }); send({ type: "control_request", request_id: id, request: value });
});
const hook = (event, input) => request({ subtype: "hook_callback", callback_id: hooks[event][0].hookCallbackIds[0],
  input: { session_id: sessionID, transcript_path: "fixture.jsonl", cwd: process.cwd(), hook_event_name: event, ...input }, tool_use_id: input.tool_use_id });
const toolInput = (id) => ({ tool_name: "mcp__threadcontrol__coordinate", tool_use_id: id, tool_input: { value: "same" }, mcp_server: { name: "threadcontrol", source: "sdk" } });
const mcp = (method, params) => request({ subtype: "mcp_message", server_name: "threadcontrol", message: { jsonrpc: "2.0", id: randomUUID(), method, params } });
const result = (terminalReason, extra = {}) => send({ type: "result", subtype: "success", terminal_reason: terminalReason,
  uuid: randomUUID(), session_id: sessionID, result: "complete", num_turns: 1, usage: {}, modelUsage: {}, total_cost_usd: 0, ...extra });

async function input(message) {
  inputCount++;
  const lifecycle = (state) => send({ type: "command_lifecycle", command_uuid: message.uuid, state, uuid: randomUUID(), session_id: sessionID });
  lifecycle("queued"); lifecycle("started");
  if (!initialized) {
    initialized = true;
    send({ type: "system", subtype: "init", uuid: randomUUID(), session_id: sessionID, model: "fixture-model", tools: [], fixture_args: process.argv.slice(2) });
  }
  if (message.shouldQuery === false) {
    result(undefined, { result: "", num_turns: 0, duration_api_ms: 0, user_message_uuid: message.uuid, user_message_uuids: [message.uuid], fixture_input: message });
    lifecycle("completed");
    return;
  }
  if (!message.isSynthetic) send({ ...message, session_id: sessionID, isReplay: true });
  if (message.message.content === "context") {
    for (let index = 0; index < 32; index++) send({ type: "stream_event", uuid: randomUUID(), session_id: sessionID, parent_tool_use_id: null,
      event: { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "fixture" } } });
    result("completed", { user_message_uuid: message.uuid });
    lifecycle("completed");
    return;
  }
  if (message.message.content === "parallel") {
    await mcp("initialize", { protocolVersion: "2024-11-05", capabilities: {}, clientInfo: { name: "fixture", version: "1" } });
    const before = await Promise.all([hook("PreToolUse", toolInput("tool-a")), hook("PreToolUse", toolInput("tool-b"))]);
    const calls = await Promise.all([1, 0].map(async (index) => {
      const id = index ? "tool-b" : "tool-a";
      const call = await mcp("tools/call", { name: "coordinate", arguments: before[index].hookSpecificOutput.updatedInput });
      const native = call.mcp_response.result;
      const after = await hook("PostToolUse", { ...toolInput(id), tool_response: native });
      return { id, native, after };
    }));
    // Reusing an already-consumed token must never reach the authenticated host.
    const replay = await mcp("tools/call", { name: "coordinate", arguments: before[0].hookSpecificOutput.updatedInput });
    send({ type: "assistant", uuid: randomUUID(), session_id: sessionID, parent_tool_use_id: null,
      message: { role: "assistant", content: [{ type: "text", text: JSON.stringify({ calls, replay }) }] } });
    const batch = await hook("PostToolBatch", { tool_calls: calls.map(({ id, native }) => ({ ...toolInput(id), tool_response: native })) });
    result(batch.continue === false ? "hook_stopped" : "completed");
    return;
  }
  if (message.message.content === "permission") {
    interrupt = () => {
      for (const [id, callback] of pending) {
        send({ type: "control_cancel_request", request_id: id }); callback.reject(new Error("interrupted")); pending.delete(id);
      }
    };
    try {
      const permission = await request({ subtype: "can_use_tool", tool_name: "Bash", input: { command: "fixture" }, tool_use_id: "permission-id" });
      result("completed", { permission });
    } catch { result("aborted_tools"); }
    return;
  }
  result("completed");
}

lines.on("line", (line) => {
  const value = JSON.parse(line);
  if (value.type === "control_response") {
    const callback = pending.get(value.response.request_id); pending.delete(value.response.request_id);
    if (value.response.subtype === "error") callback?.reject(new Error(value.response.error)); else callback?.resolve(value.response.response);
    return;
  }
  if (value.type === "control_request") {
    if (value.request.subtype === "initialize") {
      if (process.env.MEKUGI_FIXTURE_INITIALIZATION_REPORT) writeFileSync(process.env.MEKUGI_FIXTURE_INITIALIZATION_REPORT,
        JSON.stringify({ pid: process.pid, cwd: process.cwd(), args: process.argv.slice(2), captured: process.env.MEKUGI_FIXTURE_CAPTURED }));
      if (process.env.MEKUGI_FIXTURE_INIT_ERROR) { process.stderr.write("fixture initialization failure\n"); process.exit(23); }
      if (process.env.MEKUGI_FIXTURE_INIT_HANG) return;
      hooks = value.request.hooks;
      response(value.request_id, { commands: [], agents: [], models: [{ value: "fixture-model", displayName: "Fixture Model", description: "Native fixture choice", resolvedModel: "fixture-resolved" }], account: { email: "private@example.invalid" }, output_style: "default", available_output_styles: [], hooks_applied: true, claude_code_version: "2.1.312" });
    } else if (value.request.subtype === "interrupt") {
      interrupt?.(); releaseContext?.(); response(value.request_id, { still_queued: [], ...(value.request.cancel_queued ? { cancelled: [] } : {}) });
    } else if (value.request.subtype === "get_usage") {
      if (process.env.MEKUGI_FIXTURE_USAGE_REPORT) writeFileSync(process.env.MEKUGI_FIXTURE_USAGE_REPORT,
        JSON.stringify({ request: value.request, input_count: inputCount }));
      if (process.env.MEKUGI_FIXTURE_USAGE_HANG) return;
      if (process.env.MEKUGI_FIXTURE_USAGE_ERROR) {
        send({ type: "control_response", response: { subtype: "error", request_id: value.request_id, error: "private fixture usage failure" } });
      } else response(value.request_id, { session: { total_cost_usd: 0.125, total_api_duration_ms: 3, total_duration_ms: 4,
        total_lines_added: 0, total_lines_removed: 0, model_usage: { "fixture-model": { inputTokens: 100, outputTokens: 10,
          cacheReadInputTokens: 20, cacheCreationInputTokens: 5, webSearchRequests: 0, costUSD: 0.125, contextWindow: 200000, maxOutputTokens: 8192 } } },
        subscription_type: null, rate_limits_available: false, rate_limits: null, behaviors: null });
    } else if (value.request.subtype === "get_context_usage") {
      contextPending++;
      contextRequests.push(value.request);
      if (process.env.MEKUGI_FIXTURE_CONTEXT_REPORT) writeFileSync(process.env.MEKUGI_FIXTURE_CONTEXT_REPORT, JSON.stringify(contextRequests));
      send({ type: "system", subtype: "status", status: null, uuid: randomUUID(), session_id: sessionID,
        fixture_context_reads: contextRequests.length, fixture_context_pending: contextPending });
      releaseContext = () => {
        contextPending--;
        releaseContext = undefined;
        if (process.env.MEKUGI_FIXTURE_CONTEXT_ERROR) {
          send({ type: "control_response", response: { subtype: "error", request_id: value.request_id, error: "private fixture context failure" } });
        } else response(value.request_id, { totalTokens: 1234, rawMaxTokens: 200000, maxTokens: 180000, percentage: 1,
          categories: [], gridRows: [], model: "fixture-model", memoryFiles: [], mcpTools: [], agents: [], isAutoCompactEnabled: true, apiUsage: null });
      };
      if (!process.env.MEKUGI_FIXTURE_CONTEXT_HOLD) releaseContext();
    } else response(value.request_id, {});
    return;
  }
  if (value.type === "user") void input(value).catch((error) => { process.stderr.write(`${error.stack}\n`); process.exitCode = 1; });
});
