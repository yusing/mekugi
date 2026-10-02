import { query, getSessionInfo, getSessionMessages, type SDKUserMessage, type PermissionResult } from '@anthropic-ai/claude-agent-sdk';
import { createInterface } from 'node:readline';
import { once } from 'node:events';
import { realpath } from 'node:fs/promises';
import { companion, type CompanionConfig } from './companion.js';
import { readFileSync, closeSync } from 'node:fs';
import { setSettings, type SettingsCommand } from './controls.js';

const limit = 8 * 1024 * 1024;
const config = JSON.parse(process.argv[2]) as {cwd: string; executable: string; resume?: string; model?: string; companionFD?: number};
let endpoint: CompanionConfig | undefined;
if (config.companionFD !== undefined) {
  try { endpoint = JSON.parse(readFileSync(config.companionFD, 'utf8')) as CompanionConfig; }
  finally { closeSync(config.companionFD); }
}
const abortController = new AbortController();
let stopping = false;
let wake: (() => void) | undefined;
const inputs: SDKUserMessage[] = [];
const permissions = new Map<string, (result: PermissionResult) => void>();
let serial = 0;
let controls = Promise.resolve();

// Bound frames without transforming native tool inputs/results. An oversized
// event terminates this presentation connection rather than fabricating evidence.
async function emit(value: unknown): Promise<void> {
  const frame = JSON.stringify(value) + '\n';
  if (Buffer.byteLength(frame) > limit) throw new Error('Claude event exceeds the 8 MiB bridge frame limit');
  if (!process.stdout.write(frame)) await once(process.stdout, 'drain');
}
async function* messages(): AsyncGenerator<SDKUserMessage> {
  while (!stopping) {
    const message = inputs.shift();
    if (message) yield message;
    else await new Promise<void>(resolve => { wake = resolve; });
  }
}
const observer = endpoint ? companion(endpoint, config.cwd, text => emit({kind: 'notice', text})) : undefined;
const running = query({prompt: messages(), options: {
  cwd: config.cwd,
  pathToClaudeCodeExecutable: config.executable,
  systemPrompt: {type: 'preset', preset: 'claude_code'},
  settingSources: ['user', 'project', 'local'],
  includePartialMessages: true,
  ...(config.resume ? {resume: config.resume} : {}),
  ...(config.model ? {model: config.model} : {}),
  abortController,
  ...(observer ? {hooks: observer.hooks} : {}),
  canUseTool: async (tool, input, options) => {
    const id = String(++serial);
    return new Promise<PermissionResult>((resolve) => {
      const finish = (result: PermissionResult): void => {
        permissions.delete(id);
        options.signal.removeEventListener('abort', cancelled);
        resolve(result);
      };
      const cancelled = (): void => {
        finish({behavior: 'deny', message: 'Native permission request cancelled'});
        void emit({kind: 'permission_cancelled', id}).catch(stop);
      };
      permissions.set(id, result => finish(result.behavior === 'allow' && result.updatedInput ? {...result, updatedInput: {...input, ...result.updatedInput}} : result));
      options.signal.addEventListener('abort', cancelled, {once: true});
      if (options.signal.aborted) { cancelled(); return; }
      void emit({kind: 'permission', id, tool, input, toolUseID: options.toolUseID,
        description: options.description ?? options.title ?? ''}).catch(stop);
    });
  },
}});

function stop(): void {
  if (stopping) return;
  stopping = true;
  abortController.abort();
  for (const finish of permissions.values()) finish({behavior: 'deny', message: 'Client closed'});
  wake?.();
}
const lines = createInterface({input: process.stdin, crlfDelay: Infinity});
// Go also bounds outgoing frames. Reject oversized input before parsing.
lines.on('line', (line: string) => {
  if (Buffer.byteLength(line) > limit) { stop(); return; }
  try {
    const command = JSON.parse(line) as {kind: string; text?: string; id?: string; allow?: boolean; answers?: Record<string, string>};
    switch (command.kind) {
      case 'input':
        if (inputs.length >= 16 || typeof command.text !== 'string') throw new Error('Invalid or excessive pending input');
        inputs.push({type: 'user', message: {role: 'user', content: command.text}, parent_tool_use_id: null, origin: {kind: 'human'}});
        wake?.(); wake = undefined;
        break;
      case 'decision': {
        const finish = permissions.get(command.id ?? '');
        if (!finish) break; // A native cancellation can race the user's decision.
        finish(command.allow ? {behavior: 'allow', ...(command.answers ? {updatedInput: {answers: command.answers}} : {})}
          : {behavior: 'deny', message: 'Denied by the user'});
        break;
      }
      case 'interrupt':
        void running.interrupt().catch(async error => { await emit({kind: 'notice', text: `Interrupt failed: ${String(error)}`}); });
        break;
      case 'settings':
        controls = controls.then(async () => { await emit(await setSettings(running, command as SettingsCommand)); }).catch(stop);
        break;
      case 'close': stop(); break;
      default: throw new Error('Unknown bridge control');
    }
  } catch (error) { void emit({kind: 'error', text: String(error)}).finally(stop); }
});
lines.on('close', stop);
process.on('SIGTERM', stop);
process.on('SIGINT', stop);
try {
  if (config.resume) {
    const info = await getSessionInfo(config.resume, {dir: config.cwd});
    if (!info || !info.cwd || await realpath(info.cwd) !== await realpath(config.cwd)) {
      throw new Error('Resume session does not belong to the selected workspace');
    }
    const history = await getSessionMessages(config.resume, {dir: config.cwd, limit: 2001});
    for (const message of history.slice(0, 2000)) {
      await emit({kind: 'history', event: message});
    }
    if (history.length > 2000) await emit({kind: 'notice', text: 'Transcript display limited to the first 2000 messages; native resume retains its own context'});
  }
  const commands = await running.supportedCommands();
  const models = await running.supportedModels().catch(async error => {
    await emit({kind: 'notice', text: `Native model choices unavailable: ${String(error)}`});
    return [];
  });
  await emit({kind: 'ready', commands: commands.flatMap(command => [command.name, ...(command.aliases ?? [])]), models});
  for await (const event of running) {
    await observer?.event(event);
    await emit({kind: 'event', event});
  }
} catch (error) {
  if (!stopping) { await emit({kind: 'error', text: String(error)}); process.exitCode = 1; }
} finally {
  stop();
  running.close();
  lines.close();
}
