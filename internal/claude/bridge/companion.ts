import { companionRequest, type CompanionConfig } from './companion_transport.js';
export type { CompanionConfig } from './companion_transport.js';
import { realpath } from 'node:fs/promises';
import type { HookCallback, HookInput, Options, SDKMessage, SDKSessionInfo, SyncHookJSONOutput } from '@anthropic-ai/claude-agent-sdk';
import { companionGuidance, companionFrontendGuidance, type CompanionPrompt } from './guidance.js';

interface Binding { runtime: 'claude'; session: string; workspace: string; agent?: string }
interface NativeCall { binding: Binding; id: string; tool: string; input: string; paths?: string[]; command?: string }
const terminal = (status: string): boolean => ['completed', 'failed', 'stopped'].includes(status);
const object = (value: unknown): Record<string, unknown> | undefined =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined;

// Only the bridge holds this capability. Session/agent hooks deliver the shared
// companion guidance.
export function companion(config: CompanionConfig, cwd: string, notice: (text: string) => Promise<void>, rootInputGuidance = true,
  prompt: CompanionPrompt = {workflow: companionGuidance(config), frontends: companionFrontendGuidance(config)}, verifyGuard?: (id: string) => Promise<void>): {
  hooks: Options['hooks']; event: (event: SDKMessage) => Promise<void>;
  resume: (info: SDKSessionInfo) => Promise<void>;
} {
  const guidance = prompt.workflow;
  const frontendHooks: HookCallback[] = prompt.frontends.map(context => {
    let deliveredSession: string | undefined;
    return async input => {
      try {
        const scope = await binding(input);
        const firstInput = input.hook_event_name === 'UserPromptSubmit' && !scope.agent && rootInputGuidance && deliveredSession !== scope.session;
        const child = input.hook_event_name === 'SubagentStart';
        const compact = input.hook_event_name === 'SessionStart' && input.source === 'compact';
        if (!firstInput && !child && !compact) return {};
        if (firstInput) deliveredSession = scope.session;
        return {hookSpecificOutput: {hookEventName: input.hook_event_name, additionalContext: context}};
      } catch (error) {
        void notice(`Frontend guidance unavailable: ${String(error)}`).catch(() => {});
        return {};
      }
    };
  });
  let rootGuidanceSession: string | undefined;
  const guarded = new Map<string, NativeCall>();
  const send = (payload: unknown, signal?: AbortSignal): Promise<unknown> => companionRequest(config, payload, signal);
  const binding = async (input: Pick<HookInput, 'session_id' | 'cwd' | 'agent_id'>): Promise<Binding> => {
    if (await realpath(input.cwd) !== cwd) throw new Error('native hook workspace differs from this launch');
    return {runtime: 'claude', session: input.session_id, workspace: cwd, ...(input.agent_id ? {agent: input.agent_id} : {})};
  };
  const report = (value: unknown): string => {
    const text = typeof value === 'string' ? value : JSON.stringify(value) ?? '';
    return text.slice(0, 32768);
  };
  const hook: HookCallback = async (input, toolUseID, options): Promise<SyncHookJSONOutput> => {
    try {
      const scope = await binding(input);
      if (input.hook_event_name === 'UserPromptSubmit') {
        if (!scope.agent && guidance && rootGuidanceSession !== scope.session) {
          rootGuidanceSession = scope.session;
          return {hookSpecificOutput: {hookEventName: 'UserPromptSubmit', additionalContext: guidance}};
        }
        return {};
      }
      if (input.hook_event_name === 'SessionStart' || input.hook_event_name === 'SubagentStart') {
        await send({operation: 'bind', binding: scope}, options.signal);
        let context = input.hook_event_name === 'SubagentStart' ? guidance : '';
        if (input.hook_event_name === 'SessionStart' && input.source === 'compact' && config.journalSchema) {
          context = guidance;
          try {
            const maxCharacters = 10000 - (guidance ? guidance.length + 2 : 0);
            const result = await companionRequest(config, {operation: 'journal_recovery', binding: scope, maxCharacters}, options.signal, 128 * 1024) as {text?: string};
            if (typeof result.text !== 'string' || result.text.length > maxCharacters) throw new Error('Native journal recovery carrier unavailable');
            await notice(`Journal recovery prepared (${result.text.length} characters); native summary preserved`);
            context = context ? `${context}\n\n${result.text}` : result.text;
          } catch (error) {
            void notice(`Journal recovery unavailable: ${String(error)}; native summary and workflow guidance preserved`).catch(() => {});
          }
        }
        return context ? {hookSpecificOutput: {hookEventName: input.hook_event_name, additionalContext: context}} : {};
      }
      if (input.hook_event_name !== 'PreToolUse' && input.hook_event_name !== 'PostToolUse' && input.hook_event_name !== 'PostToolUseFailure') return {};
      if (toolUseID && toolUseID !== input.tool_use_id) throw new Error('native hook tool IDs disagree');
      if (config.journalSchema && input.hook_event_name === 'PreToolUse' && ['Agent', 'Task'].includes(input.tool_name)) {
        await send({operation: 'journal_before', call: {binding: scope, id: input.tool_use_id, tool: input.tool_name, input: JSON.stringify(input.tool_input)}}, options.signal);
        return {};
      }
      if (config.journalSchema && input.hook_event_name === 'PreToolUse' && ['mcp__mekugi__journal_batch', 'mcp__mekugi__journal_read', 'mcp__mekugi__mchanges'].includes(input.tool_name)) {
        if (input.mcp_server?.name !== 'mekugi' || input.mcp_server.source !== 'sdk') throw new Error('Companion MCP provenance mismatch');
        await send({operation: 'journal_before', call: {binding: scope, id: input.tool_use_id, tool: input.tool_name, input: JSON.stringify(input.tool_input)}}, options.signal);
        return {};
      }
      if (!['Edit', 'Write', 'Bash'].includes(input.tool_name)) return {};
      const args = object(input.tool_input);
      if (!args) throw new Error('native tool input is not an object');
      const call: NativeCall = {binding: scope, id: input.tool_use_id, tool: input.tool_name, input: JSON.stringify(input.tool_input)};
      if (input.tool_name === 'Bash') {
        if (typeof args.command !== 'string') throw new Error('native Bash command missing');
        // The hook does not prove the actual shell executable. Leave it unknown:
        // the shared owner can snapshot writers, but cannot invent literal scope.
        call.command = args.command;
      } else {
        if (typeof args.file_path !== 'string' || !args.file_path) throw new Error('native file path missing');
        call.paths = [args.file_path];
      }
      if (input.hook_event_name === 'PreToolUse') {
        if (config.vcsGuard && input.tool_name === 'Bash') {
          // Instrumentation must fail closed, independently of auxiliary capture.
          await verifyGuard?.(input.tool_use_id);
          if (guarded.size >= 1024) throw new Error('native guard pending-call limit reached');
          const result = await send({operation: 'before', call}, options.signal) as {guardReady?: boolean; captureError?: string};
          if (result.guardReady !== true) throw new Error('native startup guard unavailable');
          guarded.set(call.id, call);
          if (result.captureError) void notice(`Companion capture unavailable: ${result.captureError}`).catch(() => {});
          // Native rules evaluate original input. Startup guards execution.
          return {};
        }
        await send({operation: 'before', call}, options.signal);
      }
      else if (input.hook_event_name === 'PostToolUseFailure') {
        const result = await send({operation: 'after', call, terminal: {status: input.is_interrupt ? 'stopped' : 'failed', report: input.error, interrupted: input.is_interrupt ?? false}}, options.signal) as {changeID?: string};
        guarded.delete(call.id);
        if (config.frontendDirectory && result.changeID) return {hookSpecificOutput: {hookEventName: 'PostToolUseFailure', additionalContext: `Mekugi recorded actual file effects: ${result.changeID}. Review this explicit ID with mchanges through native Bash.`}};
      }
      else {
        const response = object(input.tool_response);
        const task = typeof response?.backgroundTaskId === 'string' ? response.backgroundTaskId : undefined;
        if (args.run_in_background === true && !task) throw new Error('background Bash completion identity unavailable; capture left unfinished');
        const result = await send({operation: 'after', call, terminal: {status: task ? 'running' : 'completed', ...(task ? {task} : {}), report: report(input.tool_response)}}, options.signal) as {changeID?: string};
        guarded.delete(call.id);
        if (config.frontendDirectory && result.changeID) return {hookSpecificOutput: {hookEventName: 'PostToolUse', additionalContext: `Mekugi recorded actual file effects: ${result.changeID}. Review this explicit ID with mchanges through native Bash.`}};
      }
    } catch (error) {
      if (config.vcsGuard && input.hook_event_name === 'PreToolUse' && input.tool_name === 'Bash') {
        return {hookSpecificOutput: {hookEventName: 'PreToolUse', permissionDecision: 'deny', permissionDecisionReason: `Mekugi VCS guard unavailable: ${String(error)}`}};
      }
      void notice(`Companion capture unavailable: ${String(error)}`).catch(() => {});
    }
    return {};
  };
  const matcher = {hooks: [hook], timeout: 6};
  const contextMatcher = {...matcher, hooks: [hook, ...frontendHooks]};
  return {
    resume: async info => {
      try {
        if (!info.cwd) throw new Error('native resume workspace unavailable');
        await send({operation: 'bind', binding: await binding({session_id: info.sessionId, cwd: info.cwd})});
      } catch (error) { void notice(`Companion capture unavailable: ${String(error)}`).catch(() => {}); }
    },
    hooks: {SessionStart: [contextMatcher], SubagentStart: [contextMatcher], ...(guidance && rootInputGuidance ? {UserPromptSubmit: [contextMatcher]} : {}), PreToolUse: [{...matcher, matcher: config.journalSchema ? 'Edit|Write|Bash|Agent|Task|mcp__mekugi__journal_batch|mcp__mekugi__journal_read|mcp__mekugi__mchanges' : 'Edit|Write|Bash'}],
      PostToolUse: [{...matcher, matcher: 'Edit|Write|Bash'}], PostToolUseFailure: [{...matcher, matcher: 'Edit|Write|Bash'}]},
    event: async event => {
      try {
        // Native permission denial precedes execution and has no post-tool hook.
        // Its exact error result closes only the observation opened for this call.
        if (event.type === 'user' && Array.isArray(event.message.content)) {
          for (const block of event.message.content) {
            if (block.type !== 'tool_result' || !block.is_error) continue;
            const call = guarded.get(block.tool_use_id);
            if (!call || call.binding.session !== event.session_id) continue;
            await send({operation: 'after', call, terminal: {status: 'failed', report: report(block.content)}});
            guarded.delete(call.id);
          }
        }
        if (config.journalSchema && event.type === 'user') {
          const result = object(event.tool_use_result);
          if (typeof result?.agentId === 'string' && Array.isArray(event.message.content)) {
            const results = event.message.content.filter(block => block.type === 'tool_result');
            if (results.length === 1) await send({operation: 'journal_parent', nativeID: results[0]!.tool_use_id, child: result.agentId, terminal: {status: typeof result.status === 'string' ? result.status : ''}});
          }
        }
        if (event.type !== 'system') return;
        if (event.subtype === 'compact_boundary') {
          await notice('Native context compaction completed');
        } else if (event.subtype === 'init') {
          await send({operation: 'bind', binding: await binding({session_id: event.session_id, cwd: event.cwd})});
        } else if (config.journalSchema && event.subtype === 'task_started' && event.tool_use_id) {
          await send({operation: 'journal_task_start', task: {id: event.task_id, session: event.session_id, callID: event.tool_use_id}});
        } else if (event.subtype === 'task_notification' && terminal(event.status)) {
          await send({operation: 'task', task: {id: event.task_id, session: event.session_id, status: event.status,
            ...(event.tool_use_id ? {callID: event.tool_use_id} : {}), report: report(event.summary)}});
        } else if (event.subtype === 'task_updated') {
          // Native TaskStop emits killed here and stopped in task_notification.
          // Either terminal form must settle the same original observation.
          const status = event.patch.status === 'killed' ? 'stopped' : event.patch.status;
          if (!status || !terminal(status)) return;
          await send({operation: 'task', task: {id: event.task_id, session: event.session_id, status}});
        }
      } catch (error) { void notice(`Companion capture unavailable: ${String(error)}`).catch(() => {}); }
    },
  };
}
