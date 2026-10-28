import { randomUUID } from "node:crypto";
import { createInterface } from "node:readline";
import {
  createSdkMcpServer, query, tool,
  type HookCallback, type Options, type Query,
  type SDKControlGetContextUsageResponse, type SDKControlGetUsageResponse, type SDKResultMessage, type SDKUserMessage, type SyncHookJSONOutput,
} from "@anthropic-ai/claude-agent-sdk";
import { CallToolResultSchema } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";

const object = z.record(z.string(), z.unknown());
const requestSchema = z.object({ kind: z.literal("request"), id: z.string(), method: z.string(), params: z.unknown().optional() });
const responseSchema = z.object({ kind: z.literal("response"), id: z.string(), result: z.unknown().optional(), error: z.string().optional() });
const initializeSchema = z.object({
  workspace: z.string(), claudePath: z.string(), options: object.default({}),
  tools: z.array(z.object({ name: z.string(), description: z.string(), inputSchema: object })).default([]),
});
const inputSchema = z.object({
  type: z.literal("user"), uuid: z.uuid(),
  message: z.object({ role: z.literal("user"), content: z.union([z.string(), z.array(object)]) }),
  parent_tool_use_id: z.string().nullable(), session_id: z.string().optional(),
  isSynthetic: z.boolean().optional(), origin: object.optional(), shouldQuery: z.boolean().optional(),
  priority: z.enum(["now", "next", "later"]).optional(),
}).passthrough();
const hookOutputSchema = z.object({
  continue: z.boolean().optional(), suppressOutput: z.boolean().optional(), stopReason: z.string().optional(),
  decision: z.enum(["approve", "block"]).optional(), reason: z.string().optional(), systemMessage: z.string().optional(),
  hookSpecificOutput: z.discriminatedUnion("hookEventName", [
    z.object({ hookEventName: z.literal("PreToolUse"), permissionDecision: z.enum(["allow", "deny", "ask"]).optional(),
      permissionDecisionReason: z.string().optional(), updatedInput: object.optional(), additionalContext: z.string().optional() }),
    z.object({ hookEventName: z.literal("PostToolUse"), additionalContext: z.string().optional() }),
    z.object({ hookEventName: z.literal("PostToolUseFailure"), additionalContext: z.string().optional() }),
    z.object({ hookEventName: z.literal("PostToolBatch"), additionalContext: z.string().optional() }),
  ]).optional(),
});
const permissionSchema = z.discriminatedUnion("behavior", [
  z.object({ behavior: z.literal("allow"), updatedInput: object.optional() }),
  z.object({ behavior: z.literal("deny"), message: z.string(), interrupt: z.boolean().optional() }),
]);
const tokenField = "_mekugi_call_token";
const serverName = "threadcontrol";
const auxiliaryReadTimeoutMs = 5_000;

function errorText(error: unknown): string { return error instanceof Error ? error.message : String(error); }
function send(frame: object): void { process.stdout.write(`${JSON.stringify(frame)}\n`); }

// Input retention/dispatch policy belongs to the host. This bounded hand-off
// queue acknowledges only local enqueue, never CLI acceptance or persistence.
class InputQueue implements AsyncIterable<SDKUserMessage> {
  private values: SDKUserMessage[] = [];
  private wake: (() => void) | undefined;
  private closed = false;
  push(value: SDKUserMessage): void {
    if (this.closed) throw new Error("Claude input stream is closed");
    if (this.values.length >= 64) throw new Error("Claude input hand-off queue is full");
    this.values.push(value);
    this.wake?.();
  }
  close(): void { this.closed = true; this.values = []; this.wake?.(); }
  cancelQueued(): string[] {
    const cancelled = this.values.flatMap((value) => value.uuid ? [value.uuid] : []);
    this.values = [];
    return cancelled;
  }
  async *[Symbol.asyncIterator](): AsyncGenerator<SDKUserMessage> {
    while (!this.closed) {
      const value = this.values.shift();
      if (value) { yield value; continue; }
      await new Promise<void>((resolve) => { this.wake = resolve; });
      this.wake = undefined;
    }
  }
}

type Authority = { name: string; toolUseID: string; arguments: Record<string, unknown> };
type Pending = { method: string; cancel(): void; resolve(value: unknown): void; reject(error: Error): void };
type ResultIdentity = Pick<SDKResultMessage, "session_id" | "uuid">;

