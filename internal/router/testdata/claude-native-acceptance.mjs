// Opt-in installed-runtime probe. The endpoint is received on stdin, never argv
// or Claude's environment. Only synthetic workspace payloads are retained.
import {createRequire} from 'node:module';
import {pathToFileURL} from 'node:url';
import {readFileSync} from 'node:fs';
const config = JSON.parse(readFileSync(0, 'utf8'));
const require = createRequire(pathToFileURL(config.bridge));
const {query, listSubagents, getSubagentMessages} = await import(pathToFileURL(require.resolve('@anthropic-ai/claude-agent-sdk')));
const {companion} = await import(new URL('./companion.js', pathToFileURL(config.bridge)));
const records = [];
const record = (kind, value) => records.push({kind, value});
const observer = config.endpoint ? companion(config.endpoint, config.cwd, async text => record('notice', text)) : undefined;
const hooks = {};
for (const name of ['SessionStart', 'SubagentStart', 'SubagentStop', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure']) {
  hooks[name] = [{hooks: [async (input, id, options) => {
    record('hook', input);
    const output = await observer?.hooks?.[name]?.[0]?.hooks[0](input, id, options) ?? {};
    record('hook_return', {event: name, id, output});
    return output;
  }]}];
}
const abortController = new AbortController();
const timeout = setTimeout(() => abortController.abort(), 90000);
// Streaming input keeps native canUseTool active. No permissionMode, credentials,
// provider endpoints, or settings files are replaced by this harness.
let release;
let grace;
let graceExpired = false;
let sessionID;
let rootFinished = false;
let notified = false;
async function* prompt() {
  yield {type: 'user', message: {role: 'user', content: config.prompt}, parent_tool_use_id: null};
  await new Promise(resolve => { release = resolve; });
}
const running = query({prompt: prompt(), options: {
  cwd: config.cwd, pathToClaudeCodeExecutable: config.executable,
  systemPrompt: {type: 'preset', preset: 'claude_code'},
  settingSources: ['user', 'project', 'local'], includePartialMessages: true,
  maxTurns: 4, abortController, hooks,
  ...(config.plugin ? {plugins: [{type: 'local', path: config.plugin}]} : {}),
  canUseTool: async (tool, input, options) => {
    const allow = !config.deny && config.tools.includes(tool);
    record('permission', {tool, input, tool_use_id: options.toolUseID, allow});
    return allow ? {behavior: 'allow'} : {behavior: 'deny', message: 'Denied by acceptance user; do not retry'};
  },
}});
try {
  for await (const event of running) {
    sessionID = event.session_id;
    await observer?.event(event);
    if (config.background && !notified && event.type === 'system' && event.subtype === 'task_updated' && ['completed', 'failed', 'killed'].includes(event.patch.status)) {
      grace ??= setTimeout(() => { graceExpired = true; abortController.abort(); }, 8000);
    }
    if (event.type === 'assistant' || event.type === 'user' || event.type === 'result' ||
        event.type === 'system' && event.subtype !== 'init' && !event.subtype.startsWith('hook_')) record('sdk', event);
    if (event.type === 'result') rootFinished = true;
    if (event.type === 'system' && event.subtype === 'task_notification') { notified = true; clearTimeout(grace); }
    if (rootFinished && (!config.background || notified)) break;
  }
} catch (error) { record(graceExpired ? 'unavailable' : 'error', graceExpired ? 'task_notification not delivered within 8 seconds of terminal task_updated' : String(error)); }
finally { clearTimeout(timeout); clearTimeout(grace); release?.(); running.close(); }
if (config.child && sessionID) {
  const agents = await listSubagents(sessionID, {dir: config.cwd});
  for (const agent of agents) record('subagent_history', {agent, messages: await getSubagentMessages(sessionID, agent, {dir: config.cwd, limit: 20})});
}
if (config.hookLog) {
  for (const line of readFileSync(config.hookLog, 'utf8').trim().split('\n')) record('user_hook', JSON.parse(line));
}
// Credentials and init inventories are never logged. Preserve native IDs and
// payload shapes, replacing only the disposable workspace and transcript paths.
const taskOutputs = records.flatMap(({kind, value}) =>
  kind === 'sdk' && typeof value.output_file === 'string' ? [value.output_file] : []);
const redact = (key, value) => {
  if (key === 'transcript_path' || key === 'agent_transcript_path') return '<native-transcript>';
  if (typeof value !== 'string') return value;
  for (const path of taskOutputs) value = value.replaceAll(path, '<native-task-output>');
  return value.replaceAll(config.cwd, '<workspace>');
};
process.stdout.write(JSON.stringify(records, redact) + '\n');
