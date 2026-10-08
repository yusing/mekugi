import test from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {mkdtemp, mkdir, rm, writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {forkSession, getSessionMessages, getSubagentMessages, InMemorySessionStore, type SessionStoreEntry} from '@anthropic-ai/claude-agent-sdk';
import {scanSkillHistory} from './skill_history.js';

test('SDK selected snapshot crosses the display window, compaction and native fork', async () => {
  const sessionId = '12345678-1234-4234-8234-123456789abc';
  const dir = '/tmp/skill-history-sdk';
  const store = new InMemorySessionStore();
  const key = {projectKey: '-tmp-skill-history-sdk', sessionId};
  let parent: string | null = null;
  const entries: SessionStoreEntry[] = Array.from({length: 2101}, (_, index) => {
    const uuid = `message-${index}`;
    const entry = {type: 'user', uuid, parentUuid: parent, sessionId, message: {role: 'user', content: 'Conversation'}, timestamp: '2026-01-01T00:00:00Z'};
    parent = uuid;
    return entry;
  });
  await store.append(key, entries);
  const read = (id: string) => () => getSessionMessages(id, {dir, sessionStore: store, limit: 32769});
  const full = await scanSkillHistory(read(sessionId));
  assert.equal(full.length, 2101);
  assert.equal(full.at(-1)?.uuid, 'message-2100');
  const fork = await forkSession(sessionId, {dir, sessionStore: store});
  assert.equal((await scanSkillHistory(read(fork.sessionId))).length, 2101);
  await store.append(key, [
    {type: 'system', subtype: 'compact_boundary', uuid: 'compact', parentUuid: null, sessionId,
      compactMetadata: {preservedSegment: {headUuid: 'message-0', tailUuid: 'message-2100', anchorUuid: 'summary'}}},
    {type: 'user', uuid: 'summary', parentUuid: 'compact', sessionId, isCompactSummary: true, timestamp: '2026-01-02T00:00:00Z', message: {role: 'user', content: 'Native summary'}},
    {type: 'user', uuid: 'new-context', parentUuid: 'summary', sessionId, timestamp: '2026-01-02T00:00:01Z', message: {role: 'user', content: 'New context'}},
  ]);
  const compacted = await scanSkillHistory(read(sessionId));
  assert.equal(compacted.length, 2103);
  assert.equal(compacted[0]?.uuid, 'summary');
  assert.equal(compacted[1]?.uuid, 'message-0'); // Preserved native history is not a new post-compact load.
  assert.equal(compacted.at(-1)?.uuid, 'new-context');
  assert.equal((await scanSkillHistory(read(fork.sessionId))).length, 2101);

  await store.append({...key, subpath: 'subagents/agent-child'}, [
    ...entries, {type: 'agent_metadata', toolUseId: 'parent-tool'},
  ]);
  const child = () => getSubagentMessages(sessionId, 'child', {dir, sessionStore: store, limit: 32769});
  const selected = await scanSkillHistory(child, ['message-2100']);
  assert.deepEqual(selected.map(message => message.uuid), ['message-2100']);
  assert.equal(selected[0]?.parent_tool_use_id, 'parent-tool');
  await assert.rejects(scanSkillHistory(child, ['missing']), /selection is incomplete/);
});

test('fork context UUID capture stays complete when the display window exhausts child rows', async () => {
  const config = await mkdtemp(join(tmpdir(), 'mekugi-skill-window-'));
  try {
    const cwd = join(config, 'workspace');
    await mkdir(cwd);
    const id = '12345678-1234-4234-8234-123456789abc';
    const project = join(config, 'projects', cwd.replace(/[^a-zA-Z0-9]/g, '-'));
    await mkdir(join(project, id, 'subagents'), {recursive: true});
    const root = Array.from({length: 1998}, (_, i) => ({type: 'user', uuid: `root-${i}`, parentUuid: i ? `root-${i - 1}` : null, sessionId: id, message: {role: 'user', content: 'Fixture'}, timestamp: '2026-01-01T00:00:00Z'}));
    const child = Array.from({length: 4}, (_, i) => ({type: i % 2 ? 'user' : 'assistant', uuid: `child-${i}`, parentUuid: i ? `child-${i - 1}` : null, sessionId: id, message: {content: [i % 2
      ? {type: 'tool_result', tool_use_id: `read-${i - 1}`, content: 'Native read receipt'}
      : {type: 'tool_use', id: `read-${i}`, name: 'Read', input: {file_path: `${cwd}/${i ? 'late' : 'early'}/SKILL.md`}}]}, timestamp: '2026-01-01T00:00:00Z'}));
    const encode = (entries: unknown[]) => entries.map(entry => JSON.stringify(entry)).join('\n') + '\n';
    await writeFile(join(project, `${id}.jsonl`), encode(root));
    await writeFile(join(project, id, 'subagents', 'agent-child.jsonl'), encode(child));
    const script = String.raw`
      import assert from 'node:assert/strict';
      import {appendFile,writeFile} from 'node:fs/promises';
      import {sessionHistory} from ${JSON.stringify(new URL('./session_history.js', import.meta.url).href)};
      import {restoreSkillHistory} from ${JSON.stringify(new URL('./skill_history.js', import.meta.url).href)};
      const [id,cwd] = process.argv.slice(1);
      const history = await sessionHistory(id,cwd);
      assert.equal(history.children[0].messageIDs.length,2);
      const frames=[];
      const retained=await restoreSkillHistory(id,cwd,history.contexts,async frame=>frames.push(frame));
      assert.deepEqual(retained[0].messageIDs,['child-0','child-1','child-2','child-3']);
      assert.equal(retained[0].complete,true);
      assert.equal(frames.filter(f=>f.agentID==='child'&&f.phase==='message').length,4);
      assert.ok(!frames.some(f=>f.failed));
      const extra=[1998,1999].map(i=>({type:'user',uuid:'root-'+i,parentUuid:'root-'+(i-1),sessionId:id,message:{role:'user',content:'Fixture'},timestamp:'2026-01-01T00:00:00Z'}));
      await appendFile(${JSON.stringify(join(project, `${id}.jsonl`))},extra.map(m=>JSON.stringify(m)).join('\n')+'\n');
      const exhausted=await sessionHistory(id,cwd);
      assert.equal(exhausted.children.length,0);
      const full=await restoreSkillHistory(id,cwd,exhausted.contexts,async()=>{});
      assert.deepEqual(full[0],retained[0]);
      const legacy=[];
      await restoreSkillHistory(id,cwd,history.children,async frame=>legacy.push(frame));
      assert.ok(legacy.some(f=>f.agentID==='child'&&f.phase==='done'&&f.failed));
      const childPath=${JSON.stringify(join(project, id, 'subagents', 'agent-child.jsonl'))};
      const malformed=${JSON.stringify(child)};
      malformed[0].timestamp='unavailable';
      malformed[1].isCompactSummary=true;
      await writeFile(childPath,malformed.map(m=>JSON.stringify(m)).join('\n')+'\n');
      const failed=[];
      const captured=await restoreSkillHistory(id,cwd,history.contexts,async frame=>failed.push(frame),history.children);
      assert.deepEqual(captured[0],retained[0]);
      assert.ok(failed.some(f=>f.agentID==='child'&&f.phase==='done'&&f.failed));
      const overflow=Array.from({length:32769},(_,i)=>({type:'user',uuid:'child-'+i,parentUuid:i?'child-'+(i-1):null,sessionId:id,message:{role:'user',content:'Fixture'}}));
      await writeFile(childPath,overflow.map(m=>JSON.stringify(m)).join('\n')+'\n');
      const partial=await restoreSkillHistory(id,cwd,history.contexts,async()=>{},history.children);
      assert.deepEqual(partial[0],{...history.children[0],complete:false});
    `;
    execFileSync(process.execPath, ['--input-type=module', '-e', script, id, cwd], {env: {...process.env, CLAUDE_CONFIG_DIR: config}});
  } finally { await rm(config, {recursive: true, force: true}); }
});

test('SDK sentinel and inherited admission distinguish complete and incomplete snapshots', async () => {
  const message = {type: 'user' as const, uuid: 'same', session_id: 'session', message: {}, parent_tool_use_id: null, parent_agent_id: null};
  await assert.rejects(scanSkillHistory(async () => [message, message]), /repeated message identity/);
  const snapshot = Array.from({length: 32769}, (_, index) => ({...message, uuid: String(index)}));
  await assert.rejects(scanSkillHistory(async () => snapshot), /message limit/);
  assert.equal((await scanSkillHistory(async () => snapshot.slice(0, -1))).length, 32768);
  assert.deepEqual((await scanSkillHistory(async () => snapshot, ['0'])).map(message => message.uuid), ['0']);
  await assert.rejects(scanSkillHistory(async () => snapshot, ['32768']), /message limit/);
});
