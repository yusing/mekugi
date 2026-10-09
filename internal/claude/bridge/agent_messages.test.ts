import test from 'node:test';
import assert from 'node:assert/strict';
import {request, type RequestOptions} from 'node:http';
import {join} from 'node:path';
import {pathToFileURL} from 'node:url';
import type {SDKUserMessage} from '@anthropic-ai/claude-agent-sdk';
import {AgentMessages} from './agent_messages.js';

test('stored child boundaries follow their preceding SDK rows even when native appends outrun SDK draining', async t => {
  const delivered: string[] = [];
  const owner = await AgentMessages.create(async boundary => {delivered.push(boundary.id);});
  t.after(() => owner.close());
  type Row = {uuid: string; agentId: string; origin: {kind: string}; door: string; message: {type: string; name?: string; content: {type: string}[]}};
  let append!: ($: unknown, row: Row, next: (row: Row) => Promise<{uuid: string; message: Row['message']}>) => Promise<unknown>;
  const mod = await import(pathToFileURL(join(owner.plugin, 'hooks/register.js')).href);
  mod.register((name: string, callback: typeof append) => {if (name === 'session.append') append = callback;});
  const api = {session: {id: async () => 'native-session'}, http: {fetch: async (_: string, options: RequestOptions & {body: string}) => {
    await new Promise<void>((resolve, reject) => {
      const req = request({...options, path: '/context'}, response => {
        response.resume();
        response.on('end', () => {assert.equal(response.statusCode, 200); resolve();});
      });
      req.on('error', reject);
      req.end(options.body);
    });
    return {ok: true};
  }}};
  const stored = async (uuid: string, type: string, block: string, boundary = false): Promise<void> => {
    const row: Row = {uuid, agentId: 'real-child', origin: {kind: 'engine'}, door: boundary ? 'compaction' : 'tool',
      message: {type, ...(boundary ? {name: 'compact_boundary'} : {}), content: [{type: block}]}};
    await append(api, row, async value => ({uuid: value.uuid, message: value.message}));
  };
  const drained = (uuid: NonNullable<SDKUserMessage['uuid']>, caller = 'parent-tool'): Promise<void> => owner.event({type: 'user', uuid, session_id: 'native-session',
    parent_tool_use_id: caller, message: {role: 'user', content: [{type: 'tool_result', tool_use_id: 'read', content: 'Native read receipt'}]}});
  const start = '00000000-0000-4000-8000-000000000001';
  const result = '00000000-0000-4000-8000-000000000002';
  const other = '00000000-0000-4000-8000-000000000003';
  const later = '00000000-0000-4000-8000-000000000004';
  const undrained = '00000000-0000-4000-8000-000000000005';
  await stored(start, 'assistant', 'tool_use');
  await stored(result, 'user', 'tool_result');
  await stored('compact', 'system', 'text', true);
  assert.deepEqual(delivered, []);
  await drained(other, 'sibling-tool');
  await drained(start);
  assert.deepEqual(delivered, []);
  await drained(result);
  assert.deepEqual(delivered, ['compact']);
  // A later boundary whose predecessor already drained publishes immediately.
  await stored(later, 'user', 'tool_result');
  await drained(later);
  await stored('compact-again', 'system', 'text', true);
  assert.deepEqual(delivered, ['compact', 'compact-again']);
  await stored(undrained, 'user', 'tool_result');
  await stored('retired-compact', 'system', 'text', true);
  await owner.close();
  await drained(undrained);
  assert.deepEqual(delivered, ['compact', 'compact-again']);
});
