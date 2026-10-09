import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdtemp, realpath, rm, mkdir, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { once } from 'node:events';
import type { HookInput, SDKMessage, Query } from '@anthropic-ai/claude-agent-sdk';
import { companion } from './companion.js';
import { nativeToolUseID } from './journal.js';
import { validateCompanionGuidance, verifyCompanionGuidance } from './guidance.js';

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

test('registered guidance preflight requires readable bounded regular artifacts', async t => {
  const f = await fixture(t);
  const plugin = join(f.base.cwd, 'plugin');
  const skill = join(plugin, 'skills', 'mekugi');
  await mkdir(skill, {recursive: true});
  const config = {socket: join(f.base.cwd, 'service.sock'), token: 'capability', plugin};
  assert.throws(() => validateCompanionGuidance(config), /ENOENT/);
  await writeFile(join(skill, 'SKILL.md'), 'Current workflow '.repeat(800));
  await writeFile(join(skill, 'frontends.md'), 'Full contracts');
  await mkdir(join(plugin, '.claude-plugin'));
  await writeFile(join(plugin, '.claude-plugin', 'plugin.json'), '{"name":"mekugi"}');
  await mkdir(join(plugin, 'hooks'));
  await writeFile(join(plugin, 'hooks', 'hooks.json'), '{"modules":["./register.js"]}');
  assert.throws(() => validateCompanionGuidance(config), /ENOENT/);
  await writeFile(join(plugin, 'hooks', 'register.js'), 'export function register(on) {}');
  validateCompanionGuidance(config); // Static text no longer uses the 10,000-unit hook carrier.
  await writeFile(join(skill, 'frontends.md'), 'x'.repeat(1024 * 1024 + 1));
  assert.throws(() => validateCompanionGuidance(config), /registry carrier bound/);
  await rm(join(skill, 'frontends.md'));
  await mkdir(join(skill, 'frontends.md'));
  assert.throws(() => validateCompanionGuidance(config), /not a regular file/);
});

test('native mod policy is inspected independently of optional VCS guarding', async () => {
  const inspect = (listing: unknown) => verifyCompanionGuidance({
    getHooksListing: async () => listing,
  } as unknown as Query);
  const policy = {allDisabled: false, managedOnly: false};
  await inspect({policy});
  await inspect({policy: {...policy, pluginOnly: true}}); // The carrier is a plugin.
  for (const listing of [
    {policy: {...policy, allDisabled: true}}, {policy: {...policy, managedOnly: true}},
    {policy: {...policy, policyUnreadable: true}}, {policy, safeMode: {}},
    {policy, bareMode: {}}, {policy, errors: ['Native settings unreadable']},
  ]) await assert.rejects(inspect(listing), /Mandatory companion guidance unavailable/);
});

for (const failure of ['overflow', 'transport'] as const) {
test(`compact ${failure} reports missing recovery without replacing the native summary`, async t => {
  const f = await fixture(t, 'x'.repeat(10001), failure === 'transport');
  const before = {...f.base, hook_event_name: 'SessionStart' as const, source: 'compact' as const};
  const snapshot = JSON.stringify(before);
  assert.deepEqual(await f.invoke(before), {});
  assert.equal(JSON.stringify(before), snapshot);
  assert.equal(f.payloads.find(p => p.operation === 'journal_recovery')?.maxCharacters, 10000);
  assert.match(f.notices[0]!, /Journal recovery unavailable:.*native summary preserved/);
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
