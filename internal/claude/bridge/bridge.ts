import { query, getSessionInfo, getSessionMessages, type SDKUserMessage, type PermissionResult, type Query } from '@anthropic-ai/claude-agent-sdk';
import { createInterface } from 'node:readline';
import { once } from 'node:events';
import { randomUUID } from 'node:crypto';
import { companionRequest } from './companion_transport.js';
import { realpath } from 'node:fs/promises';
import { companion, type CompanionConfig } from './companion.js';
import { journalServer } from './journal.js';
import { readFileSync, closeSync } from 'node:fs';
import { setSettings, type SettingsCommand } from './controls.js';

const limit = 8 * 1024 * 1024;
const config = JSON.parse(process.argv[2]) as {cwd: string; executable: string; resume?: string; forkSession?: boolean; model?: string; companionFD?: number};
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
let epoch = 0;
let pendingTurns = 0;
let replacing = false;
let resetCancelled = false;
let pendingReset: {id: string; session: string} | undefined;
let model = config.model;
let effort: SettingsCommand | undefined;
let background = new Set<string>();
let running!: Query;
let pump = Promise.resolve();
let stopped!: () => void;
const closed = new Promise<void>(resolve => { stopped = resolve; });

// Bound frames without transforming native tool inputs/results. An oversized
// event terminates this presentation connection rather than fabricating evidence.
async function emit(value: unknown): Promise<void> {
  const frame = JSON.stringify(value) + '\n';
  if (Buffer.byteLength(frame) > limit) throw new Error('Claude event exceeds the 8 MiB bridge frame limit');
  if (!process.stdout.write(frame)) await once(process.stdout, 'drain');
}
async function* messages(generation: number): AsyncGenerator<SDKUserMessage> {
  while (!stopping && generation === epoch) {
    const message = inputs.shift();
    if (message) yield message;
    else await new Promise<void>(resolve => { wake = resolve; });
  }
}
const observer = endpoint ? companion(endpoint, config.cwd, text => emit({kind: 'notice', text})) : undefined;
function createQuery(fresh?: {session: string; context: string}): Query {
  const journal = endpoint ? journalServer(endpoint) : undefined;
  return query({prompt: messages(epoch), options: {
  cwd: config.cwd,
  pathToClaudeCodeExecutable: config.executable,
  systemPrompt: {type: 'preset', preset: 'claude_code', ...(fresh ? {append: fresh.context} : {})},
  settingSources: ['user', 'project', 'local'],
  includePartialMessages: true,
  ...(fresh ? {sessionId: fresh.session} : config.resume ? {resume: config.resume} : {}),
  ...(!fresh && config.forkSession ? {forkSession: true} : {}),
  ...(model ? {model} : {}),
  abortController,
  ...(observer ? {hooks: observer.hooks} : {}),
  ...(endpoint?.plugin ? {plugins: [{type: 'local' as const, path: endpoint.plugin}]} : {}),
  ...(endpoint?.frontendDirectory ? {env: {...process.env, PATH: `${endpoint.frontendDirectory}:${process.env.PATH ?? ''}`}} : {}),
  ...(journal ? {mcpServers: {mekugi: journal}} : {}),
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
}

async function watch(query: Query): Promise<void> {
  try {
    for await (const event of query) {
      if (event.type === 'result') pendingTurns = Math.max(0, pendingTurns - 1);
      if (event.type === 'system' && event.subtype === 'background_tasks_changed') background = new Set(event.tasks.map(task => task.task_id));
      await observer?.event(event);
      if (pendingReset && event.type === 'system' && event.subtype === 'init') {
        if (event.session_id !== pendingReset.session) throw new Error('Reset native session identity mismatch');
        await companionRequest(endpoint!, {operation: 'journal_reset_installed', binding: {runtime: 'claude', workspace: config.cwd, session: event.session_id}});
        await emit({kind: 'reset', id: pendingReset.id, sessionID: event.session_id});
        pendingReset = undefined;
      }
      await emit({kind: 'event', event});
    }
    if (!stopping && !replacing) throw new Error('Native query ended unexpectedly');
  } catch (error) {
    if (!stopping && !replacing) { await emit({kind: 'error', text: String(error)}); stop(); }
  }
}

async function reset(id: string): Promise<void> {
  if (!endpoint || pendingTurns || inputs.length || permissions.size || background.size || replacing || pendingReset) {
    await emit({kind: 'reset_ready', id, failed: true, text: 'Journal reset requires an idle query with no queued input, permissions or background tasks'});
    return;
  }
  replacing = true;
  resetCancelled = false;
  let retired = false;
  try {
    // Validate mandatory recovery before retiring the usable query.
    await companionRequest(endpoint, {operation: 'journal_reset_check'});
    await emit({kind: 'notice', text: 'Journal recovery validated; retiring idle native query'});
    if (stopping || resetCancelled) throw new Error('Journal reset cancelled');
    epoch++;
    running.close();
    wake?.(); wake = undefined;
    retired = true;
    await pump;
    await emit({kind: 'notice', text: 'Native query drained; preparing retained journal scope'});
    if (stopping || resetCancelled) throw new Error('Journal reset cancelled after query shutdown; resume manually');
    const session = randomUUID();
    const packet = await companionRequest(endpoint, {operation: 'journal_reset', binding: {runtime: 'claude', workspace: config.cwd, session}}, undefined, 128 * 1024) as {text: string};
    if (typeof packet.text !== 'string' || !packet.text || packet.text.length > 10000) throw new Error('Invalid journal reset packet');
    if (stopping || resetCancelled) throw new Error('Journal reset cancelled before new query initialization; resume manually');
    pendingReset = {id, session};
    running = createQuery({session, context: packet.text});
    pump = watch(running);
    await running.supportedCommands();
    if (effort) {
      const receipt = await setSettings(running, effort);
      if (receipt.failed) throw new Error(receipt.text);
    }
    replacing = false;
    await emit({kind: 'reset_ready', id});
  } catch (error) {
    if (retired) {
      await emit({kind: 'error', text: `Journal reset did not establish a new query; resume manually: ${String(error)}`});
      stop();
    } else {
      await emit({kind: 'reset_ready', id, failed: true, text: String(error)});
    }
  } finally { replacing = false; }
}

function stop(): void {
  if (stopping) return;
  stopping = true;
  abortController.abort();
  for (const finish of permissions.values()) finish({behavior: 'deny', message: 'Client closed'});
  wake?.();
  stopped();
}
const lines = createInterface({input: process.stdin, crlfDelay: Infinity});
// Go also bounds outgoing frames. Reject oversized input before parsing.
lines.on('line', (line: string) => {
  if (Buffer.byteLength(line) > limit) { stop(); return; }
  try {
    const command = JSON.parse(line) as {kind: string; text?: string; content?: SDKUserMessage['message']['content']; id?: string; allow?: boolean; answers?: Record<string, string>};
    switch (command.kind) {
      case 'input':
        if (replacing) throw new Error('Input arrived during journal reset');
        if (pendingTurns >= 16 || typeof command.text !== 'string' && !Array.isArray(command.content)) throw new Error('Invalid or excessive pending input');
        pendingTurns++;
        inputs.push({type: 'user', message: {role: 'user', content: command.content ?? command.text!}, parent_tool_use_id: null, origin: {kind: 'human'}});
        wake?.(); wake = undefined;
        break;
      case 'decision': {
        const finish = permissions.get(command.id ?? '');
        if (!finish) break; // A native cancellation can race the user's decision.
        finish(command.allow ? {behavior: 'allow', ...(command.answers ? {updatedInput: {answers: command.answers}} : {})}
          : {behavior: 'deny', message: 'Denied by the user'});
        break;
      }
      case 'reset':
        if (!command.id) throw new Error('Missing reset request ID');
        controls = controls.then(() => reset(command.id!)).catch(stop);
        break;
      case 'interrupt':
        if (replacing) { resetCancelled = true; break; }
        void running.interrupt().catch(async error => { await emit({kind: 'notice', text: `Interrupt failed: ${String(error)}`}); });
        break;
      case 'settings':
        controls = controls.then(async () => {
          const receipt = await setSettings(running, command as SettingsCommand);
          if (!receipt.failed) {
            if (receipt.field === 'model') model = receipt.value === 'default' ? undefined : receipt.value;
            else effort = command as SettingsCommand;
          }
          await emit(receipt);
        }).catch(stop);
        break;
      case 'stop_task':
        if (!command.id) throw new Error('Missing native task ID');
        void running.stopTask(command.id).then(() => emit({kind: 'task_control', id: command.id}))
          .catch(error => emit({kind: 'task_control', id: command.id, failed: true, text: String(error)})).catch(stop);
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
    if (!info || info.sessionId !== config.resume || !info.cwd || await realpath(info.cwd) !== await realpath(config.cwd)) {
      throw new Error('Resume session does not belong to the selected workspace');
    }
    // Verified native history establishes an ordinary resumed session before
    // the first prompt. A fork must wait for its newly assigned native identity.
    if (!config.forkSession) {
      await observer?.resume(info);
      await emit({kind: 'session', sessionID: info.sessionId});
    }
    const history = await getSessionMessages(config.resume, {dir: config.cwd, limit: 2001});
    for (const message of history.slice(0, 2000)) {
      await emit({kind: 'history', event: message});
    }
    if (history.length > 2000) await emit({kind: 'notice', text: 'Transcript display limited to the first 2000 messages; native resume retains its own context'});
  }
  running = createQuery();
  pump = watch(running);
  const commands = await running.supportedCommands();
  const models = await running.supportedModels().catch(async error => {
    await emit({kind: 'notice', text: `Native model choices unavailable: ${String(error)}`});
    return [];
  });
  await emit({kind: 'ready', commandInfo: commands, models});
  await closed;
} catch (error) {
  if (!stopping) { await emit({kind: 'error', text: String(error)}); process.exitCode = 1; }
} finally {
  stop();
  running?.close();
  lines.close();
}
