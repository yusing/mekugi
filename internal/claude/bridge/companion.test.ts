import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdtemp, rm, realpath, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { once } from 'node:events';
import type { HookInput, SDKMessage } from '@anthropic-ai/claude-agent-sdk';
import { companion } from './companion.js';

async function fixture(t: test.TestContext, failure?: 'reject' | 'disconnect' | 'oversized' | 'stall'): Promise<{
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
    if (failure === 'reject') { res.writeHead(503); res.end('unavailable'); }
    else if (failure === 'disconnect') req.socket.destroy();
    else if (failure === 'oversized') res.end('x'.repeat(8193));
    else if (failure !== 'stall') res.end('{}');
  });
  server.listen(socket);
  await once(server, 'listening');
  t.after(async () => { server.close(); await once(server, 'close'); await rm(cwd, {recursive: true}); });
  return {observer: companion({socket, token: 'test-capability'}, cwd, async text => { notices.push(text); }), payloads, notices, cwd};
}

async function invoke(observer: ReturnType<typeof companion>, input: HookInput, toolID?: string, signal = new AbortController().signal): Promise<unknown> {
  const callback = observer.hooks?.[input.hook_event_name]?.[0]?.hooks[0];
  assert.ok(callback);
  return callback(input, toolID, {signal});
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

test('verified native resume restores only matching workspace evidence before tools', async t => {
  const {observer, payloads, notices, cwd} = await fixture(t);
  const info = {sessionId: 'resume-session', cwd, summary: 'Native history', lastModified: 0};
  await observer.resume(info);
  assert.deepEqual(payloads, [{operation: 'bind', binding: {runtime: 'claude', session: 'resume-session', workspace: cwd}}]);
  assert.deepEqual(notices, []);
  await observer.resume({...info, cwd: undefined});
  await observer.resume({...info, cwd: '/'});
  assert.equal(payloads.length, 1);
  assert.equal(notices.length, 2);
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


for (const failure of ['reject', 'disconnect', 'oversized', 'stall'] as const) {
  test(`observation ${failure} cannot change native permission or input`, async t => {
    const {observer, notices, cwd} = await fixture(t, failure);
    const input: HookInput = {hook_event_name: 'PreToolUse', session_id: 'native', cwd,
      transcript_path: '/native/transcript', tool_use_id: 'native-tool', tool_name: 'Write',
      tool_input: {file_path: 'native.txt', content: 'unchanged'}};
    const before = JSON.stringify(input);
    assert.deepEqual(await invoke(observer, input, 'native-tool', AbortSignal.timeout(failure === 'stall' ? 100 : 2000)), {});
    assert.equal(JSON.stringify(input), before);
    assert.equal(notices.length, 1);
    assert.match(notices[0]!, /^Companion capture unavailable:/);
  });
}

type NativeEvidence = {
  runtime: string; sdk: string;
  cases: Record<string, {kind: string; value: Record<string, unknown>}[]>;
};

test('installed native payloads preserve call and task identities across hook and SDK delivery', async t => {
  const evidence = JSON.parse(await readFile(new URL('../testdata/native-hook-2.1.287.json', import.meta.url), 'utf8')) as NativeEvidence;
  assert.doesNotMatch(JSON.stringify(evidence), /\/tmp\/claude-/);
  assert.equal(evidence.runtime, '2.1.287');
  assert.equal(evidence.sdk, '0.3.287');
  for (const name of ['write', 'failed', 'partial', 'no_effect', 'background', 'background_stopped', 'subagent', 'coexist']) {
    await t.test(name, async t => {
      const {observer, payloads, notices, cwd} = await fixture(t);
      const records = JSON.parse(JSON.stringify(evidence.cases[name]).replaceAll('<workspace>', cwd)) as {kind: string; value: Record<string, unknown>}[];
      const starts = new Map<string, Record<string, unknown>>();
      const sdkInputs = new Map<string, unknown>();
      const taskForms = new Set<string>();
      for (const {kind, value} of records) {
        const original = JSON.stringify(value);
        if (kind === 'hook' && value.hook_event_name !== 'SubagentStop') {
          const input = value as unknown as HookInput;
          assert.deepEqual(await invoke(observer, input, value.tool_use_id as string | undefined), {});
          if (value.hook_event_name === 'PreToolUse' && ['Write', 'Bash', 'Edit'].includes(String(value.tool_name))) starts.set(String(value.tool_use_id), value);
        } else if (kind === 'sdk') {
          await observer.event(value as unknown as SDKMessage);
          if (value.type === 'assistant') {
            const message = value.message as {content: {type: string; id: string; input: unknown}[]};
            for (const block of message.content) if (block.type === 'tool_use') sdkInputs.set(block.id, block.input);
          }
          if (value.subtype === 'task_updated' || value.subtype === 'task_notification') taskForms.add(String(value.subtype));
        }
        assert.equal(JSON.stringify(value), original);
      }
      assert.equal(starts.size, 1);
      for (const [id, hook] of starts) {
        assert.deepEqual(sdkInputs.get(id), hook.tool_input);
        const calls = payloads.filter(p => (p.call as {id: string} | undefined)?.id === id);
        assert.deepEqual(calls.map(p => p.operation), ['before', 'after']);
        for (const call of calls) {
          const observed = call.call as {input: string; binding: {session: string; agent?: string}};
          assert.deepEqual(JSON.parse(observed.input), hook.tool_input);
          assert.equal(observed.binding.session, hook.session_id);
          assert.equal(observed.binding.agent, hook.agent_id);
        }
        const outcome = calls[1]!.terminal as {status: string; report: string; task?: string};
        assert.equal(outcome.status, name.startsWith('background') ? 'running' : ['failed', 'partial'].includes(name) ? 'failed' : 'completed');
        if (name.startsWith('background')) {
          const tasks = payloads.filter(p => p.operation === 'task').map(p => p.task as {id: string; callID?: string; status: string});
          assert.equal(tasks.length, 2);
          assert.ok(tasks.every(task => task.id === outcome.task && task.status === (name === 'background_stopped' ? 'stopped' : 'completed')));
          assert.equal(tasks.find(task => task.callID)?.callID, id);
        }
      }
      if (name.startsWith('background') || name === 'subagent') assert.deepEqual([...taskForms].sort(), ['task_notification', 'task_updated']);
      assert.deepEqual(notices, []);
    });
  }
});


test('native killed task_updated settles without requiring task_notification', async t => {
  const evidence = JSON.parse(await readFile(new URL('../testdata/native-hook-2.1.287.json', import.meta.url), 'utf8')) as NativeEvidence;
  const {observer, payloads, notices} = await fixture(t);
  const update = evidence.cases.background_stopped!.find(r => r.kind === 'sdk' && r.value.subtype === 'task_updated')!.value;
  await observer.event(update as unknown as SDKMessage);
  assert.deepEqual(payloads, [{operation: 'task', task: {id: update.task_id, session: update.session_id, status: 'stopped'}}]);
  assert.deepEqual(notices, []);
});
