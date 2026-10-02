import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdtemp, rm, realpath } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { once } from 'node:events';
import type { HookInput, SDKMessage } from '@anthropic-ai/claude-agent-sdk';
import { companion } from './companion.js';

async function fixture(t: test.TestContext): Promise<{
  observer: ReturnType<typeof companion>; payloads: Record<string, unknown>[]; notices: string[]; cwd: string;
}> {
  const cwd = await realpath(await mkdtemp(join(tmpdir(), 'mekugi-hook-test-')));
  const socket = join(cwd, 'service.sock');
  const payloads: Record<string, unknown>[] = [];
  const notices: string[] = [];
  const server = createServer(async (req, res) => {
    assert.equal(req.headers.authorization, 'Bearer test-capability');
    let data = '';
    for await (const chunk of req) data += chunk;
    payloads.push(JSON.parse(data) as Record<string, unknown>);
    res.end('{}');
  });
  server.listen(socket);
  await once(server, 'listening');
  t.after(async () => { server.close(); await once(server, 'close'); await rm(cwd, {recursive: true}); });
  return {observer: companion({socket, token: 'test-capability'}, cwd, async text => { notices.push(text); }), payloads, notices, cwd};
}

async function invoke(observer: ReturnType<typeof companion>, input: HookInput, toolID?: string): Promise<unknown> {
  const callback = observer.hooks?.[input.hook_event_name]?.[0]?.hooks[0];
  assert.ok(callback);
  return callback(input, toolID, {signal: new AbortController().signal});
}

test('capture callbacks retain exact native input, identity and empty native output', async t => {
  const {observer, payloads, notices, cwd} = await fixture(t);
  const base = {session_id: 'session', cwd, transcript_path: '/native/transcript'};
  assert.deepEqual(await invoke(observer, {...base, hook_event_name: 'SessionStart', source: 'startup'}), {});
  assert.deepEqual(await invoke(observer, {...base, hook_event_name: 'SubagentStart', agent_id: 'child', agent_type: 'worker'}), {});
  const input = {file_path: 'ignored.txt', old_string: 'old', new_string: 'new', replace_all: false};
  const tool = {...base, agent_id: 'child', tool_use_id: 'tool-id', tool_name: 'Edit', tool_input: input};
  const before = JSON.stringify(input);
  assert.deepEqual(await invoke(observer, {...tool, hook_event_name: 'PreToolUse'}, 'tool-id'), {});
  assert.deepEqual(await invoke(observer, {...tool, hook_event_name: 'PostToolUseFailure', error: 'partial failure', is_interrupt: true}, 'tool-id'), {});
  assert.equal(JSON.stringify(input), before);
  assert.deepEqual(payloads.map(p => p.operation), ['bind', 'bind', 'before', 'after']);
  assert.deepEqual(payloads[2]?.call, {binding: {runtime: 'claude', session: 'session', workspace: cwd, agent: 'child'}, id: 'tool-id', tool: 'Edit', input: before, paths: ['ignored.txt']});
  assert.deepEqual(payloads[3]?.terminal, {status: 'stopped', report: 'partial failure', interrupted: true});
  assert.deepEqual(notices, []);
});

test('background mapping waits for either native terminal event, never root completion', async t => {
  const {observer, payloads, notices, cwd} = await fixture(t);
  const base = {session_id: 'session', cwd, transcript_path: '/native/transcript'};
  const tool = {...base, tool_use_id: 'bash-id', tool_name: 'Bash', tool_input: {command: 'original && unchanged', run_in_background: true}};
  await invoke(observer, {...tool, hook_event_name: 'PreToolUse'}, 'bash-id');
  await invoke(observer, {...tool, hook_event_name: 'PostToolUse', tool_response: {backgroundTaskId: 'task'}}, 'bash-id');
  assert.deepEqual(payloads[1]?.terminal, {status: 'running', task: 'task', report: '{"backgroundTaskId":"task"}'});
  assert.equal((payloads[0]?.call as Record<string, unknown>).shell, undefined);
  await observer.event({type: 'result', subtype: 'success', session_id: 'session'} as SDKMessage);
  assert.equal(payloads.length, 2);
  await observer.event({type: 'system', subtype: 'task_updated', session_id: 'session', task_id: 'task', patch: {status: 'completed'}} as SDKMessage);
  await observer.event({type: 'system', subtype: 'task_notification', session_id: 'session', task_id: 'task', tool_use_id: 'bash-id', status: 'failed', summary: 'native failure'} as SDKMessage);
  assert.deepEqual(payloads[2], {operation: 'task', task: {id: 'task', session: 'session', status: 'completed'}});
  assert.deepEqual(payloads[3], {operation: 'task', task: {id: 'task', session: 'session', callID: 'bash-id', status: 'failed', report: 'native failure'}});
  assert.deepEqual(notices, []);
});

test('missing binding or task evidence fails observationally without changing hook decisions', async t => {
  const {observer, payloads, notices, cwd} = await fixture(t);
  const tool = {session_id: 'session', cwd, transcript_path: '/native/transcript', tool_use_id: 'bash-id', tool_name: 'Bash', tool_input: {command: 'original', run_in_background: true}};
  assert.deepEqual(await invoke(observer, {...tool, hook_event_name: 'PreToolUse'}, 'different-id'), {});
  assert.deepEqual(await invoke(observer, {...tool, hook_event_name: 'PostToolUse', tool_response: {stdout: 'not terminal'}}), {});
  assert.deepEqual(await invoke(observer, {...tool, cwd: '/not-a-real-workspace', hook_event_name: 'PreToolUse'}), {});
  assert.equal(payloads.length, 0);
  assert.equal(notices.length, 3);
});
