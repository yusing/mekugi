import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdtemp, realpath, rm, mkdir, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { once } from 'node:events';
import type { HookInput, SDKMessage } from '@anthropic-ai/claude-agent-sdk';
import { companion } from './companion.js';
import { nativeToolUseID } from './journal.js';
import { companionGuidance, companionFrontendGuidance } from './guidance.js';

async function fixture(t: test.TestContext, recovery = 'Preserved native evidence', recoveryFailure = false) {
  const cwd = await realpath(await mkdtemp(join(tmpdir(), 'mekugi-journal-hook-')));
  const socket = join(cwd, 'service.sock');
  const payloads: Record<string, unknown>[] = [];
  const notices: string[] = [];
  const server = createServer(async (req, res) => {
    assert.equal(req.headers.authorization, 'Bearer capability');
    let data = '';
    for await (const chunk of req) data += chunk;
    const payload = JSON.parse(data) as Record<string, unknown>;
    payloads.push(payload);
    if (payload.operation === 'journal_recovery' && recoveryFailure) {res.writeHead(503); res.end('Recovery fixture unavailable'); return;}
    res.end(JSON.stringify(payload.operation === 'journal_recovery' ? {text: recovery} : {}));
  });
  server.listen(socket);
  await once(server, 'listening');
  t.after(async () => { server.close(); await once(server, 'close'); await rm(cwd, {recursive: true}); });
  const observer = companion({socket, token: 'capability', journalSchema: {}}, cwd, async text => { notices.push(text); });
  const base = {cwd, session_id: 'native-session', transcript_path: '/native/transcript'};
  const invoke = async (input: HookInput, id?: string) => {
    const callback = observer.hooks?.[input.hook_event_name]?.[0]?.hooks[0];
    assert.ok(callback);
    return callback(input, id, {signal: new AbortController().signal});
  };
  return {observer, payloads, notices, base, invoke};
}

test('journal receipts require SDK provenance and exact native tool identity without hook decisions', async t => {
  const f = await fixture(t);
  const input = {journal: [{op: 'log', text: 'Established result'}]};
  const hook = {...f.base, hook_event_name: 'PreToolUse' as const, agent_id: 'native-child', tool_use_id: 'native-call', tool_name: 'mcp__mekugi__journal_batch', tool_input: input};
  for (const provenance of [undefined, {name: 'mekugi', source: 'project' as const}, {name: 'other', source: 'sdk' as const}]) {
    assert.deepEqual(await f.invoke({...hook, mcp_server: provenance}, 'native-call'), {});
  }
  assert.deepEqual(await f.invoke({...hook, mcp_server: {name: 'mekugi', source: 'sdk'}}, 'different-call'), {});
  assert.equal(f.payloads.length, 0);
  assert.deepEqual(await f.invoke({...hook, mcp_server: {name: 'mekugi', source: 'sdk'}}, 'native-call'), {});
  assert.deepEqual(f.payloads, [{operation: 'journal_before', call: {binding: {runtime: 'claude', session: 'native-session', workspace: f.base.cwd, agent: 'native-child'}, id: 'native-call', tool: hook.tool_name, input: JSON.stringify(input)}}]);
  assert.equal(f.notices.length, 4);
});

test('only compact SessionStart adds bounded recovery, oversized carrier falls back unchanged', async t => {
  for (const text of ['🚀'.repeat(5000), '🚀'.repeat(5001)]) {
    await t.test(String(text.length), async t => {
      const f = await fixture(t, text);
      assert.deepEqual(await f.invoke({...f.base, hook_event_name: 'SessionStart', source: 'startup'}), {});
      assert.deepEqual(f.payloads.map(p => p.operation), ['bind']);
      const result = await f.invoke({...f.base, hook_event_name: 'SessionStart', source: 'compact'});
      assert.deepEqual(result, text.length <= 10000 ? {hookSpecificOutput: {hookEventName: 'SessionStart', additionalContext: text}} : {});
      assert.deepEqual(f.payloads.map(p => p.operation), ['bind', 'bind', 'journal_recovery']);
      assert.equal(f.notices.length, 1);
    });
  }
});