class Worker {
  private runtime: Query | undefined;
  private initializing = false;
  private ready = false;
  private stopping = false;
  private pump: Promise<void> | undefined;
  private contextRead: Promise<void> | undefined;
  private contextTimer: ReturnType<typeof setTimeout> | undefined;
  private pendingContext: ResultIdentity | undefined;
  private readonly abort = new AbortController();
  private readonly inputs = new InputQueue();
  private readonly pending = new Map<string, Pending>();
  private readonly tokens = new Map<string, Authority>();
  private readonly tokenByID = new Map<string, string>();
  private readonly toolNames = new Map<string, string>();

  respond(value: unknown): void {
    const frame = responseSchema.parse(value);
    const pending = this.pending.get(frame.id);
    if (!pending) return;
    this.pending.delete(frame.id);
    if (frame.error) pending.reject(new Error(frame.error)); else pending.resolve(frame.result);
  }

  private async callHost(method: string, params: unknown, signal = this.abort.signal): Promise<unknown> {
    signal.throwIfAborted();
    const id = randomUUID();
    let cancel = () => {};
    try {
      return await new Promise<unknown>((resolve, reject) => {
        cancel = () => { this.pending.delete(id); send({ kind: "cancel", id }); reject(new Error("Claude callback cancelled")); };
        signal.addEventListener("abort", cancel, { once: true });
        this.pending.set(id, { method, cancel, resolve, reject });
        send({ kind: "request", id, method, params });
      });
    } finally { signal.removeEventListener("abort", cancel); }
  }

  private forget(toolUseID: string): void {
    const token = this.tokenByID.get(toolUseID);
    if (token) this.tokens.delete(token);
    this.tokenByID.delete(toolUseID);
  }

  private readonly hook: HookCallback = async (input, _toolUseID, { signal }) => {
    try {
      const output: SyncHookJSONOutput = hookOutputSchema.parse(await this.callHost("hook", input, signal));
      if (input.hook_event_name === "PreToolUse") {
        if (input.tool_name === "Task" || input.tool_name === "Agent") {
          return { hookSpecificOutput: { hookEventName: "PreToolUse", permissionDecision: "deny", permissionDecisionReason: "Native delegation is disabled; use coordination tools" } };
        }
        const name = this.toolNames.get(input.tool_name);
        if (!name || output.continue === false || output.decision === "block") return output;
        const specific = output.hookSpecificOutput;
        if (specific && specific.hookEventName !== "PreToolUse") throw new Error("Mismatched PreToolUse host response");
        if (specific?.permissionDecision === "deny") return output;
        if (input.mcp_server?.source !== "sdk" || input.mcp_server.name !== serverName) {
          throw new Error("Coordination tool lacks native SDK server provenance");
        }
        const args = { ...object.parse(specific?.updatedInput ?? input.tool_input) };
        delete args[tokenField];
        this.forget(input.tool_use_id);
        const token = randomUUID();
        this.tokens.set(token, { name, toolUseID: input.tool_use_id, arguments: args });
        this.tokenByID.set(input.tool_use_id, token);
        return { ...output, hookSpecificOutput: { ...specific, hookEventName: "PreToolUse", updatedInput: { ...args, [tokenField]: token } } };
      }
      return output;
    } catch (error) {
      // SDK hook exceptions can fail open. Return an explicit stop/denial.
      return { continue: false, stopReason: `Mekugi ${input.hook_event_name}: ${errorText(error)}`,
        ...(input.hook_event_name === "PreToolUse" ? { hookSpecificOutput: { hookEventName: "PreToolUse" as const, permissionDecision: "deny" as const, permissionDecisionReason: errorText(error) } } : {}) };
    } finally {
      if (input.hook_event_name === "PostToolUse" || input.hook_event_name === "PostToolUseFailure") this.forget(input.tool_use_id);
      if (input.hook_event_name === "PostToolBatch") for (const call of input.tool_calls) this.forget(call.tool_use_id);
    }
  };

