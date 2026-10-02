import { request } from 'node:http';
import { realpath } from 'node:fs/promises';
import type { HookCallback, HookInput, Options, SDKMessage } from '@anthropic-ai/claude-agent-sdk';

export interface CompanionConfig { socket: string; token: string }
interface Binding { runtime: 'claude'; session: string; workspace: string; agent?: string }
interface NativeCall { binding: Binding; id: string; tool: string; input: string; paths?: string[]; command?: string }
const terminal = (status: string): boolean => ['completed', 'failed', 'stopped'].includes(status);
const object = (value: unknown): Record<string, unknown> | undefined =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined;

// Only the bridge holds this capability. Hooks add no permissions, substitutions,
// tool-result edits or model context, including when observation is unavailable.
export function companion(config: CompanionConfig, cwd: string, notice: (text: string) => Promise<void>): {
  hooks: Options['hooks']; event: (event: SDKMessage) => Promise<void>;
} {
  const send = (payload: unknown, signal?: AbortSignal): Promise<void> => new Promise((resolve, reject) => {
    const data = JSON.stringify(payload);
    if (Buffer.byteLength(data) > 8 * 1024 * 1024) { reject(new Error('observation exceeds frame limit')); return; }
    const req = request({socketPath: config.socket, path: '/observe', method: 'POST', signal,
      headers: {'Authorization': `Bearer ${config.token}`, 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data)}}, response => {
      let bytes = 0; let message = '';
      response.on('data', (chunk: Buffer) => { bytes += chunk.length; if (bytes > 8192) req.destroy(new Error('oversized observation response')); else message += chunk.toString(); });
      response.on('end', () => response.statusCode === 200 ? resolve() : reject(new Error(`observation rejected (${response.statusCode}): ${message.trim()}`)));
      response.on('error', reject);
    });
    req.setTimeout(4000, () => req.destroy(new Error('observation timeout')));
    req.on('error', reject);
    req.end(data);
  });
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
        return {};
      }
      if (input.hook_event_name !== 'PreToolUse' && input.hook_event_name !== 'PostToolUse' && input.hook_event_name !== 'PostToolUseFailure') return {};
      if (toolUseID && toolUseID !== input.tool_use_id) throw new Error('native hook tool IDs disagree');
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
      else if (input.hook_event_name === 'PostToolUseFailure') await send({operation: 'after', call,
        terminal: {status: input.is_interrupt ? 'stopped' : 'failed', report: input.error, interrupted: input.is_interrupt ?? false}}, options.signal);
      else {
        const response = object(input.tool_response);
        const task = typeof response?.backgroundTaskId === 'string' ? response.backgroundTaskId : undefined;
        if (args.run_in_background === true && !task) throw new Error('background Bash completion identity unavailable; capture left unfinished');
        await send({operation: 'after', call, terminal: {status: task ? 'running' : 'completed', ...(task ? {task} : {}), report: report(input.tool_response)}}, options.signal);
      }
    } catch (error) {
      void notice(`Companion capture unavailable: ${String(error)}`).catch(() => {});
    }
    return {};
  };
  const matcher = {hooks: [hook], timeout: 6};
  return {
    hooks: {SessionStart: [matcher], SubagentStart: [matcher], PreToolUse: [{...matcher, matcher: 'Edit|Write|Bash'}],
      PostToolUse: [{...matcher, matcher: 'Edit|Write|Bash'}], PostToolUseFailure: [{...matcher, matcher: 'Edit|Write|Bash'}]},
    event: async event => {
      try {
        if (event.type !== 'system') return;
        if (event.subtype === 'init') {
          await send({operation: 'bind', binding: await binding({session_id: event.session_id, cwd: event.cwd})});
        } else if (event.subtype === 'task_notification' && terminal(event.status)) {
          await send({operation: 'task', task: {id: event.task_id, session: event.session_id, status: event.status,
            ...(event.tool_use_id ? {callID: event.tool_use_id} : {}), report: report(event.summary)}});
        } else if (event.subtype === 'task_updated' && event.patch.status && terminal(event.patch.status)) {
          await send({operation: 'task', task: {id: event.task_id, session: event.session_id, status: event.patch.status}});
        }
      } catch (error) { void notice(`Companion capture unavailable: ${String(error)}`).catch(() => {}); }
    },
  };
}
