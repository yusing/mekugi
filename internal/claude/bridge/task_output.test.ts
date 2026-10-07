import test from 'node:test';
import assert from 'node:assert/strict';
import type {Query, SDKMessage} from '@anthropic-ai/claude-agent-sdk';
import {taskOutput, type OutputFrame} from './task_output.js';

const started = (id: string, background = false): SDKMessage => ({type: 'system', subtype: 'task_started', task_id: id,
  tool_use_id: `tool-${id}`, task_type: 'local_bash', is_backgrounded: background, description: 'Run fixture', session_id: 'native', uuid: '00000000-0000-4000-8000-000000000001'});
const flush = (): Promise<void> => new Promise(resolve => setImmediate(resolve));

test('native output snapshots stream before completion and terminal sampling preserves native results', async t => {
  t.mock.timers.enable({apis: ['setTimeout']});
  const frames: OutputFrame[] = [];
  let tail = {output: 'first\n', total_bytes: 6, truncated: false};
  const reads: string[] = [];
  const query = {getTaskOutput: async (id: string) => {reads.push(id); return tail;}} as unknown as Query;
  const observer = taskOutput(query, async frame => {frames.push(frame);}, async text => {assert.fail(text);});
  t.after(observer.close);
  const event = started('shell');
  const before = JSON.stringify(event);
  await observer.event(event);
  t.mock.timers.tick(125); await flush();
  assert.deepEqual(frames, [{kind: 'command_output', id: 'tool-shell', caller: '', taskID: 'shell', text: 'first\n', truncated: false, done: false, failed: false}]);
  tail = {...tail, total_bytes: 12};
  t.mock.timers.tick(125); await flush();
  assert.equal(frames.length, 1, 'byte-only changes must not repaint identical snapshots');
  tail = {output: 'first\nsecond\n', total_bytes: 13, truncated: false};
  t.mock.timers.tick(125); await flush();
  assert.equal(frames.at(-1)?.text, tail.output);
  assert.equal(frames.at(-1)?.done, false);
  const result = {type: 'user', session_id: 'native', parent_tool_use_id: null, message: {role: 'user', content: [{type: 'tool_result', tool_use_id: 'tool-shell', content: 'Native aggregate', is_error: false}]}} as SDKMessage;
  const resultBefore = JSON.stringify(result);
  await observer.event(result);
  const count = reads.length;
  t.mock.timers.tick(1000); await flush();
  assert.equal(reads.length, count);
  assert.equal(frames.at(-1)?.done, false, 'foreground native aggregate must settle the output');
  assert.equal(JSON.stringify(event), before);
  assert.equal(JSON.stringify(result), resultBefore);
});

test('background snapshots replace truncated tails and settle at the actual native task edge', async t => {
  t.mock.timers.enable({apis: ['setTimeout']});
  const frames: OutputFrame[] = [];
  let tail = {output: 'tail\n', total_bytes: 100000, truncated: true};
  const observer = taskOutput({getTaskOutput: async () => tail} as unknown as Query, async frame => {frames.push(frame);}, async text => {assert.fail(text);});
  t.after(observer.close);
  await observer.event(started('background', true));
  t.mock.timers.tick(125); await flush();
  await observer.event({type: 'result'} as SDKMessage);
  assert.equal(frames.at(-1)?.done, false);
  tail = {output: 'final\n', total_bytes: 100006, truncated: true};
  await observer.event({type: 'system', subtype: 'task_notification', task_id: 'background', tool_use_id: 'tool-background', status: 'failed'} as SDKMessage);
  assert.deepEqual(frames.at(-1), {kind: 'command_output', id: 'tool-background', caller: '', taskID: 'background', text: 'final\n', truncated: true, done: true, failed: true});
});

test('output polling is bounded and gives every live task a slot', async t => {
  t.mock.timers.enable({apis: ['setTimeout']});
  const reads: string[] = [];
  const observer = taskOutput({getTaskOutput: async (id: string) => {reads.push(id); return {output: '', total_bytes: 0, truncated: false};}} as unknown as Query, async () => {}, async text => {assert.fail(text);});
  t.after(observer.close);
  for (let i = 0; i < 9; i++) await observer.event(started(`shell-${i}`));
  for (let i = 0; i < 3; i++) {t.mock.timers.tick(125); await flush();}
  assert.equal(new Set(reads).size, 9);
  assert.equal(reads.length, 12);
});