test('native child ancestry joins the sole result tool ID, not ambiguous result order', async t => {
  const f = await fixture(t);
  const event = {type: 'user', session_id: 'native-session', uuid: 'message', tool_use_result: {agentId: 'child', status: 'completed'}, message: {role: 'user', content: [{type: 'tool_result', tool_use_id: 'spawn-call', content: 'Native child answer'}]}};
  await f.observer.event(event as SDKMessage);
  assert.deepEqual(f.payloads, [{operation: 'journal_parent', nativeID: 'spawn-call', child: 'child', terminal: {status: 'completed'}}]);
  event.message.content.push({type: 'tool_result', tool_use_id: 'other-call', content: 'Other answer'});
  await f.observer.event(event as SDKMessage);
  assert.equal(f.payloads.length, 1);
});

test('MCP identity never substitutes transport or model IDs', () => {
  assert.equal(nativeToolUseID({requestId: 'native-call', agent: 'root'}), undefined);
  assert.equal(nativeToolUseID({_meta: {'claudecode/toolUseId': ''}}), undefined);
  assert.equal(nativeToolUseID({_meta: {'claudecode/toolUseId': 'x'.repeat(1025)}}), undefined);
  assert.equal(nativeToolUseID({_meta: {'claudecode/toolUseId': 'native-call'}, requestId: 1}), 'native-call');
});

test('root input, resumed queries, child and compact hooks deliver owned guidance', async t => {
  const f = await fixture(t);
  const plugin = join(f.base.cwd, 'plugin');
  const skill = join(plugin, 'skills', 'mekugi');
  await mkdir(skill, {recursive: true});
  const body = 'Record multi-step work in the durable journal.';
  const contracts = '## mcat\n\nRead bounded source rows.';
  await writeFile(join(skill, 'SKILL.md'), `---\nname: mekugi\ndescription: Companion\n---\n\n${body}\n`);
  await writeFile(join(skill, 'frontends.md'), contracts);
  const config = {socket: join(f.base.cwd, 'service.sock'), token: 'capability', journalSchema: {}, plugin};
  const observer = companion(config, f.base.cwd, async text => { f.notices.push(text); });
  const guidance = `${body}\n\nAuthenticated frontend contracts are injected into native context. If that context is unavailable during recovery, read the reference at ${JSON.stringify(join(skill, 'frontends.md'))}.`;
  assert.equal(companionGuidance(config), guidance);
  assert.equal(companion(config, f.base.cwd, async () => {}, false).hooks?.UserPromptSubmit, undefined);
  for (const source of ['startup', 'resume', 'clear'] as const) {
    const hook = observer.hooks?.SessionStart?.[0]?.hooks[0];
    assert.ok(hook);
    assert.deepEqual(await hook({...f.base, hook_event_name: 'SessionStart', source}, undefined, {signal: new AbortController().signal}), {});
  }
  const submit = observer.hooks?.UserPromptSubmit?.[0]?.hooks[0];
  assert.ok(submit);
  const prompt = {...f.base, hook_event_name: 'UserPromptSubmit' as const, prompt: 'Implement a feature'};
  assert.deepEqual(await submit(prompt, undefined, {signal: new AbortController().signal}),
    {hookSpecificOutput: {hookEventName: 'UserPromptSubmit', additionalContext: guidance}});
  assert.deepEqual(await submit(prompt, undefined, {signal: new AbortController().signal}), {});
  await observer.event({type: 'system', subtype: 'init', session_id: f.base.session_id, cwd: f.base.cwd} as SDKMessage);
  assert.deepEqual(await submit(prompt, undefined, {signal: new AbortController().signal}), {});
  assert.deepEqual(await submit({...prompt, session_id: 'fresh-session'}, undefined, {signal: new AbortController().signal}),
    {hookSpecificOutput: {hookEventName: 'UserPromptSubmit', additionalContext: guidance}});
  const resumed = companion(config, f.base.cwd, async () => {});
  const resumedSubmit = resumed.hooks?.UserPromptSubmit?.[0]?.hooks[0];
  assert.ok(resumedSubmit);
  assert.deepEqual(await resumedSubmit(prompt, undefined, {signal: new AbortController().signal}),
    {hookSpecificOutput: {hookEventName: 'UserPromptSubmit', additionalContext: guidance}});
  const child = observer.hooks?.SubagentStart?.[0]?.hooks[0];
  assert.ok(child);
  assert.deepEqual(await child({...f.base, hook_event_name: 'SubagentStart', agent_id: 'child', agent_type: 'worker'}, undefined, {signal: new AbortController().signal}),
    {hookSpecificOutput: {hookEventName: 'SubagentStart', additionalContext: guidance}});
  const start = observer.hooks?.SessionStart?.[0]?.hooks[0];
  assert.ok(start);
  assert.deepEqual(await start({...f.base, hook_event_name: 'SessionStart', source: 'compact'}, undefined, {signal: new AbortController().signal}),
    {hookSpecificOutput: {hookEventName: 'SessionStart', additionalContext: `${guidance}\n\nPreserved native evidence`}});
  assert.equal(f.payloads.find(p => p.operation === 'journal_recovery')?.maxCharacters, 10000 - guidance.length - 2);
  assert.equal(f.notices.length, 1);
  assert.equal(f.payloads.filter(p => p.operation === 'bind').length, 6);
  await writeFile(join(skill, 'frontends.md'), contracts.repeat(2000));
  assert.equal(companionGuidance(config), guidance);
  await writeFile(join(skill, 'SKILL.md'), body.repeat(250));
  assert.throws(() => companionGuidance(config), /inline context capacity/);
});

