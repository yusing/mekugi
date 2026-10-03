import { companionRequest, type CompanionConfig } from './companion_transport.js';
export type { CompanionConfig } from './companion_transport.js';
import { realpath } from 'node:fs/promises';
import type { HookCallback, HookInput, Options, SDKMessage, SDKSessionInfo } from '@anthropic-ai/claude-agent-sdk';

interface Binding { runtime: 'claude'; session: string; workspace: string; agent?: string }
interface NativeCall { binding: Binding; id: string; tool: string; input: string; paths?: string[]; command?: string }
const terminal = (status: string): boolean => ['completed', 'failed', 'stopped'].includes(status);
const object = (value: unknown): Record<string, unknown> | undefined =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined;

// Only the bridge holds this capability. Hooks add no permissions, substitutions,
// tool-result edits or model context, including when observation is unavailable.
export function companion(config: CompanionConfig, cwd: string, notice: (text: string) => Promise<void>): {
  hooks: Options['hooks']; event: (event: SDKMessage) => Promise<void>;
  resume: (info: SDKSessionInfo) => Promise<void>;
} {
  const send = (payload: unknown, signal?: AbortSignal): Promise<unknown> => companionRequest(config, payload, signal);
  const binding = async (input: Pick<HookInput, 'session_id' | 'cwd' | 'agent_id'>): Promise<Binding> => {
    if (await realpath(input.cwd) !== cwd) throw new Error('native hook workspace differs from this launch');
    return {runtime: 'claude', session: input.session_id, workspace: cwd, ...(input.agent_id ? {agent: input.agent_id} : {})};
  };
  const report = (value: unknown): string => {
    const text = typeof value === 'string' ? value : JSON.stringify(value) ?? '';
    return text.slice(0, 32768);
  };
  const hook: HookCallback = async (input, toolUseID, options) => {
    try {
      const scope = await binding(input);
      if (input.hook_event_name === 'SessionStart' || input.hook_event_name === 'SubagentStart') {
        await send({operation: 'bind', binding: scope}, options.signal);
        if (input.hook_event_name === 'SessionStart' && input.source === 'compact' && config.journalSchema) {
          const result = await companionRequest(config, {operation: 'journal_recovery', binding: scope}, options.signal, 128 * 1024) as {text?: string};
          if (typeof result.text === 'string' && result.text.length <= 10000) {
            await notice(`Journal recovery prepared (${result.text.length} characters); native summary preserved`);
            return {hookSpecificOutput: {hookEventName: 'SessionStart', additionalContext: result.text}};
          }
          throw new Error('Native journal recovery carrier unavailable');
        }
        return {};
      }
      if (input.hook_event_name !== 'PreToolUse' && input.hook_event_name !== 'PostToolUse' && input.hook_event_name !== 'PostToolUseFailure') return {};
      if (toolUseID && toolUseID !== input.tool_use_id) throw new Error('native hook tool IDs disagree');
      if (config.journalSchema && input.hook_event_name === 'PreToolUse' && ['Agent', 'Task'].includes(input.tool_name)) {
        await send({operation: 'journal_before', call: {binding: scope, id: input.tool_use_id, tool: input.tool_name, input: JSON.stringify(input.tool_input)}}, options.signal);
        return {};
      }
      if (config.journalSchema && input.hook_event_name === 'PreToolUse' && ['mcp__mekugi__journal_batch', 'mcp__mekugi__journal_read'].includes(input.tool_name)) {
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
      if (input.hook_event_name === 'PreToolUse') await send({operation: 'before', call}, options.signal);
      else if (input.hook_event_name === 'PostToolUseFailure') {
        const result = await send({operation: 'after', call, terminal: {status: input.is_interrupt ? 'stopped' : 'failed', report: input.error, interrupted: input.is_interrupt ?? false}}, options.signal) as {changeID?: string};
        if (config.frontendDirectory && result.changeID) return {hookSpecificOutput: {hookEventName: 'PostToolUseFailure', additionalContext: `Mekugi recorded actual file effects: ${result.changeID}. Review this explicit ID with mchanges through native Bash.`}};
      }
      else {
        const response = object(input.tool_response);
        const task = typeof response?.backgroundTaskId === 'string' ? response.backgroundTaskId : undefined;
        if (args.run_in_background === true && !task) throw new Error('background Bash completion identity unavailable; capture left unfinished');
        const result = await send({operation: 'after', call, terminal: {status: task ? 'running' : 'completed', ...(task ? {task} : {}), report: report(input.tool_response)}}, options.signal) as {changeID?: string};
        if (config.frontendDirectory && result.changeID) return {hookSpecificOutput: {hookEventName: 'PostToolUse', additionalContext: `Mekugi recorded actual file effects: ${result.changeID}. Review this explicit ID with mchanges through native Bash.`}};
      }
    } catch (error) {
      void notice(`Companion capture unavailable: ${String(error)}`).catch(() => {});
    }
    return {};
  };
  const matcher = {hooks: [hook], timeout: 6};
  return {
    resume: async info => {
      try {
        if (!info.cwd) throw new Error('native resume workspace unavailable');
        await send({operation: 'bind', binding: await binding({session_id: info.sessionId, cwd: info.cwd})});
      } catch (error) { void notice(`Companion capture unavailable: ${String(error)}`).catch(() => {}); }
    },
    hooks: {SessionStart: [matcher], SubagentStart: [matcher], PreToolUse: [{...matcher, matcher: config.journalSchema ? 'Edit|Write|Bash|Agent|Task|mcp__mekugi__journal_batch|mcp__mekugi__journal_read' : 'Edit|Write|Bash'}],
      PostToolUse: [{...matcher, matcher: 'Edit|Write|Bash'}], PostToolUseFailure: [{...matcher, matcher: 'Edit|Write|Bash'}]},
    event: async event => {
      try {
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