test('closed queries drop in-flight tails and native read failures remain auxiliary', async t => {
  t.mock.timers.enable({apis: ['setTimeout']});
  const frames: OutputFrame[] = [];
  const notices: string[] = [];
  let resolve!: (value: {output: string; total_bytes: number; truncated: boolean}) => void;
  const observer = taskOutput({getTaskOutput: async () => new Promise(done => {resolve = done;})} as unknown as Query, async frame => {frames.push(frame);}, async text => {notices.push(text);});
  await observer.event(started('old'));
  t.mock.timers.tick(125); await flush();
  observer.close();
  resolve({output: 'expired', total_bytes: 7, truncated: false}); await flush();
  assert.equal(frames.length, 0);
  const failing = taskOutput({getTaskOutput: async () => {throw new Error('native read rejected');}} as unknown as Query, async frame => {frames.push(frame);}, async text => {notices.push(text);});
  t.after(failing.close);
  await failing.event(started('new'));
  for (let i = 0; i < 2; i++) {t.mock.timers.tick(125); await flush();}
  assert.equal(notices.length, 1);
  assert.match(notices[0]!, /native read rejected/);
  assert.equal(frames.length, 0);
});

for (const background of [false, true]) {
test(`terminal reconciliation preserves an in-flight ${background ? 'background' : 'foreground'} read`, async t => {
  t.mock.timers.enable({apis: ['setTimeout']});
  const frames: OutputFrame[] = [];
  let resolve!: (value: {output: string; total_bytes: number; truncated: boolean}) => void;
  let reads = 0;
  const query = {getTaskOutput: async () => ++reads === 1 ? new Promise(done => {resolve = done;}) : {output: 'first\nfinal\n', total_bytes: 12, truncated: false}} as unknown as Query;
  const observer = taskOutput(query, async frame => {frames.push(frame);}, async text => {assert.fail(text);});
  t.after(observer.close);
  await observer.event(started('pending', background));
  t.mock.timers.tick(125); await flush();
  const terminal = observer.event({type: 'system', subtype: 'task_notification', task_id: 'pending', tool_use_id: 'tool-pending', status: 'completed'} as SDKMessage);
  await flush();
  assert.equal(frames.length, 0);
  resolve({output: 'first\n', total_bytes: 6, truncated: false});
  await terminal;
  assert.equal(frames[0]?.text, 'first\n');
  assert.equal(frames[0]?.done, false);
  assert.equal(frames.at(-1)?.text, 'first\nfinal\n');
  assert.equal(frames.at(-1)?.done, background, 'only background reconciliation settles output; foreground keeps its native aggregate');
  assert.equal(reads, 2);
});
}

for (const failed of [false, true]) test(`terminal reconciliation ignores expired ${failed ? 'failed' : 'successful'} reads`, async t => {
  t.mock.timers.enable({apis: ['setTimeout']});
  const frames: OutputFrame[] = [];
  const notices: string[] = [];
  let reads = 0;
  let resolve!: (value: {output: string; total_bytes: number; truncated: boolean}) => void;
  let reject!: (error: Error) => void;
  const observer = taskOutput({getTaskOutput: async () => {
    reads++;
    return new Promise((done, fail) => {resolve = done; reject = fail;});
  }} as unknown as Query, async frame => {frames.push(frame);}, async text => {notices.push(text);});
  t.after(observer.close);
  await observer.event(started('expired', true));
  t.mock.timers.tick(125); await flush();
  const terminal = observer.event({type: 'system', subtype: 'task_updated', task_id: 'expired', patch: {status: 'completed'}} as SDKMessage);
  t.mock.timers.tick(750); await terminal;
  if (failed) reject(new Error('expired native read'));
  else resolve({output: 'too late', total_bytes: 8, truncated: false});
  await flush();
  assert.equal(frames.length, 0);
  assert.equal(reads, 1);
  assert.deepEqual(notices, []);
});