  private async initialize(params: unknown): Promise<unknown> {
    if (this.initializing) throw new Error("Claude worker is already initialized");
    this.initializing = true;
    const config = initializeSchema.parse(params);
    const forkSessionID = config.options.forkSession === true ? z.uuid().parse(config.options.sessionId) : undefined;
    const tools = config.tools.map((definition) => {
      const schema = z.fromJSONSchema(definition.inputSchema);
      if (!(schema instanceof z.ZodObject)) throw new Error(`Tool ${definition.name} requires an object schema`);
      if (tokenField in schema.shape) throw new Error(`Tool ${definition.name} uses a reserved argument`);
      const nativeName = `mcp__${serverName}__${definition.name}`;
      if (this.toolNames.has(nativeName)) throw new Error(`Duplicate tool ${definition.name}`);
      this.toolNames.set(nativeName, definition.name);
      return tool(definition.name, definition.description, { ...schema.shape, [tokenField]: z.string().optional() }, async (args, extra) => {
        const token = args[tokenField];
        const authority = typeof token === "string" ? this.tokens.get(token) : undefined;
        if (!authority || authority.name !== definition.name) throw new Error("Unauthenticated coordination tool invocation");
        this.forget(authority.toolUseID);
        const context = z.object({ signal: z.instanceof(AbortSignal) }).safeParse(extra);
        return CallToolResultSchema.parse(await this.callHost("tool", authority, context.success ? context.data.signal : this.abort.signal));
      });
    });
    const disallowedTools = z.array(z.string()).parse(config.options.disallowedTools ?? []);
    const extraArgs = z.record(z.string(), z.string().nullable()).parse(config.options.extraArgs ?? {});
    const options: Options = {
      ...config.options,
      cwd: config.workspace, pathToClaudeCodeExecutable: config.claudePath,
      env: process.env, settingSources: [],
      sandbox: { enabled: false },
      disallowedTools: [...new Set([...disallowedTools, "Agent", "Task"])],
      // This is a CLI flag, not an SDK Options property in the pinned SDK.
      extraArgs: { ...extraArgs, "replay-user-messages": null },
      strictMcpConfig: true,
      mcpServers: { [serverName]: createSdkMcpServer({ name: serverName, tools }) },
      abortController: this.abort,
      stderr: (data) => { process.stderr.write(data); },
      hooks: Object.fromEntries(["PreToolUse", "PostToolUse", "PostToolUseFailure", "PostToolBatch"].map((event) => [event, [{ hooks: [this.hook] }]])),
      canUseTool: async (name, args, info) => {
        const { signal, ...metadata } = info;
        try {
          const response = permissionSchema.parse(await this.callHost("permission", { name, arguments: args, ...metadata }, signal));
          if (response.behavior === "deny") this.forget(info.toolUseID);
          // Keep the worker-issued token across an admitted input rewrite.
          if (response.behavior === "allow" && response.updatedInput && this.tokenByID.has(info.toolUseID)) {
            const token = this.tokenByID.get(info.toolUseID)!;
            const authority = this.tokens.get(token)!;
            const updated = { ...response.updatedInput };
            delete updated[tokenField];
            authority.arguments = updated;
            response.updatedInput = { ...updated, [tokenField]: token };
          }
          return response;
        } catch (error) { this.forget(info.toolUseID); return { behavior: "deny", message: errorText(error), interrupt: true }; }
      },
    };
    this.runtime = query({ prompt: this.inputs, options });
    this.pump = this.forward(this.runtime);
    const initialization = await this.runtime.initializationResult();
    if (initialization.hooks_applied !== true) {
      this.stop();
      throw new Error("Claude runtime did not confirm the required native hooks");
    }
    if (forkSessionID) {
      let baseline: SDKControlGetUsageResponse["session"] | undefined;
      let timeout: ReturnType<typeof setTimeout> | undefined;
      // This public API is experimental in the pinned SDK; keep the adaptation here.
      try {
        baseline = (await Promise.race([
          this.runtime.usage_EXPERIMENTAL_MAY_CHANGE_DO_NOT_RELY_ON_THIS_API_YET({ skipBehaviors: true }),
          new Promise<undefined>((resolve) => { timeout = setTimeout(() => resolve(undefined), auxiliaryReadTimeoutMs); }),
        ]))?.session;
      }
      catch { /* Missing auxiliary usage must not fail initialization or expose provider errors. */ }
      finally { clearTimeout(timeout); }
      if (this.stopping) throw new Error("Claude worker stopped during initialization");
      send({ kind: "event", event: {
        type: "mekugi_usage_baseline", session_id: forkSessionID,
        modelUsage: baseline?.model_usage ?? null, total_cost_usd: baseline?.total_cost_usd ?? null,
      } });
    }
    this.ready = true;
    return initialization;
  }

