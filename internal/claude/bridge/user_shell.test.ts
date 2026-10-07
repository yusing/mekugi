import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {mkdtemp, writeFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {spawn} from 'node:child_process';
import {createInterface} from 'node:readline';
import {fileURLToPath} from 'node:url';
import {setTimeout} from 'node:timers/promises';
import type {SDKMessage, SDKUserMessage} from '@anthropic-ai/claude-agent-sdk';
import {UserShell, type NativeInput, type ShellFrame} from './user_shell.js';

test('native shell input and confirmed output retain once, after native append completion', async () => {
  const inputs: NativeInput[] = [];
  const frames: ShellFrame[] = [];
  const shell = new UserShell(input => inputs.push(input), async frame => {frames.push(frame);});
  await shell.start({id: 'shell/1', sessionID: '', text: "printf '<世界>' > result"}, '/workspace with spaces', '');
  const input = inputs[0]!;
  assert.equal(input.type, 'bash_command');
  if (input.type !== 'bash_command') throw Error('missing native shell input');
  assert.equal(input.command, "printf '<世界>' > result");
  const receipt = (content: string, uuid: SDKUserMessage['uuid'] = randomUUID()): SDKMessage => ({type: 'user', uuid: uuid!, session_id: 'native', parent_tool_use_id: null, isReplay: true, message: {role: 'user', content}});
  await shell.event(receipt("<bash-input>printf '&lt;世界&gt;' &gt; result</bash-input>"));
  const output = '<bash-stdout>&lt;世界&gt;</bash-stdout><bash-stderr>failed</bash-stderr><bash-exit-code>7</bash-exit-code>';
  await shell.event(receipt(output));
  const terminal = {type: 'command_lifecycle' as const, command_uuid: input.uuid, session_id: 'native', state: 'completed'};
  await shell.event(terminal);
  await shell.event(terminal);
  assert.equal(inputs.length, 4);
  const appends = inputs.filter(input => input.type === 'user');
  for (const append of appends) {
    if (append.type !== 'user') throw Error('execution repeated');
    assert.equal(append.shouldQuery, false);
    assert.equal(append.client_composed, true);
    assert.equal(await shell.event(receipt(String(append.message.content), append.uuid)), true);
    assert.equal(shell.pending, true); // Echo can precede durable history.
  }
  assert.equal(appends[0]!.type === 'user' && appends[0]!.isSynthetic, true);
  assert.deepEqual(appends.slice(1).map(append => append.type === 'user' ? append.message.content : ''), ["<bash-input>printf '&lt;世界&gt;' &gt; result</bash-input>", output]);
  await shell.event({...terminal, command_uuid: appends[0]!.uuid!});
  assert.equal(shell.pending, true);
  await shell.event({...terminal, command_uuid: appends[1]!.uuid!});
  assert.equal(shell.pending, true);
  await shell.event({...terminal, command_uuid: appends[2]!.uuid!});
  assert.equal(shell.pending, false);
  assert.equal(frames.length, 2);
  assert.equal(frames[1]!.retained, true);
  assert.equal(frames[1]!.output, output);
});

test('incomplete native evidence cannot fabricate history or repeat execution', async () => {
  const inputs: NativeInput[] = [];
  const frames: ShellFrame[] = [];
  const shell = new UserShell(input => inputs.push(input), async frame => {frames.push(frame);});
  await shell.start({id: 'shell/2', sessionID: 'native', text: 'true'}, '/workspace', 'native');
  const input = inputs[0]!;
  if (input.type !== 'bash_command') throw Error('missing native shell input');
  await shell.event({type: 'command_lifecycle', command_uuid: input.uuid!, session_id: 'other', state: 'completed'});
  assert.equal(shell.pending, true);
  await shell.event({type: 'command_lifecycle', command_uuid: input.uuid!, session_id: 'native', state: 'completed'});
  assert.equal(inputs.length, 1);
  assert.equal(shell.pending, false);
  assert.equal(frames[0]!.failed, true);
  assert.equal(frames[0]!.output, undefined);
});

test('shutdown hands confirmed receipts to retention-only input in the resumed query', async () => {
  const oldInputs: NativeInput[] = [];
  const newInputs: NativeInput[] = [];
  const frames: ShellFrame[] = [];
  const emit = async (frame: ShellFrame): Promise<void> => {frames.push(frame);};
  const old = new UserShell(input => oldInputs.push(input), emit);
  await old.start({id: 'shell/cancel', sessionID: 'native', text: 'sleep 30'}, '/workspace', 'native');
  const execution = oldInputs[0]!;
  if (execution.type !== 'bash_command') throw Error('missing native shell input');
  const stopped = old.shutdown();
  assert.ok(stopped);
  assert.equal(old.shutdown(), undefined);
  assert.deepEqual(oldInputs[1]?.type === 'control_request' && oldInputs[1].request, {subtype: 'end_session'});
  const input = '<bash-input>sleep 30</bash-input>';
  const output = '<bash-stdout>partial</bash-stdout><bash-stderr>Interrupted</bash-stderr><bash-exit-code>1</bash-exit-code>';
  for (const content of [input, output]) {
    await old.event({type: 'user', uuid: randomUUID(), session_id: 'native', parent_tool_use_id: null,
      isReplay: true, message: {role: 'user', content}});
  }
  await old.event({type: 'command_lifecycle', command_uuid: execution.uuid, session_id: 'native', state: 'completed'});
  assert.equal(oldInputs.length, 2); // The old input reader cannot retain after shutdown.
  assert.equal(frames.length, 1); // Native exit alone is not retained completion.
  const resumed = new UserShell(input => newInputs.push(input), emit);
  resumed.retain(await stopped);
  assert.equal(newInputs.length, 3);
  assert.deepEqual(newInputs.slice(1).map(input => input.type === 'user' ? input.message.content : ''), [input, output]);
  for (const append of newInputs) {
    if (append.type !== 'user') throw Error('resumed query repeated execution');
    assert.equal(append.shouldQuery, false);
    assert.equal(append.session_id, 'native');
    assert.equal(await resumed.event({type: 'command_lifecycle', command_uuid: append.uuid!, session_id: 'other', state: 'completed'}), false);
    assert.equal(resumed.pending, true);
  }
  const append = newInputs[1]!;
  if (append.type !== 'user') throw Error('missing append');
  // Native transcript-only results belong to shell retention, not Main.
  assert.equal(await resumed.event({type: 'result', subtype: 'error_during_execution', is_error: true,
    user_message_uuid: append.uuid, session_id: 'native', uuid: randomUUID(), duration_ms: 0,
    duration_api_ms: 0, num_turns: 0, total_cost_usd: 0, modelUsage: {}, usage: {
      input_tokens: 0, output_tokens: 0, cache_creation_input_tokens: 0, cache_read_input_tokens: 0,
      cache_creation: {ephemeral_1h_input_tokens: 0, ephemeral_5m_input_tokens: 0},
      fallback_credit: null, inference_geo: '', iterations: [], output_tokens_details: {thinking_tokens: 0},
      server_tool_use: {web_fetch_requests: 0, web_search_requests: 0}, service_tier: 'standard', speed: 'standard'},
    stop_reason: null, permission_denials: [], errors: ['append rejected']}), true);
  for (const append of newInputs) {
    if (append.type !== 'user') throw Error('missing append');
    await resumed.event({type: 'command_lifecycle', command_uuid: append.uuid!, session_id: 'native', state: 'completed'});
  }
  assert.equal(resumed.pending, false);
  assert.equal(frames.length, 2);
  assert.equal(frames[1]!.retained, false);
  assert.equal(frames[1]!.output, output);
});

test('bridge reports failed shutdown rather than waiting forever for lost native evidence', async t => {
  for (const failure of ['transport-error', 'query-ended', 'missing-output']) {
    await t.test(failure, {timeout: 5000}, async t => {
      const workspace = await mkdtemp(join(tmpdir(), 'mekugi-shell-shutdown-'));
      const executable = join(workspace, 'native.mjs');
      await writeFile(executable, `#!/usr/bin/env node
import {createInterface} from 'node:readline';
import {randomUUID} from 'node:crypto';
let command;
const emit = value => process.stdout.write(JSON.stringify(value) + '\\n');
createInterface({input: process.stdin}).on('line', line => {
  const input = JSON.parse(line);
  if (input.request?.subtype === 'initialize') {
    emit({type: 'control_response', response: {subtype: 'success', request_id: input.request_id,
      response: {commands: [], models: [], agents: []}}});
  } else if (input.type === 'bash_command') {
    command = input;
    emit({type: 'user', uuid: randomUUID(), session_id: 'native', parent_tool_use_id: null,
      isReplay: true, message: {role: 'user', content: '<bash-input>sleep 30</bash-input>'}});
  } else if (input.request?.subtype === 'end_session') {
    ${failure === 'missing-output' ? "emit({type: 'command_lifecycle', command_uuid: command.uuid, session_id: 'native', state: 'completed'});" : ''}
    process.stdout.end(() => process.exit(${failure === 'transport-error' ? 1 : 0}));
  }
});
`, {mode: 0o700});
      const child = spawn(process.execPath, [fileURLToPath(new URL('./bridge.js', import.meta.url)),
        JSON.stringify({cwd: workspace, executable})], {cwd: workspace, stdio: ['pipe', 'pipe', 'pipe'], signal: t.signal});
      const lines = createInterface({input: child.stdout});
      const iterator = lines[Symbol.asyncIterator]();
      const exit = new Promise<void>(resolve => child.once('exit', () => resolve()));
      child.on('error', () => {}); // AbortSignal owns timeout cleanup.
      t.after(async () => {
        child.kill();
        await exit;
        lines.close();
        await rm(workspace, {recursive: true, force: true});
      });
      const next = async (): Promise<{kind: string; text?: string}> => {
        const line = await iterator.next();
        assert.equal(line.done, false, 'bridge closed without reporting shutdown failure');
        return JSON.parse(line.value!);
      };
      assert.equal((await next()).kind, 'ready');
      child.stdin.write(JSON.stringify({kind: 'shell', id: 'cancel', sessionID: '', text: 'sleep 30'}) + '\n');
      assert.equal((await next()).kind, 'shell_started');
      child.stdin.write('{"kind":"interrupt"}\n');
      assert.equal((await next()).kind, 'shell_restarting');
      const failed = await Promise.race([next(), setTimeout(2000, undefined, {ref: false})]);
      assert.ok(failed, 'bridge left shell cancellation waiting after native query failure');
      assert.equal(failed.kind, 'error');
      assert.match(failed.text!, /Native shell cancellation did not establish resumed context/);
      await exit;
    });
  }
});