test('missing registered guidance is not silently replaced by an empty prompt', async t => {
  const f = await fixture(t);
  assert.throws(() => companion({socket: join(f.base.cwd, 'service.sock'), token: 'capability', plugin: join(f.base.cwd, 'missing')}, f.base.cwd, async () => {}), /ENOENT/);
});

test('authenticated frontend contracts use full bounded context hooks in every native lane', async t => {
  const f = await fixture(t);
  const plugin = join(f.base.cwd, 'tool-context-plugin');
  const skill = join(plugin, 'skills', 'mekugi');
  await mkdir(skill, {recursive: true});
  await writeFile(join(skill, 'SKILL.md'), 'Maintain the durable journal.');
  const contracts = Array.from({length: 5}, (_, i) => `## utility-${i}\n\n${'Exact contract \u{1f680} '.repeat(240)}\n\n`).join('');
  await writeFile(join(skill, 'frontends.md'), contracts);
  const config = {socket: join(f.base.cwd, 'service.sock'), token: 'capability', journalSchema: {}, plugin};
  const chunks = companionFrontendGuidance(config);
  assert.equal(chunks.join(''), contracts);
  assert.ok(chunks.length > 1);
  assert.ok(chunks.every(chunk => chunk.length <= 9000));
  const observer = companion(config, f.base.cwd, async text => {f.notices.push(text);});
  for (const input of [
    {...f.base, hook_event_name: 'UserPromptSubmit' as const, prompt: 'Implement the feature'},
    {...f.base, hook_event_name: 'SubagentStart' as const, agent_id: 'child', agent_type: 'worker'},
    {...f.base, hook_event_name: 'SessionStart' as const, source: 'compact' as const},
  ]) {
    const hooks = observer.hooks?.[input.hook_event_name]?.[0]?.hooks.slice(1);
    assert.ok(hooks);
    const contexts: string[] = [];
    for (const hook of hooks) {
      const result = await hook(input, undefined, {signal: new AbortController().signal});
      assert.ok('hookSpecificOutput' in result);
      const context = result.hookSpecificOutput as {additionalContext: string};
      contexts.push(context.additionalContext);
    }
    assert.equal(contexts.join(''), contracts);
  }
  const hooks = observer.hooks?.UserPromptSubmit?.[0]?.hooks.slice(1);
  assert.ok(hooks);
  for (const hook of hooks) assert.deepEqual(await hook({...f.base, hook_event_name: 'UserPromptSubmit', prompt: 'Next input'}, undefined, {signal: new AbortController().signal}), {});
  assert.deepEqual(f.notices, []);
});

