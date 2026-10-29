import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import { mkdir, mkdtemp, readFile, readdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { once } from "node:events";
import { createMockProvider } from "./testdata/mock-provider.mjs";

const root = fileURLToPath(new URL(".", import.meta.url));

function start(t, onRequest, env = {}, onCancel = () => {}) {
  const child = spawn(process.execPath, [fileURLToPath(new URL("dist/worker.mjs", import.meta.url))], { cwd: root, env: { ...process.env, ...env }, stdio: ["pipe", "pipe", "pipe"], signal: AbortSignal.timeout(30_000) });
  const pending = new Map();
  const events = [];
  let stderr = "";
  let changed;
  let closed = false;
  const exit = new Promise((resolve) => child.on("exit", resolve));
  child.on("error", (error) => { for (const callback of pending.values()) callback.reject(error); });
  child.on("exit", () => { closed = true; changed?.(); });
  t.after(async () => { child.stdin.end(); await exit; });
  child.stderr.on("data", (data) => { stderr += data; });
  const send = (value) => child.stdin.write(`${JSON.stringify(value)}\n`);
  createInterface({ input: child.stdout }).on("line", (line) => {
    const value = JSON.parse(line);
    if (value.kind === "response") {
      const callback = pending.get(value.id); pending.delete(value.id);
      if (value.error) callback?.reject(new Error(value.error)); else callback?.resolve(value.result);
    } else if (value.kind === "event") {
      events.push(value.event); changed?.();
      if (env.ANTHROPIC_BASE_URL && value.event.type === "result") {
        const { subtype, terminal_reason, user_message_uuid, user_message_uuids, num_turns, is_error, errors } = value.event;
        console.log(JSON.stringify({ nativeResult: { subtype, terminal_reason, user_message_uuid, user_message_uuids, num_turns, is_error, errors } }));
      }
      if (env.ANTHROPIC_BASE_URL && value.event.type === "command_lifecycle") console.log(JSON.stringify({ nativeLifecycle: { input: value.event.command_uuid, state: value.event.state } }));
    }
    else if (value.kind === "request") {
      void Promise.resolve(onRequest(value.method, value.params)).then((result) => send({ kind: "response", id: value.id, result }),
        (error) => send({ kind: "response", id: value.id, error: error.message }));
    } else if (value.kind === "fatal") {
      for (const callback of pending.values()) callback.reject(new Error(`${value.error}\n${stderr}`));
    } else if (value.kind === "cancel") onCancel(value.id);
  });
  const request = (method, params) => new Promise((resolve, reject) => {
    const id = randomUUID(); pending.set(id, { resolve, reject }); send({ kind: "request", id, method, params });
  });
  const waitFor = async (predicate) => {
    while (!events.some(predicate)) {
      if (closed) throw new Error(`SDK fixture exited before expected event: ${stderr.slice(-4000)}`);
      await new Promise((resolve) => { changed = resolve; });
    }
    return events.find(predicate);
  };
  const initialize = (options = {}) => request("initialize", { workspace: root, claudePath: fileURLToPath(new URL("testdata/mock-claude.mjs", import.meta.url)), options,
    tools: [{ name: "coordinate", description: "fixture", inputSchema: { type: "object", properties: { value: { type: "string" } }, required: ["value"], additionalProperties: false } }] });
  const input = (content, extra = {}) => request("input", { type: "user", uuid: randomUUID(), parent_tool_use_id: null, message: { role: "user", content }, ...extra });
  return { request, initialize, input, waitFor, events, exit, closeInput: () => child.stdin.end() };
}

test("real SDK streaming query authenticates parallel identical MCP calls, retains receipts and stop reasons", { timeout: 15_000 }, async (t) => {
  const calls = [];
  const worker = start(t, async (method, params) => {
    if (method === "tool") { calls.push(params); return { content: [{ type: "text", text: `native ${params.toolUseID}` }] }; }
    assert.equal(method, "hook");
    if (params.hook_event_name === "PostToolUse") return { hookSpecificOutput: { hookEventName: "PostToolUse", additionalContext: `receipt ${params.tool_use_id}` } };
    if (params.hook_event_name === "PostToolBatch") return { continue: false, stopReason: "waiting" };
    return {};
  });
  const forkId = randomUUID();
  const initialization = await worker.initialize({ permissionMode: "default", resume: "b0f7cf70-0dcc-4b9d-b5ab-e6264b07bc55", forkSession: true, sessionId: forkId });
  assert.equal(initialization.hooks_applied, true);
  const retained = await worker.input("retain", { shouldQuery: false, isSynthetic: true, origin: { kind: "peer", from: "main" } });
  assert.equal(retained.status, "queued");
  const accepted = await worker.waitFor((event) => event.type === "command_lifecycle" && event.command_uuid === retained.uuid && event.state === "completed");
  assert.notEqual(accepted.uuid, retained.uuid);
  const init = await worker.waitFor((event) => event.type === "system");
  assert.ok(init.fixture_args.includes("--replay-user-messages"));
  assert.ok(init.fixture_args.includes("--setting-sources=user,project,local"));
  assert.ok(init.fixture_args.includes("--resume=b0f7cf70-0dcc-4b9d-b5ab-e6264b07bc55"));
  assert.ok(init.fixture_args.includes("--fork-session"));
  assert.ok(init.fixture_args.includes(`--session-id=${forkId}`));
  assert.equal(init.session_id, forkId);
  const retention = worker.events.find((event) => event.type === "result");
  assert.equal(retention.num_turns, 0);
  assert.equal(retention.user_message_uuid, retained.uuid);
  assert.equal(retention.fixture_input.shouldQuery, false);
  assert.deepEqual(retention.fixture_input.origin, { kind: "peer", from: "main" });
  const querying = await worker.input("parallel");
  const result = await worker.waitFor((event) => event.type === "result" && event.terminal_reason === "hook_stopped");
  assert.equal(result.terminal_reason, "hook_stopped");
  assert.ok(worker.events.some((event) => event.type === "user" && event.uuid === querying.uuid && event.isReplay));
  assert.deepEqual(calls.map((call) => call.toolUseID).sort(), ["tool-a", "tool-b"]);
  for (const call of calls) assert.deepEqual(call.arguments, { value: "same" });
  const assistant = worker.events.find((event) => event.type === "assistant");
  const details = JSON.parse(assistant.message.content[0].text);
  for (const call of details.calls) {
    assert.equal(call.native.content[0].text, `native ${call.id}`);
    assert.equal(call.after.hookSpecificOutput.additionalContext, `receipt ${call.id}`);
  }
  assert.equal(details.replay.mcp_response.result.isError, true);
  await worker.request("shutdown");
  assert.equal(await worker.exit, 0);
});

for (const outcome of ["native", "unavailable", "timeout"]) test(`real SDK reports ${outcome} fork usage before input`, { timeout: 15_000 }, async (t) => {
  const unavailable = outcome !== "native";
  const directory = await mkdtemp(join(tmpdir(), "mekugi-claude-usage-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const report = join(directory, "request.json");
  const worker = start(t, () => assert.fail("Baseline acquisition must not execute callbacks"), {
    MEKUGI_FIXTURE_USAGE_ERROR: outcome === "unavailable" ? "1" : "", MEKUGI_FIXTURE_USAGE_HANG: outcome === "timeout" ? "1" : "", MEKUGI_FIXTURE_USAGE_REPORT: report,
  });
  const sessionId = randomUUID();
  await worker.initialize({ resume: randomUUID(), forkSession: true, sessionId });
  assert.deepEqual(JSON.parse(await readFile(report, "utf8")), { request: { subtype: "get_usage", skip_behaviors: true }, input_count: 0 });
  assert.deepEqual(worker.events, [{ type: "mekugi_usage_baseline", session_id: sessionId, total_cost_usd: unavailable ? null : 0.125,
    modelUsage: unavailable ? null : { "fixture-model": { inputTokens: 100, outputTokens: 10, cacheReadInputTokens: 20,
      cacheCreationInputTokens: 5, webSearchRequests: 0, costUSD: 0.125, contextWindow: 200000, maxOutputTokens: 8192 } } }]);
  await worker.request("shutdown");
  assert.equal(await worker.exit, 0);
});

for (const outcome of ["native", "unavailable", "timeout"]) test(`real SDK forwards native events while context is ${outcome}`, { timeout: 15_000 }, async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mekugi-claude-context-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const report = join(directory, "requests.json");
  const worker = start(t, () => ({}), { MEKUGI_FIXTURE_CONTEXT_ERROR: outcome === "unavailable" ? "1" : "", MEKUGI_FIXTURE_CONTEXT_HOLD: "1", MEKUGI_FIXTURE_CONTEXT_REPORT: report });
  await worker.initialize();
  const retained = await worker.input("retain", { shouldQuery: false });
  await worker.waitFor((event) => event.type === "command_lifecycle" && event.command_uuid === retained.uuid && event.state === "completed");
  await assert.rejects(readFile(report), { code: "ENOENT" });
  assert.equal(worker.events.some((event) => event.type === "mekugi_context_usage"), false);
  const results = [];
  for (let turn = 0; turn < 3; turn++) {
    const input = await worker.input("context");
    await worker.waitFor((event) => event.type === "command_lifecycle" && event.command_uuid === input.uuid && event.state === "started");
    const result = await worker.waitFor((event) => event.type === "result" && event.user_message_uuid === input.uuid);
    await worker.waitFor((event) => event.type === "command_lifecycle" && event.command_uuid === input.uuid && event.state === "completed");
    assert.equal(result.result, "complete");
    assert.equal(result.terminal_reason, "completed");
    assert.equal(result.num_turns, 1);
    results.push(result);
  }
  await worker.waitFor((event) => event.fixture_context_reads === 1);
  assert.deepEqual(JSON.parse(await readFile(report, "utf8")), [{ subtype: "get_context_usage", detail: "summary" }]);
  assert.equal(worker.events.filter((event) => event.type === "stream_event").length, 96);
  assert.equal(worker.events.filter((event) => event.type === "result").length, 4);
  assert.equal(worker.events.filter((event) => event.type === "mekugi_context_usage").length, 0);
  if (outcome === "timeout") await worker.waitFor((event) => event.type === "mekugi_context_usage" && event.result_uuid === results[0].uuid);
  await worker.request("interrupt");
  const first = await worker.waitFor((event) => event.type === "mekugi_context_usage" && event.result_uuid === results[0].uuid);
  assert.deepEqual(first, { type: "mekugi_context_usage", session_id: results[0].session_id, result_uuid: results[0].uuid,
    context_tokens: outcome === "native" ? 1234 : null, context_window: outcome === "native" ? 200000 : null });
  await worker.waitFor((event) => event.fixture_context_reads === 2);
  assert.deepEqual(worker.events.filter((event) => event.fixture_context_reads).map((event) => event.fixture_context_pending), [1, 1]);
  assert.deepEqual(JSON.parse(await readFile(report, "utf8")), Array.from({ length: 2 }, () => ({ subtype: "get_context_usage", detail: "summary" })));
  if (outcome !== "unavailable") {
    await worker.request("interrupt");
    const latest = await worker.waitFor((event) => event.type === "mekugi_context_usage" && event.result_uuid === results[2].uuid);
    assert.deepEqual(latest, { type: "mekugi_context_usage", session_id: results[2].session_id, result_uuid: results[2].uuid,
      context_tokens: 1234, context_window: 200000 });
  }
  assert.equal(JSON.stringify(worker.events).includes("private fixture context failure"), false);
  await worker.request("shutdown");
  assert.equal(await worker.exit, 0);
  assert.deepEqual(worker.events.filter((event) => event.type === "mekugi_context_usage").map((event) => event.result_uuid),
    outcome === "unavailable" ? [results[0].uuid] : [results[0].uuid, results[2].uuid]);
});

test("native permission waits for host answer without widening captured policy", { timeout: 15_000 }, async (t) => {
  let approve;
  let asked;
  const approval = new Promise((resolve) => { approve = resolve; });
  const requested = new Promise((resolve) => { asked = resolve; });
  const worker = start(t, (method, params) => {
    assert.equal(method, "permission"); assert.equal(params.toolUseID, "permission-id"); asked(); return approval;
  });
  await worker.initialize({ permissionMode: "default" });
  await worker.input("permission");
  await requested;
  assert.equal(worker.events.filter((event) => event.type === "result").length, 0);
  approve({ behavior: "deny", message: "host rejected" });
  const result = await worker.waitFor((event) => event.type === "result");
  assert.equal(result.permission.behavior, "deny");
  await worker.request("shutdown");
});

test("SDK startup failure rejects initialization", { timeout: 15_000 }, async (t) => {
  const worker = start(t, () => ({}), { MEKUGI_FIXTURE_INIT_ERROR: "1" });
  await assert.rejects(worker.initialize(), /exited with code 23|initialization failure/);
});

test("native interrupt cancels an outstanding host approval", { timeout: 15_000 }, async (t) => {
  let asked;
  let cancelled;
  const requested = new Promise((resolve) => { asked = resolve; });
  const cancellation = new Promise((resolve) => { cancelled = resolve; });
  const worker = start(t, (method) => {
    assert.equal(method, "permission"); asked(); return new Promise(() => {});
  }, {}, () => cancelled());
  await worker.initialize();
  await worker.input("permission");
  await requested;
  const receipt = await worker.request("interrupt");
  assert.deepEqual(receipt.still_queued, []);
  await cancellation;
  const result = await worker.waitFor((event) => event.type === "result");
  assert.equal(result.terminal_reason, "aborted_tools");
  await worker.request("shutdown");
});

test("interrupt cancels authenticated MCP host execution", { timeout: 15_000 }, async (t) => {
  let called;
  let cancelled;
  const invocation = new Promise((resolve) => { called = resolve; });
  const cancellation = new Promise((resolve) => { cancelled = resolve; });
  const worker = start(t, (method) => {
    if (method === "hook") return {};
    assert.equal(method, "tool"); called(); return new Promise(() => {});
  }, {}, () => cancelled());
  await worker.initialize();
  await worker.input("parallel");
  await invocation;
  await worker.request("interrupt");
  await cancellation;
  await worker.request("shutdown");
});

test("native CLI initialization discovers models without input, inference or session transcripts", { skip: process.env.MEKUGI_TEST_CLAUDE_NATIVE !== "1", timeout: 30_000 }, async (t) => {
  const claude = Bun.which("claude");
  assert.ok(claude, "Opted-in native acceptance requires Claude Code");
  const directory = await mkdtemp(join(tmpdir(), "mekugi-claude-discovery-"));
  const requests = [];
  const provider = createMockProvider((body) => { requests.push(body); return { content: [{ type: "text", text: "Unexpected inference" }] }; });
  provider.listen(0, "127.0.0.1");
  await once(provider, "listening");
  const effects = [];
  const worker = start(t, async (method) => { effects.push(method); throw new Error("Discovery rejects runtime effects"); }, {
    CLAUDE_CONFIG_DIR: join(directory, "config"), ANTHROPIC_BASE_URL: `http://127.0.0.1:${provider.address().port}`,
    ANTHROPIC_API_KEY: "local-fixture", ANTHROPIC_AUTH_TOKEN: "", CLAUDE_CODE_OAUTH_TOKEN: "",
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1", CLAUDE_CODE_USE_BEDROCK: "0", CLAUDE_CODE_USE_VERTEX: "0", CLAUDE_CODE_USE_FOUNDRY: "0",
  });
  t.after(async () => { worker.closeInput(); await worker.exit; await new Promise((resolve) => provider.close(resolve)); await rm(directory, { recursive: true, force: true }); });
  const initialization = await worker.request("initialize", { workspace: directory, claudePath: claude, options: { tools: [], persistSession: false }, tools: [] });
  assert.ok(initialization.models.length > 0);
  for (const model of initialization.models) {
    assert.equal(typeof model.value, "string");
    assert.notEqual(model.value, "");
    assert.equal(typeof model.displayName, "string");
    assert.equal(typeof model.description, "string");
    if (model.resolvedModel !== undefined) assert.equal(typeof model.resolvedModel, "string");
  }
  await worker.request("shutdown");
  assert.equal(await worker.exit, 0);
  assert.deepEqual(requests, [], "initialization must not make provider message requests");
  assert.deepEqual(effects, [], "initialization must not execute runtime effects");
  assert.equal(worker.events.some((event) => ["user", "assistant", "result", "command_lifecycle"].includes(event.type)), false);
  const projects = await readdir(join(directory, "config", "projects"), { recursive: true }).catch((error) => { if (error.code === "ENOENT") return []; throw error; });
  assert.equal(projects.some((path) => path.endsWith(".jsonl")), false, "discovery must not retain a session transcript");
  const alive = (pid) => { try { process.kill(pid, 0); return true; } catch (error) { if (error.code === "ESRCH") return false; throw error; } };
  assert.equal(typeof initialization.pid, "number");
  for (let i = 0; i < 100 && alive(initialization.pid); i++) await Bun.sleep(10);
  assert.equal(alive(initialization.pid), false, "native discovery process outlived joined shutdown");
});

test("native CLI publishes metrics before a tool finishes", { skip: process.env.MEKUGI_TEST_CLAUDE_NATIVE !== "1", timeout: 30_000 }, async (t) => {
  const claude = Bun.which("claude");
  assert.ok(claude, "Opted-in native acceptance requires Claude Code");
  const directory = await mkdtemp(join(tmpdir(), "mekugi-claude-live-metrics-"));
  let releaseTool;
  const toolGate = new Promise((resolve) => { releaseTool = resolve; });
  let releaseBlock;
  const blockGate = new Promise((resolve) => { releaseBlock = resolve; });
  let releaseLastBlock;
  const lastBlockGate = new Promise((resolve) => { releaseLastBlock = resolve; });
  let firstToolStarted;
  const toolStarted = new Promise((resolve) => { firstToolStarted = resolve; });
  const provider = createMockProvider(() => ({ content: [{ type: "text", text: "Starting work" },
    { type: "tool_use", id: "tool-live", name: "mcp__threadcontrol__coordinate", input: { value: "wait" } },
    { type: "tool_use", id: "tool-live-2", name: "mcp__threadcontrol__coordinate", input: { value: "wait again" } }],
    beforeBlock: (index) => index === 1 ? blockGate : index === 2 ? lastBlockGate : undefined }));
  provider.listen(0, "127.0.0.1");
  await once(provider, "listening");
  const worker = start(t, async (method) => { if (method === "tool") { firstToolStarted(); return toolGate; } return {}; }, {
    CLAUDE_CONFIG_DIR: join(directory, "config"), ANTHROPIC_BASE_URL: `http://127.0.0.1:${provider.address().port}`,
    ANTHROPIC_API_KEY: "local-fixture", ANTHROPIC_AUTH_TOKEN: "", CLAUDE_CODE_OAUTH_TOKEN: "",
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1", CLAUDE_CODE_USE_BEDROCK: "0", CLAUDE_CODE_USE_VERTEX: "0", CLAUDE_CODE_USE_FOUNDRY: "0",
  });
  t.after(async () => { releaseBlock(); releaseLastBlock(); releaseTool({ content: [] }); worker.closeInput(); await worker.exit; await new Promise((resolve) => provider.close(resolve)); await rm(directory, { recursive: true, force: true }); });
  await worker.request("initialize", { workspace: directory, claudePath: claude,
    options: { model: "claude-sonnet-4-6", permissionMode: "bypassPermissions", allowDangerouslySkipPermissions: true, maxTurns: 1 },
    tools: [{ name: "coordinate", description: "Wait", inputSchema: { type: "object", properties: { value: { type: "string" } }, required: ["value"] } }] });
  await worker.input("work");
  const partial = await worker.waitFor((event) => event.type === "mekugi_live_metrics");
  assert.deepEqual(partial.modelUsage, {}, "first content block precedes native accounting");
  releaseBlock();
  await toolStarted;
  releaseLastBlock();
  const metrics = await worker.waitFor((event) => event.type === "mekugi_live_metrics" && Object.values(event.modelUsage ?? {}).some((usage) => usage.inputTokens === 20));
  assert.equal(worker.events.some((event) => event.type === "result"), false);
  assert.ok(metrics.context_tokens > 0);
  assert.ok(metrics.context_window > 0);
  assert.equal(Object.values(metrics.modelUsage)[0].inputTokens, 20);
  assert.equal(Object.values(metrics.modelUsage)[0].outputTokens, 5);
  assert.ok(metrics.total_cost_usd > 0);
  console.log(JSON.stringify({ nativeEarlyMetrics: metrics }));
  releaseTool({ content: [] });
  await worker.waitFor((event) => event.type === "result");
  await worker.request("shutdown");
});

test("native CLI consumes synthetic input and stops a waiting tool batch with a local mock provider", { skip: process.env.MEKUGI_TEST_CLAUDE_NATIVE !== "1", timeout: 30_000 }, async (t) => {
  const claude = Bun.which("claude");
  assert.ok(claude, "Opted-in native acceptance requires Claude Code");
  const directory = await mkdtemp(join(tmpdir(), "mekugi-claude-native-"));
  await mkdir(join(directory, ".claude", "agents"), { recursive: true });
  await writeFile(join(directory, ".claude", "agents", "native-role.md"), "---\nname: native-role\ndescription: native-role-catalog-marker\nmodel: haiku\n---\nnative-role-prompt-marker");
  await writeFile(join(directory, ".claude", "settings.json"), JSON.stringify({ agent: "native-role" }));
  const requests = [];
  const providerStarts = [];
  let reportFailure;
  const failedAttempt = new Promise((resolve) => { reportFailure = resolve; });
  let reportHeld;
  let heldAttempt = new Promise((resolve) => { reportHeld = resolve; });
  let releaseHeld;
  let phase = "answer";
  const provider = createMockProvider(async (body) => {
    requests.push(body);
    providerStarts.push(worker.events.findLast((event) => event.type === "command_lifecycle" && event.state === "started")?.command_uuid);
    console.log(JSON.stringify({ nativeProviderRequest: { phase, count: requests.length, started: providerStarts.at(-1) } }));
    if (phase === "hold") { reportHeld(); await new Promise((resolve) => { releaseHeld = resolve; }); }
    if (phase === "error") { reportFailure(); return { status: 401, error: { type: "authentication_error", message: "local fixture authentication failure" } }; }
    const content = phase === "wait" ? [{ type: "tool_use", id: "native_coordination_call", name: "mcp__threadcontrol__coordinate", input: { value: "wait" } }]
      : phase === "edits" ? [
        { type: "tool_use", id: "native_write_call", name: "Write", input: { file_path: join(directory, "native-write.txt"), content: "native write\n" } },
        { type: "tool_use", id: "native_bash_call", name: "Bash", input: { command: "printf 'native bash\\n' > native-bash.txt", description: "Write fixture file" } },
      ] : phase === "long" ? [{ type: "tool_use", id: "native_running_call", name: "Bash", input: { command: "printf '%s' $$ > native-running.pid; exec sleep 60", description: "Wait for fixture shutdown", timeout: 120000 } }]
      : [{ type: "text", text: "Local fixture completed." }];
    return { content };
  });
  provider.listen(0, "127.0.0.1");
  await once(provider, "listening");
  t.after(() => new Promise((resolve) => provider.close(resolve)));
  const hooks = [];
  const tools = [];
  let permissionPrompts = 0;
  const onRequest = (method, params) => {
    if (method === "permission") { permissionPrompts++; return { behavior: "deny", message: "Unexpected prompt under never approval policy" }; }
    if (method === "tool") { tools.push(params); return { content: [{ type: "text", text: "Waiting for the delegated task." }] }; }
    assert.equal(method, "hook"); hooks.push(params);
    if (params.hook_event_name === "PostToolBatch" && phase !== "answer") return { continue: false, stopReason: "fixture tool batch complete" };
    if (params.hook_event_name === "PostToolUse") return { hookSpecificOutput: { hookEventName: "PostToolUse", additionalContext: "fixture receipt" } };
    return {};
  };
  const environment = { CLAUDE_CONFIG_DIR: join(directory, "config"), ANTHROPIC_BASE_URL: `http://127.0.0.1:${provider.address().port}`,
    ANTHROPIC_API_KEY: "local-fixture", ANTHROPIC_AUTH_TOKEN: "", CLAUDE_CODE_OAUTH_TOKEN: "",
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1", CLAUDE_CODE_USE_BEDROCK: "0", CLAUDE_CODE_USE_VERTEX: "0", CLAUDE_CODE_USE_FOUNDRY: "0" };
  let worker = start(t, onRequest, environment);
  t.after(async () => { worker.closeInput(); await worker.exit; await rm(directory, { recursive: true, force: true }); });
  const sessionId = randomUUID();
  const startup = { workspace: directory, claudePath: claude, options: { model: "claude-sonnet-4-6", permissionMode: "bypassPermissions", allowDangerouslySkipPermissions: true, sandbox: { enabled: false }, maxTurns: 3 },
    tools: [{ name: "coordinate", description: "Wait for a task", inputSchema: { type: "object", properties: { value: { type: "string" } }, required: ["value"] } }] };
  let initialization = await worker.request("initialize", { ...startup, options: { ...startup.options, sessionId } });
  const origin = { kind: "peer", from: "fixture", fromMode: "bypass" };
  const retained = await worker.input("retained-fixture-fact", { shouldQuery: false, isSynthetic: true, origin });
  await worker.waitFor((event) => event.type === "result" && event.user_message_uuid === retained.uuid);
  assert.equal(requests.length, 0, "retained input must not perform inference");
  const queried = await worker.input("query-fixture-fact", { isSynthetic: true, origin });
  const completion = await worker.waitFor((event) => event.type === "result" && event.user_message_uuid === queried.uuid);
  assert.equal(completion.is_error, false);
  assert.equal(requests[0].model, "claude-sonnet-4-6", "native role must not replace the captured model");
  assert.equal(JSON.stringify(requests[0]).includes("native-role-"), false, "native role prompts and catalogs must not reach inference");
  assert.equal(requests[0].tools.some((tool) => ["Agent", "Task", "TeamCreate", "TeamDelete", "SendMessage"].includes(tool.name)), false);
  assert.deepEqual(Object.values(completion.modelUsage).map(({ inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens }) =>
    ({ inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens })),
  [{ inputTokens: 20, outputTokens: 5, cacheReadInputTokens: 0, cacheCreationInputTokens: 0 }]);
  const context = await worker.waitFor((event) => event.type === "mekugi_context_usage" && event.result_uuid === completion.uuid);
  assert.equal(context.session_id, completion.session_id);
  assert.ok(Number.isFinite(context.context_tokens) && context.context_tokens >= 0);
  assert.ok(Number.isFinite(context.context_window) && context.context_window > 0);
  assert.equal(requests.length, 1);
  // Save native accounting, then fork from a resumed source before its first input.
  const sourceEvents = worker.events;
  await worker.request("shutdown");
  assert.equal(await worker.exit, 0);
  worker = start(t, onRequest, environment);
  await worker.request("initialize", { ...startup, options: { ...startup.options, resume: sessionId } });
  const resumedSourceWorker = worker;
  const savedForkWorker = start(t, onRequest, environment);
  const savedForkSessionId = randomUUID();
  await savedForkWorker.request("initialize", { ...startup, options: { ...startup.options, resume: sessionId, forkSession: true, sessionId: savedForkSessionId } });
  assert.equal(requests.length, 1, "fork baseline acquisition must not make another provider request");
  const forkBaseline = savedForkWorker.events.find((event) => event.type === "mekugi_usage_baseline");
  assert.ok(forkBaseline, "fork baseline must arrive before initialization acknowledges readiness");
  worker = savedForkWorker;
  const savedForkInput = await savedForkWorker.input("saved-fork-fixture");
  const savedForkResult = await savedForkWorker.waitFor((event) => event.type === "result" && event.user_message_uuid === savedForkInput.uuid);
  worker = resumedSourceWorker;
  const savedResumeInput = await worker.input("saved-resume-fixture", { isSynthetic: true, origin });
  const savedResumeResult = await worker.waitFor((event) => event.type === "result" && event.user_message_uuid === savedResumeInput.uuid);
  assert.equal(requests.length, 3, "saved fork and resume must each make one additional loopback inference request");
  assert.equal(savedForkResult.is_error, false);
  assert.equal(savedResumeResult.is_error, false);
  assert.equal(forkBaseline.session_id, savedForkSessionId);
  for (const [usage, expectedInput, expectedOutput] of [
    [forkBaseline.modelUsage, 20, 5], [savedForkResult.modelUsage, 40, 10], [savedResumeResult.modelUsage, 40, 10],
  ]) assert.deepEqual(Object.values(usage).map(({ inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens }) =>
    ({ inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens })),
  [{ inputTokens: expectedInput, outputTokens: expectedOutput, cacheReadInputTokens: 0, cacheCreationInputTokens: 0 }]);
  assert.equal(forkBaseline.total_cost_usd, completion.total_cost_usd);
  assert.equal(savedForkResult.total_cost_usd, completion.total_cost_usd * 2);
  assert.equal(savedResumeResult.total_cost_usd, savedForkResult.total_cost_usd);
  await savedForkWorker.request("shutdown");
  assert.equal(await savedForkWorker.exit, 0);
  assert.ok(JSON.stringify(requests[0].messages).includes("retained-fixture-fact"));
  assert.ok(JSON.stringify(requests[0].messages).includes("query-fixture-fact"));
  const replay = sourceEvents.find((event) => event.type === "user" && event.uuid === queried.uuid);
  console.log(JSON.stringify({ nativeInputEvidence: { queriedUUID: queried.uuid, replayUUID: replay?.uuid,
    consumedUUIDs: completion.user_message_uuids,
    lifecycle: sourceEvents.filter((event) => event.type === "command_lifecycle").map((event) => ({ input: event.command_uuid === queried.uuid ? "query" : "retained", state: event.state })) } }));
  phase = "wait";
  const wait = await worker.input("wait-fixture", { isSynthetic: true, origin });
  const stopped = await worker.waitFor((event) => event.type === "result" && event.user_message_uuid === wait.uuid);
  assert.equal(stopped.terminal_reason, "hook_stopped");
  assert.equal(tools.length, 1);
  assert.equal(tools[0].toolUseID, "native_coordination_call");
  assert.ok(hooks.some((hook) => hook.hook_event_name === "PostToolBatch"));
  assert.equal(requests.length, 4, "waiting must not make another provider request");
  phase = "edits";
  const edits = await worker.input("edit-fixture", { isSynthetic: true, origin });
  await Promise.race([worker.waitFor((event) => event.type === "result" && event.user_message_uuid === edits.uuid), new Promise((_, reject) => setTimeout(() => reject(new Error("native edit turn did not settle after waiting turn")), 5000))]);
  assert.equal(await readFile(join(directory, "native-write.txt"), "utf8"), "native write\n");
  assert.equal(await readFile(join(directory, "native-bash.txt"), "utf8"), "native bash\n");
  assert.equal(permissionPrompts, 0);
  for (const name of ["Write", "Bash"]) {
    assert.ok(hooks.some((hook) => hook.hook_event_name === "PreToolUse" && hook.tool_name === name));
    assert.ok(hooks.some((hook) => hook.hook_event_name === "PostToolUse" && hook.tool_name === name && hook.tool_response));
  }
  phase = "hold";
  const overlapStart = worker.events.length;
  const original = await worker.input("overlap-original-fixture", { isSynthetic: true, origin });
  await heldAttempt;
  phase = "answer";
  const followup = await worker.input("overlap-followup-fixture", { isSynthetic: true, origin, priority: "now" });
  await worker.waitFor((event) => event.type === "command_lifecycle" && event.command_uuid === followup.uuid && event.state === "queued");
  releaseHeld();
  await worker.waitFor((event) => event.type === "result" && event.user_message_uuids?.includes(followup.uuid));
  assert.ok(requests.some((request) => JSON.stringify(request.messages).includes("overlap-followup-fixture")));
  console.log(JSON.stringify({ nativeOverlap: { original: original.uuid, followup: followup.uuid,
    events: worker.events.slice(overlapStart).filter((event) => event.type === "result" || event.type === "command_lifecycle").map((event) => ({ type: event.type, state: event.state, command_uuid: event.command_uuid, terminal_reason: event.terminal_reason, user_message_uuids: event.user_message_uuids })) } }));
  phase = "error";
  const failed = await worker.input("error-fixture", { isSynthetic: true, origin });
  await failedAttempt;
  const failure = await worker.waitFor((event) => event.type === "result" && event.user_message_uuid === failed.uuid);
  assert.equal(failure.terminal_reason, "api_error");
  assert.equal(failure.is_error, true);
  console.log(JSON.stringify({ nativeFailureEvidence: { subtype: failure.subtype, terminalReason: failure.terminal_reason, inputUUID: failure.user_message_uuid, consumedUUIDs: failure.user_message_uuids }, nativeEditHooks: hooks.filter((hook) => hook.tool_name === "Write" || hook.tool_name === "Bash").map((hook) => ({ event: hook.hook_event_name, name: hook.tool_name, id: hook.tool_use_id })), permissionPrompts }));
  phase = "hold";
  heldAttempt = new Promise((resolve) => { reportHeld = resolve; });
  const interrupted = await worker.input("interrupted-fixture", { isSynthetic: true, origin });
  await heldAttempt;
  const cancelled = await worker.input("cancelled-followup-fixture", { isSynthetic: true, origin, priority: "later" });
  await worker.waitFor((event) => event.type === "command_lifecycle" && event.command_uuid === cancelled.uuid && event.state === "queued");
  const interruption = await worker.request("interrupt");
  assert.deepEqual(interruption.still_queued, []);
  assert.ok(interruption.cancelled.includes(cancelled.uuid));
  phase = "answer";
  releaseHeld();
  const interruptedResult = await worker.waitFor((event) => event.type === "result" && event.user_message_uuid === interrupted.uuid);
  assert.equal(interruptedResult.terminal_reason, "aborted_streaming");
  console.log(JSON.stringify({ nativeQueuedCancellation: interruption }));
  // Mark the settled effects so replaying either original tool would destroy
  // the markers even if it rewrote byte-identical original content.
  const effects = [
    [join(directory, "native-write.txt"), "native write\ncontinuity marker\n"],
    [join(directory, "native-bash.txt"), "native bash\ncontinuity marker\n"],
  ];
  for (const [path, content] of effects) await writeFile(path, content);
  const previousToolHooks = hooks.filter((hook) => hook.hook_event_name === "PreToolUse").length;
  await worker.request("shutdown");
  assert.equal(await worker.exit, 0);
  worker = start(t, onRequest, environment);
  initialization = await worker.request("initialize", { ...startup, options: { ...startup.options, resume: sessionId } });
  const resumed = await worker.input("resume-fixture", { isSynthetic: true, origin });
  const resumedResult = await worker.waitFor((event) => event.type === "result" && event.user_message_uuid === resumed.uuid);
  assert.equal(resumedResult.is_error, false);
  assert.equal(resumedResult.session_id, sessionId);
  assert.equal(worker.events.find((event) => event.type === "system" && event.subtype === "init")?.session_id, sessionId);
  assert.ok(JSON.stringify(requests.at(-1).messages).includes("query-fixture-fact"));
  assert.ok(JSON.stringify(requests.at(-1).messages).includes("native_write_call"));
  assert.equal(hooks.filter((hook) => hook.hook_event_name === "PreToolUse").length, previousToolHooks);
  for (const [path, content] of effects) assert.equal(await readFile(path, "utf8"), content);
  await worker.request("shutdown");
  assert.equal(await worker.exit, 0);
  const forkSessionId = randomUUID();
  worker = start(t, onRequest, environment);
  initialization = await worker.request("initialize", { ...startup, options: { ...startup.options, resume: sessionId, forkSession: true, sessionId: forkSessionId } });
  const forked = await worker.input("fork-fixture", { isSynthetic: true, origin });
  const forkedResult = await worker.waitFor((event) => event.type === "result" && event.user_message_uuid === forked.uuid);
  assert.equal(forkedResult.is_error, false);
  assert.equal(forkedResult.session_id, forkSessionId);
  assert.notEqual(forkSessionId, sessionId);
  assert.equal(worker.events.find((event) => event.type === "system" && event.subtype === "init")?.session_id, forkSessionId);
  assert.ok(JSON.stringify(requests.at(-1).messages).includes("resume-fixture"));
  assert.ok(JSON.stringify(requests.at(-1).messages).includes("native_write_call"));
  assert.equal(hooks.filter((hook) => hook.hook_event_name === "PreToolUse").length, previousToolHooks);
  for (const [path, content] of effects) assert.equal(await readFile(path, "utf8"), content);
  console.log(JSON.stringify({ nativeContinuity: { sourceSession: sessionId, resumedSession: resumedResult.session_id, forkedSession: forkedResult.session_id, replayedTools: 0 } }));
  phase = "long";
  const running = await worker.input("running-fixture", { isSynthetic: true, origin });
  let toolPID;
  while (!toolPID) {
    try { toolPID = Number(await readFile(join(directory, "native-running.pid"), "utf8")); } catch (error) { if (error.code !== "ENOENT") throw error; }
    if (!toolPID) await Bun.sleep(10);
  }
  worker.closeInput();
  assert.equal(await worker.exit, 0);
  const alive = (pid) => { try { process.kill(pid, 0); return true; } catch (error) { if (error.code === "ESRCH") return false; throw error; } };
  assert.equal(typeof initialization.pid, "number");
  for (const pid of [initialization.pid, toolPID]) {
    for (let i = 0; i < 100 && alive(pid); i++) await Bun.sleep(10);
    assert.equal(alive(pid), false, `owned native process ${pid} outlived worker cleanup`);
  }
  assert.deepEqual(providerStarts, [queried.uuid, savedForkInput.uuid, savedResumeInput.uuid, wait.uuid, edits.uuid, original.uuid, followup.uuid, failed.uuid, interrupted.uuid, resumed.uuid, forked.uuid, running.uuid]);
  assert.equal(requests.length, 12);
});
