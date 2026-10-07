import test from 'node:test';
import assert from 'node:assert/strict';
import type {Query} from '@anthropic-ai/claude-agent-sdk';
import {verifyVCSGuardHooks, nativeVCSGuard} from './vcs_guard.js';
import {writeFile, readFile, stat} from 'node:fs/promises';
import {join} from 'node:path';
import {createHash} from 'node:crypto';

const policy = {allDisabled:false, managedOnly:false, pluginOnly:false, policyHookCount:0};
const commandText = '/private/helper --claude-guard-env /private/startup /private/receipt';
const owned = ['SessionStart', 'PreToolUse'].map(event => ({event, matcher:event === 'PreToolUse' ? '^Bash$' : '', source:'flagSettings', type:'command', commandText}));
const inspect = (listing:unknown, effective = {}): Promise<void> => verifyVCSGuardHooks({getHooksListing:async()=>listing, getSettings:async()=>({effective})} as unknown as Query, commandText);

test('native guard admits only owned probes and retains unrelated caller hooks', async () => {
  const hook = {event:'PreToolUse', matcher:'Bash', source:'projectSettings', type:'command'};
  await inspect({policy, hooks:[...owned, {...hook, matcher:'Read'}, {...hook, disabled:true}, {event:'Stop',source:'sessionHook'}]});
  for (const extra of [hook, {...hook, runsInBackground:true}, {event:'FileChanged',source:'projectSettings',runsInBackground:true}, {...owned[0], source:'projectSettings'}, {...hook,source:'sessionHook'}]) {
    await assert.rejects(inspect({policy,hooks:[...owned, extra]}), /Competing native/);
  }
  await assert.rejects(inspect({policy,hooks:[owned[1]]}), /probe unavailable/);
  await assert.rejects(inspect({policy:{...policy,allDisabled:true},hooks:owned}), /hook policy/);
  await assert.rejects(inspect({policy,hooks:owned,errors:[{}]}), /hook policy/);
  await assert.rejects(inspect({policy,hooks:owned}, {sandbox:{credentials:{envVars:[{name:'BASH_ENV',mode:'deny'}]}}}), /sandbox startup/);
  await inspect({policy,hooks:owned}, {sandbox:{credentials:{envVars:[{name:'TEST_TOKEN',mode:'mask'}]}}});
});

test('startup and concurrent tool callbacks require their own completed native receipts', async t => {
  const probe = await nativeVCSGuard('/private/helper', '/private/startup');
  t.after(() => probe.close());
  const hook = probe.settings.hooks!.SessionStart[0].hooks[0];
  assert.equal(hook.type, 'command');
  if (hook.type !== 'command') return;
  const directory = hook.args![2];
  const rows = owned.map(row => ({...row, commandText:[hook.command,...hook.args!].join(' ')}));
  const query = {getHooksListing:async()=>({policy,hooks:rows}),getSettings:async()=>({effective:{}})} as unknown as Query;
  let admitted = false;
  const startup = probe.verify(query).then(()=>{admitted=true;});
  await new Promise(resolve=>setTimeout(resolve,40));
  assert.equal(admitted,false);
  await writeFile(join(directory,'startup'),'ok');
  await startup;
  const first = probe.verify(query,'first');
  let secondAdmitted=false;
  const second=probe.verify(query,'second').then(()=>{secondAdmitted=true;});
  const path=(id:string)=>join(directory,createHash('sha256').update(id).digest('hex'));
  await writeFile(path('first'),'ok');
  await first;
  assert.equal(secondAdmitted,false);
  await writeFile(path('second'),'ok');
  await second;
  await writeFile(path('invalid'),'partial');
  await assert.rejects(probe.verify(query,'invalid'),/Invalid native startup receipt/);
  await assert.rejects(readFile(path('invalid')), {code:'ENOENT'});
  await probe.close();
  await assert.rejects(stat(directory), {code:'ENOENT'});
});
