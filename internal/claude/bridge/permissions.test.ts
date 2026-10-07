import {test} from 'node:test';
import assert from 'node:assert/strict';
import type {CanUseTool} from '@anthropic-ai/claude-agent-sdk';
import {PermissionRequests} from './permissions.js';

test('native control cancellation after callback resolution remains visible', async () => {
  const frames: unknown[] = [];
  const permissions = new PermissionRequests(async frame => {frames.push(frame);}, () => {throw Error('emit failed');});
  const abort = new AbortController();
  const options: Parameters<CanUseTool>[2] = {signal: abort.signal, toolUseID: 'exact', requestId: 'native-request', agentID: 'child'};
  const input = {command: 'printf unchanged'};
  const pending = permissions.canUseTool('Bash', input, options);
  assert.deepEqual(frames[0], {kind: 'permission', id: '1', tool: 'Bash', input, toolUseID: 'exact', agentID: 'child', description: ''});
  permissions.respond('1', {behavior: 'allow'});
  assert.deepEqual(await pending, {behavior: 'allow'});
  assert.equal(permissions.size, 1); // SDK has not yet confirmed a native result.
  abort.abort();
  permissions.respond('1', {behavior: 'allow'});
  assert.equal(permissions.size, 0);
  assert.deepEqual(frames.slice(1), [
    {kind: 'permission_decision', id: '1', toolUseID: 'exact', allow: true},
    {kind: 'permission_cancelled', id: '1'},
  ]);
});

test('only matching native completion retires the signal and preserves answers', async () => {
  const frames: unknown[] = [];
  const permissions = new PermissionRequests(async frame => {frames.push(frame);}, () => {throw Error('emit failed');});
  const abort = new AbortController();
  const input = {questions: [{question: 'Choose'}]};
  const pending = permissions.canUseTool('AskUserQuestion', input, {signal: abort.signal, toolUseID: 'question', requestId: 'native-question'});
  const answers = {Choose: 'custom text'};
  permissions.respond('1', {behavior: 'allow', updatedInput: {answers}});
  assert.deepEqual(await pending, {behavior: 'allow', updatedInput: {...input, answers}});
  permissions.settle('unrelated');
  assert.equal(permissions.size, 1);
  permissions.settle('question');
  assert.equal(permissions.size, 0);
  abort.abort();
  assert.equal(frames.length, 2);
  assert.equal(frames.some(frame => (frame as {kind: string}).kind === 'permission_cancelled'), false);
});
