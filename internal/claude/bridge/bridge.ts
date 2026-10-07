import { query, getSessionInfo, renameSession, type SDKSessionInfo, type SDKUserMessage, type Query, type EffortLevel } from '@anthropic-ai/claude-agent-sdk';
import { createInterface } from 'node:readline';
import { once } from 'node:events';
import { randomUUID } from 'node:crypto';
import { companionRequest } from './companion_transport.js';
import { realpath, stat } from 'node:fs/promises';
import { companion, type CompanionConfig } from './companion.js';
import { journalServer } from './journal.js';
import { companionGuidance, companionFrontendGuidance } from './guidance.js';
import { readFileSync, closeSync } from 'node:fs';
import { setSettings, type SettingsCommand } from './controls.js';
import { taskOutput } from './task_output.js';
import {sessionPage} from './session_controls.js';
import {sessionHistory, type HistorySelection} from './session_history.js';
import {SideQueries} from './side_query.js';
import {AgentMessages, type AgentMessage} from './agent_messages.js';
import {UserShell, type NativeInput} from './user_shell.js';
import {PermissionRequests} from './permissions.js';

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
const inputs: NativeInput[] = [];
const permissions = new PermissionRequests(emit, stop);
let controls = Promise.resolve();
let epoch = 0;
let pendingTurns = 0;
let replacing = false;
let resetCancelled = false;
let pendingReset: {id: string; session: string} | undefined;
let model = config.model;
let resume = config.resume;
let cwd = config.cwd;
let forkSession = config.forkSession;
let activeSession = config.forkSession ? '' : config.resume ?? '';
let forkHistory: {source: string; children: HistorySelection[]} | undefined;
let effort: SettingsCommand | undefined;
let background = new Set<string>();
let running!: Query;
let agents: AgentMessages | undefined;
let pendingAgentMessages = 0;
let shell: UserShell | undefined;
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
const sides = new SideQueries(emit);
async function* messages(generation: number): AsyncGenerator<SDKUserMessage> {
  while (!stopping && generation === epoch) {
    const message = inputs.shift();
    if (message) yield message as SDKUserMessage;
    else await new Promise<void>(resolve => { wake = resolve; });
  }
}
let observer: ReturnType<typeof companion> | undefined;
async function createQuery(fresh?: {session: string; context: string}): Promise<Query> {
  agents = await AgentMessages.create();
  shell = new UserShell(input => {inputs.push(input); wake?.(); wake = undefined;}, async frame => {
    if (frame.kind === 'shell_started') activeSession = frame.sessionID;
    await emit({...frame, cwd});
  });
  const guidance = endpoint ? companionGuidance(endpoint) : '';
  const frontendChunks = endpoint ? companionFrontendGuidance(endpoint) : [];
  const frontends = frontendChunks.join('');
  observer = endpoint ? companion(endpoint, cwd, text => emit({kind: 'notice', text}), Boolean(resume) && !fresh,
    {workflow: guidance, frontends: frontendChunks}) : undefined;
  const journal = endpoint ? journalServer(endpoint) : undefined;
  // Fresh sessions pin additive workflow guidance with Claude's native preset.
  // Resume keeps that recorded prompt; its first input hook supplies current text.
  const append = [!resume || fresh ? guidance : '', !resume || fresh ? frontends : '', fresh?.context ?? ''].filter(Boolean).join('\n\n');
  return query({prompt: messages(epoch), options: {
  cwd,
  pathToClaudeCodeExecutable: config.executable,
  systemPrompt: {type: 'preset', preset: 'claude_code', ...(append ? {append} : {})},
  settingSources: ['user', 'project', 'local'],
  includePartialMessages: true,
  forwardSubagentText: true,
  extraArgs: {'replay-user-messages': null},
  ...(fresh ? {sessionId: fresh.session} : resume ? {resume} : {}),
  ...(!fresh && forkSession ? {forkSession: true} : {}),
  ...(model ? {model} : {}),
  abortController,
  ...(observer ? {hooks: fresh ? {...observer.hooks, UserPromptSubmit: []} : observer.hooks} : {}),
  plugins: [ ...(endpoint?.plugin ? [{type: 'local' as const, path: endpoint.plugin}] : []), {type: 'local', path: agents.plugin}],
  ...(endpoint?.frontendDirectory || endpoint?.bashEnv ? {env: {...process.env,
    ...(endpoint.frontendDirectory ? {PATH: `${endpoint.frontendDirectory}:${process.env.PATH ?? ''}`} : {}),
    ...(endpoint.bashEnv ? {BASH_ENV: endpoint.bashEnv} : {})}} : {}),
  ...(journal ? {mcpServers: {mekugi: journal}} : {}),
  canUseTool: permissions.canUseTool,
}});
}

