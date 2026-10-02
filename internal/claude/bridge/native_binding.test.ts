import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, realpath } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { query, createSdkMcpServer, tool, type PreToolUseHookInput } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';

test('native MCP tool-use metadata joins main and concurrent subagent hook receipts', {
  skip: process.env.MEKUGI_TEST_NATIVE_CLAUDE !== '1', timeout: 180000,
}, async () => {
  const cwd = await realpath(await mkdtemp(join(tmpdir(), 'mekugi-native-binding-')));
  const receipts = new Map<string, PreToolUseHookInput>();
  const calls: {label: string; id: string; agent?: string; session: string}[] = [];
  const server = createSdkMcpServer({name: 'mekugi_probe', tools: [tool('scope', 'Record the native caller scope once.', {label: z.string()}, async (args, extra) => {
    const meta = (extra as {_meta?: Record<string, unknown>})._meta;
    const id = meta?.['claudecode/toolUseId'];
    assert.equal(typeof id, 'string', 'native MCP call lacks native tool-use metadata');
    const receipt = receipts.get(id as string);
    assert.ok(receipt, 'MCP caller has no matching prior native hook receipt');
    assert.equal(receipt.tool_name, 'mcp__mekugi_probe__scope');
    assert.deepEqual(receipt.tool_input, args);
    assert.equal(receipt.cwd, cwd);
    calls.push({label: args.label, id: id as string, agent: receipt.agent_id, session: receipt.session_id});
    return {content: [{type: 'text', text: 'Recorded. Return OK.'}]};
  })]});
  const running = query({prompt: 'Call mcp__mekugi_probe__scope with label main exactly once. Then launch two binding_probe agents in parallel in a single response: ask one to call scope with label child-a, the other label child-b. Wait for both, then say OK. No other tools.', options: {
    cwd, model: 'haiku', systemPrompt: {type: 'preset', preset: 'claude_code'}, settingSources: ['user', 'project', 'local'],
    mcpServers: {mekugi_probe: server}, allowedTools: ['mcp__mekugi_probe__scope', 'Agent'],
    agents: {binding_probe: {description: 'Native MCP binding probe', prompt: 'Call mcp__mekugi_probe__scope exactly once with the requested label, then return OK. No other tools.', tools: ['mcp__mekugi_probe__scope'], model: 'haiku'}},
    hooks: {PreToolUse: [{matcher: 'mcp__mekugi_probe__scope', hooks: [async input => {
      assert.equal(input.hook_event_name, 'PreToolUse');
      const hook = input as PreToolUseHookInput;
      assert.ok(hook.tool_use_id);
      receipts.set(hook.tool_use_id, hook);
      return {};
    }]}]}, maxTurns: 12, abortController: new AbortController(),
  }});
  try {
    for await (const event of running) {
      if (event.type === 'result') assert.equal(event.subtype, 'success');
    }
    assert.equal(calls.length, 3);
    assert.equal(calls.find(c => c.label === 'main')?.agent, undefined);
    const children = calls.filter(c => c.label !== 'main');
    assert.equal(children.length, 2);
    assert.ok(children.every(c => c.agent));
    assert.equal(new Set(children.map(c => c.agent)).size, 2);
    assert.equal(new Set(calls.map(c => c.session)).size, 1);
    assert.equal(new Set(calls.map(c => c.id)).size, 3);
    process.stdout.write(`Native binding acceptance: ${JSON.stringify(calls)}\n`);
  } finally { running.close(); await rm(cwd, {recursive: true}); }
});