  private scheduleContext(runtime: Query): void {
    if (this.contextRead || !this.pendingContext || this.stopping) return;
    const result = this.pendingContext;
    this.pendingContext = undefined;
    let published = false;
    const publish = (context?: SDKControlGetContextUsageResponse): void => {
      if (published || this.stopping) return;
      published = true;
      send({ kind: "event", event: {
        type: "mekugi_context_usage", session_id: result.session_id, result_uuid: result.uuid,
        context_tokens: context?.totalTokens ?? null, context_window: context?.rawMaxTokens ?? null,
      } });
    };
    this.contextTimer = setTimeout(publish, auxiliaryReadTimeoutMs);
    // Expiry publishes absence but retains the in-flight request until settlement,
    // keeping later results coalesced rather than accumulating SDK controls.
    this.contextRead = runtime.getContextUsage({ detail: "summary" }).then(publish, () => publish()).finally(() => {
      clearTimeout(this.contextTimer);
      this.contextTimer = undefined;
      this.contextRead = undefined;
      this.scheduleContext(runtime);
    });
  }

  private async forward(runtime: Query): Promise<void> {
    try {
      for await (const event of runtime) {
        if (event.type === "result") { this.tokens.clear(); this.tokenByID.clear(); }
        if (event.type === "system" && event.subtype === "permission_denied") this.forget(event.tool_use_id);
        send({ kind: "event", event });
        if (event.type === "result" && event.num_turns > 0) {
          this.pendingContext = { session_id: event.session_id, uuid: event.uuid };
          this.scheduleContext(runtime);
        }
      }
      if (!this.stopping) throw new Error("Claude SDK stream ended unexpectedly");
    } catch (error) {
      if (!this.stopping) { send({ kind: "fatal", error: errorText(error) }); this.stop(); }
    }
  }

  async request(method: string, params: unknown): Promise<unknown> {
    if (method === "initialize") return this.initialize(params);
    if (method === "shutdown") { this.stop(); await this.pump; await this.contextRead; return {}; }
    if (!this.ready || !this.runtime || this.stopping) throw new Error("Claude SDK is not ready");
    switch (method) {
      case "input": {
        // The SDK owns the full Messages API block/origin union. Envelope fields
        // are validated here; the native parser validates that versioned body.
        const input = inputSchema.parse(params) as SDKUserMessage;
        this.inputs.push(input);
        return { uuid: input.uuid, status: "queued" };
      }
      case "interrupt": {
        const workerCancelled = this.inputs.cancelQueued();
        this.tokens.clear(); this.tokenByID.clear();
        for (const pending of this.pending.values()) if (pending.method === "tool") pending.cancel();
        // SDK 0.3.312 implements this public method's optional argument, but
        // its declarations still omit it. Keep the pinned adaptation local.
        const interrupt: (options: { cancelQueued: true }) => ReturnType<Query["interrupt"]> = this.runtime.interrupt.bind(this.runtime);
        const receipt = await interrupt({ cancelQueued: true });
        if (!receipt || !receipt.cancelled || receipt.still_queued.length > 0) {
          this.stop();
          throw new Error("Claude did not confirm queued input cancellation; native runtime stopped");
        }
        return { ...receipt, worker_cancelled: workerCancelled };
      }
      default: throw new Error(`Unknown Claude worker method ${method}`);
    }
  }

  stop(): void {
    if (this.stopping) return;
    this.stopping = true;
    clearTimeout(this.contextTimer);
    this.contextTimer = undefined;
    this.pendingContext = undefined;
    this.inputs.close();
    this.runtime?.close();
    this.abort.abort();
    for (const pending of this.pending.values()) pending.cancel();
    this.pending.clear(); this.tokens.clear(); this.tokenByID.clear();
  }
}

const worker = new Worker();
const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
lines.on("line", (line) => {
  try {
    const value: unknown = JSON.parse(line);
    if (responseSchema.safeParse(value).success) { worker.respond(value); return; }
    const request = requestSchema.parse(value);
    void worker.request(request.method, request.params).then(
      (result) => { send({ kind: "response", id: request.id, result }); if (request.method === "shutdown") { lines.close(); process.stdin.destroy(); } },
      (error: unknown) => { send({ kind: "response", id: request.id, error: errorText(error) }); },
    );
  } catch (error) { send({ kind: "fatal", error: errorText(error) }); worker.stop(); lines.close(); process.stdin.destroy(); }
});
lines.on("close", () => worker.stop());
process.on("SIGTERM", () => { worker.stop(); lines.close(); process.stdin.destroy(); });