async function watch(query: Query): Promise<void> {
  const queryObserver = observer;
  const queryAgents = agents;
  const queryShell = shell;
  const output = taskOutput(query, emit, text => emit({kind: 'notice', text}));
  try {
    for await (const event of query) {
      if (event.type === 'tool_progress' || event.type === 'system' && event.subtype === 'task_started') {
        if (event.tool_use_id) permissions.settle(event.tool_use_id);
      }
      if (event.type === 'user' && Array.isArray(event.message.content)) {
        for (const block of event.message.content) {
          if (block.type === 'tool_result') permissions.settle(block.tool_use_id);
        }
      }
      if (await queryShell?.event(event)) continue;
      if (event.type === 'system' && event.subtype === 'init') activeSession = event.session_id;
      if (event.type === 'result') pendingTurns = Math.max(0, pendingTurns - 1);
      if (event.type === 'system' && event.subtype === 'background_tasks_changed') background = new Set(event.tasks.map(task => task.task_id));
      await queryObserver?.event(event);
      if (forkHistory && event.type === 'system' && event.subtype === 'init') {
        const selection = forkHistory;
        forkHistory = undefined;
        if (endpoint) {
          try {
            await companionRequest(endpoint, {operation: 'history_fork', binding: {runtime: 'claude', workspace: cwd, session: event.session_id},
              source: {runtime: 'claude', workspace: cwd, session: selection.source}, history: selection.children});
          } catch (error) { await emit({kind: 'notice', text: `Native fork child history could not be retained: ${String(error)}`}); }
        }
      }
      await output.event(event);
      if (pendingReset && event.type === 'system' && event.subtype === 'init') {
        if (event.session_id !== pendingReset.session) throw new Error('Reset native session identity mismatch');
        await companionRequest(endpoint!, {operation: 'journal_reset_installed', binding: {runtime: 'claude', workspace: cwd, session: event.session_id}});
        await emit({kind: 'reset', id: pendingReset.id, sessionID: event.session_id});
        pendingReset = undefined;
      }
      await emit({kind: 'event', event});
    }
    queryShell?.queryEnded(new Error('Native query ended before shell cancellation evidence'));
    if (!stopping && !replacing) throw new Error('Native query ended unexpectedly');
  } catch (error) {
    queryShell?.queryEnded(error);
    if (!stopping && !replacing) { await emit({kind: 'error', text: String(error)}); stop(); }
  } finally {output.close(); permissions.close(); await queryAgents?.close();}
}