for (const failure of ['overflow', 'transport'] as const) {
test(`compact ${failure} retains mandatory guidance without replacing the native summary`, async t => {
  const f = await fixture(t, 'x'.repeat(8000), failure === 'transport');
  const plugin = join(f.base.cwd, 'compact-plugin');
  const skill = join(plugin, 'skills', 'mekugi');
  await mkdir(skill, {recursive: true});
  await writeFile(join(skill, 'SKILL.md'), 'Guidance '.repeat(250));
  await writeFile(join(skill, 'frontends.md'), 'Contracts');
  const config = {socket: join(f.base.cwd, 'service.sock'), token: 'capability', journalSchema: {}, plugin};
  const observer = companion(config, f.base.cwd, async text => {f.notices.push(text);});
  const callback = observer.hooks?.SessionStart?.[0]?.hooks[0];
  assert.ok(callback);
  const before = {...f.base, hook_event_name: 'SessionStart' as const, source: 'compact' as const};
  const snapshot = JSON.stringify(before);
  assert.deepEqual(await callback(before, undefined, {signal: new AbortController().signal}),
    {hookSpecificOutput: {hookEventName: 'SessionStart', additionalContext: companionGuidance(config)}});
  assert.equal(JSON.stringify(before), snapshot);
  assert.equal(f.payloads.find(p => p.operation === 'journal_recovery')?.maxCharacters, 10000 - companionGuidance(config).length - 2);
  assert.match(f.notices[0]!, /Journal recovery unavailable:.*native summary and workflow guidance preserved/);
});
}

test('native task start supplies exact spawning call for terminal updates without a call ID', async t => {
  const f = await fixture(t);
  await f.observer.event({type: 'system', subtype: 'task_started', task_id: 'distinct-task', tool_use_id: 'spawn-call', session_id: 'native-session', description: 'Native child', uuid: '00000000-0000-4000-8000-000000000001'});
  await f.observer.event({type: 'system', subtype: 'task_updated', task_id: 'distinct-task', session_id: 'native-session', patch: {status: 'killed'}, uuid: '00000000-0000-4000-8000-000000000002'});
  assert.deepEqual(f.payloads, [
    {operation: 'journal_task_start', task: {id: 'distinct-task', session: 'native-session', callID: 'spawn-call'}},
    {operation: 'task', task: {id: 'distinct-task', session: 'native-session', status: 'stopped'}},
  ]);
});

 test('mchanges receipt uses the same exact native MCP provenance', async t => {
  const f = await fixture(t);
  const input = {...f.base, hook_event_name: 'PreToolUse' as const, agent_id: 'child-a', tool_use_id: 'read-a', tool_name: 'mcp__mekugi__mchanges', tool_input: {args: ['--mine']}, mcp_server: {name: 'mekugi', source: 'sdk' as const}};
  assert.deepEqual(await f.invoke(input, 'read-a'), {});
  assert.equal(f.payloads.length, 1);
  const call = f.payloads[0]!.call as {binding: {agent: string}; id: string; tool: string; input: string};
  assert.equal(call.binding.agent, 'child-a');
  assert.equal(call.id, 'read-a');
  assert.equal(call.tool, input.tool_name);
  assert.deepEqual(JSON.parse(call.input), input.tool_input);
});