async function reset(id: string): Promise<void> {
  if (!endpoint || pendingTurns || inputs.length || permissions.size || background.size || pendingAgentMessages || shell?.pending || replacing || pendingReset) {
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
    await sides.closeAll();
    epoch++;
    running.close();
    wake?.(); wake = undefined;
    retired = true;
    await pump;
    await emit({kind: 'notice', text: 'Native query drained; preparing retained journal scope'});
    if (stopping || resetCancelled) throw new Error('Journal reset cancelled after query shutdown; resume manually');
    const session = randomUUID();
    const packet = await companionRequest(endpoint, {operation: 'journal_reset', binding: {runtime: 'claude', workspace: cwd, session}}, undefined, 128 * 1024) as {text: string};
    if (typeof packet.text !== 'string' || !packet.text || packet.text.length > 10000) throw new Error('Invalid journal reset packet');
    if (stopping || resetCancelled) throw new Error('Journal reset cancelled before new query initialization; resume manually');
    pendingReset = {id, session};
    activeSession = session;
    running = await createQuery({session, context: packet.text});
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

async function changeSession(id: string, target?: string, workspaceHint?: string): Promise<void> {
  if (pendingTurns || inputs.length || permissions.size || background.size || pendingAgentMessages || shell?.pending || replacing || pendingReset) {
    await emit({kind: 'session_ready', id, failed: true, text: 'Session switching requires idle native work and no queued input, permissions or background tasks'});
    return;
  }
  replacing = true;
  resetCancelled = false;
  let retired = false;
  try {
    const info = target ? await resumeInfo(target, workspaceHint) : undefined;
    const workspace = info?.cwd ?? cwd;
    const history = target ? await sessionHistory(target, workspace, endpoint) : {messages: [], notices: [], agents: []};
    const next = target ?? randomUUID();
    const source = {runtime: 'claude', workspace: cwd, session: activeSession};
    const binding = {...source, workspace, session: next};
    if (endpoint) await companionRequest(endpoint, {operation: 'session_switch_check', source, binding});
    if (stopping || resetCancelled) throw new Error('Session switch cancelled');
    await sides.closeAll();
    epoch++;
    running.close();
    wake?.(); wake = undefined;
    retired = true;
    await pump;
    if (stopping || resetCancelled) throw new Error('Session switch cancelled after query shutdown; resume manually');
    if (endpoint) {
      const presentation = await companionRequest(endpoint, {operation: 'session_switch', source, binding}) as Pick<CompanionConfig, 'plugin' | 'frontendDirectory'>;
      endpoint = {...endpoint, ...presentation};
    }
    cwd = workspace;
    resume = target;
    forkSession = false;
    activeSession = next;
    running = await createQuery(target ? undefined : {session: next, context: ''});
    await emit({kind: 'session_change', id, sessionID: target ?? '', cwd, title: info?.customTitle ?? info?.summary ?? ''});
    for (const agent of history.agents) await emit({kind: 'saved_agent', ...agent});
    for (const message of history.messages) await emit({kind: 'history', event: message});
    for (const text of history.notices) await emit({kind: 'notice', text});
    pump = watch(running);
    const commands = await running.supportedCommands();
    if (effort) {
      const receipt = await setSettings(running, effort);
      if (receipt.failed) throw new Error(receipt.text);
    }
    replacing = false;
    await emit({kind: 'commands', commandInfo: commands});
    await emit({kind: 'session_ready', id});
  } catch (error) {
    if (retired) {
      await emit({kind: 'error', text: `Session switch did not establish a new query; resume manually: ${String(error)}`});
      stop();
    } else {
      await emit({kind: 'session_ready', id, failed: true, text: String(error)});
    }
  } finally { replacing = false; }
}

async function resumeInfo(session: string, workspaceHint?: string): Promise<SDKSessionInfo> {
  const info = await getSessionInfo(session, workspaceHint ? {dir: workspaceHint} : undefined);
  if (!info || info.sessionId !== session || !info.cwd) {
    throw new Error('Native resume session metadata or workspace is unavailable');
  }
  const workspace = await realpath(info.cwd);
  if (workspaceHint && await realpath(workspaceHint) !== workspace) throw new Error('Native resume metadata differs from the selected workspace');
  if (!(await stat(workspace)).isDirectory()) throw new Error('Native resume workspace is not a directory');
  const local = workspaceHint === workspace ? info : await getSessionInfo(session, {dir: workspace});
  if (!local || local.sessionId !== session || !local.cwd || await realpath(local.cwd) !== workspace) {
    throw new Error('Native resume metadata does not match its workspace');
  }
  return {...local, cwd: workspace};
}

async function interruptShell(): Promise<void> {
  if (replacing) return;
  const terminal = shell?.shutdown();
  if (!terminal) return; // Execution ended already; let native history finish.
  replacing = true;
  try {
    await emit({kind: 'shell_restarting', text: 'Native shutdown cancels all work in this query; resuming the same session'});
    const receipt = await terminal;
    epoch++;
    wake?.(); wake = undefined;
    running.close();
    await pump;
    // Resume only confirmed history. Shutdown discards queued native work.
    inputs.length = 0;
    pendingTurns = 0;
    background.clear();
    const info = await getSessionInfo(receipt.session, {dir: cwd});
    resume = info ? receipt.session : undefined;
    forkSession = false;
    activeSession = receipt.session;
    running = await createQuery(info ? undefined : {session: receipt.session, context: ''});
    shell!.retain(receipt);
    pump = watch(running);
    const commands = await running.supportedCommands();
    if (effort) {
      const result = await setSettings(running, effort);
      if (result.failed) throw new Error(result.text);
    }
    replacing = false;
    await emit({kind: 'shell_restarted', commandInfo: commands});
  } catch (error) {
    await emit({kind: 'error', text: `Native shell cancellation did not establish resumed context; resume manually: ${String(error)}`});
    stop();
  } finally {replacing = false;}
}

function stop(): void {
  if (stopping) return;
  stopping = true;
  abortController.abort();
  permissions.close();
  wake?.();
  stopped();
}
const lines = createInterface({input: process.stdin, crlfDelay: Infinity});
// Go also bounds outgoing frames. Reject oversized input before parsing.
lines.on('line', (line: string) => {
  if (Buffer.byteLength(line) > limit) { stop(); return; }
  try {
    const command = JSON.parse(line) as {kind: string; text?: string; content?: SDKUserMessage['message']['content']; id?: string; agentID?: string; allow?: boolean; answers?: Record<string, string>; sessionID?: string; title?: string; cwd?: string; cursor?: string; limit?: number};
    switch (command.kind) {
      case 'shell':
        if (replacing || pendingReset || !shell) {
          void emit({kind: 'shell_done', id: command.id, sessionID: command.sessionID, failed: true, text: 'Native session is not ready; command not executed'}).catch(stop);
        } else {
          void shell.start({id: command.id ?? '', sessionID: command.sessionID ?? '', text: command.text ?? ''}, cwd, activeSession).catch(stop);
        }
        break;
      case 'agent_message': {
        const message: AgentMessage = {id: command.id ?? '', sessionID: command.sessionID ?? '', agentID: command.agentID ?? '', text: command.text ?? ''};
        if (replacing || !agents) {void emit({...message, kind: 'agent_message', failed: true, text: 'Native session is not ready'}).catch(stop); break;}
        pendingAgentMessages++;
        void agents.send(message, cwd, activeSession)
          .catch(error => ({...message, kind: 'agent_message' as const, failed: true, text: String(error)}))
          .then(emit).catch(stop).finally(() => {pendingAgentMessages--;});
        break;
      }
      case 'side_input':
        controls = controls.then(async () => {
          if (replacing || !activeSession || command.sessionID !== activeSession) {
            await emit({kind: 'side', id: command.id, frame: {kind: 'error', text: 'Main session is not ready for a native snapshot'}});
            return;
          }
          await sides.input({id: command.id ?? '', source: activeSession, content: command.content ?? []},
            {cwd, executable: config.executable, model, ...(effort && effort.value !== 'default' ? {effort: effort.value as EffortLevel} : {})});
        }).catch(stop);
        break;
      case 'side_close':
        controls = controls.then(() => sides.close(command.id ?? '')).catch(stop);
        break;
      case 'input':
        if (replacing) throw new Error('Input arrived during journal reset');
        if (shell?.pending) throw new Error('Input arrived before native shell history completion');
        if (pendingTurns >= 16 || typeof command.text !== 'string' && !Array.isArray(command.content)) throw new Error('Invalid or excessive pending input');
        pendingTurns++;
        inputs.push({type: 'user', message: {role: 'user', content: command.content ?? command.text!}, parent_tool_use_id: null, origin: {kind: 'human'}});
        wake?.(); wake = undefined;
        break;
      case 'decision': {
        permissions.respond(command.id ?? '', command.allow ? {behavior: 'allow', ...(command.answers ? {updatedInput: {answers: command.answers}} : {})}
          : {behavior: 'deny', message: 'Denied by the user'});
        break;
      }
      case 'reset':
        if (!command.id) throw new Error('Missing reset request ID');
        controls = controls.then(() => reset(command.id!)).catch(stop);
        break;
      case 'title':
        if (!command.id || !command.sessionID || !command.title?.trim()) throw new Error('Invalid session title request');
        controls = controls.then(async () => {
          try {
            await renameSession(command.sessionID!, command.title!, {dir: cwd});
            await emit({kind: 'title', id: command.id, sessionID: command.sessionID, title: command.title});
          } catch (error) {
            await emit({kind: 'title', id: command.id, sessionID: command.sessionID, title: command.title, failed: true, text: String(error)});
          }
        }).catch(stop);
        break;
      case 'sessions':
        if (!command.id || command.limit === undefined) throw new Error('Missing session list request');
        controls = controls.then(async () => {
          try { await emit(await sessionPage({id: command.id!, cwd: command.cwd, cursor: command.cursor, limit: command.limit!})); }
          catch (error) { await emit({kind: 'sessions', id: command.id, failed: true, text: String(error)}); }
        }).catch(stop);
        break;
      case 'session_change':
        if (!command.id) throw new Error('Missing session transition request');
        controls = controls.then(() => changeSession(command.id!, command.sessionID || undefined, command.cwd || undefined)).catch(stop);
        break;
      case 'interrupt':
        if (replacing) { resetCancelled = true; break; }
        if (shell?.pending) {
          controls = controls.then(interruptShell).catch(stop);
        } else {
          void running.interrupt().catch(async error => { await emit({kind: 'notice', text: `Interrupt failed: ${String(error)}`}); });
        }
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
    const info = await resumeInfo(config.resume);
    if (info.cwd !== await realpath(cwd)) {
      if (config.forkSession) throw new Error('Launch a native fork from its verified session workspace');
      if (endpoint) {
        const source = {runtime: 'claude', workspace: cwd, session: ''};
        const binding = {...source, workspace: info.cwd!, session: info.sessionId};
        await companionRequest(endpoint, {operation: 'session_switch_check', source, binding});
        const presentation = await companionRequest(endpoint, {operation: 'session_switch', source, binding}) as Pick<CompanionConfig, 'plugin' | 'frontendDirectory'>;
        endpoint = {...endpoint, ...presentation};
      }
    }
    cwd = info.cwd!;
    // Verified native history establishes an ordinary resumed session before
    // the first prompt. A fork must wait for its newly assigned native identity.
    if (!config.forkSession) {
      observer = endpoint ? companion(endpoint, cwd, text => emit({kind: 'notice', text}), true) : undefined;
      await observer?.resume(info);
      await emit({kind: 'session', sessionID: info.sessionId, cwd, title: info.customTitle ?? info.summary});
    }
    const history = await sessionHistory(config.resume, cwd, endpoint);
    if (config.forkSession) forkHistory = {source: config.resume, children: history.children};
    for (const agent of history.agents) await emit({kind: 'saved_agent', ...agent});
    for (const message of history.messages) {
      await emit({kind: 'history', event: message});
    }
    for (const text of history.notices) await emit({kind: 'notice', text});
  }
  running = await createQuery();
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
  await sides.closeAll();
  running?.close();
  await agents?.close();
  lines.close();
}
