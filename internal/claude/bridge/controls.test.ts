import { test } from 'node:test';
import assert from 'node:assert/strict';
import { setSettings, type SettingsCommand } from './controls.js';

test('native controls preserve selections, await host and never write settings files', async () => {
  const calls: unknown[] = [];
  let complete: (() => void) | undefined;
  const runtime = {
    setModel: async (model?: string) => { calls.push(['model', model]); await new Promise<void>(resolve => { complete = resolve; }); },
    applyFlagSettings: async (settings: unknown) => { calls.push(['flags', settings]); },
  };
  const command: SettingsCommand = {kind: 'settings', id: '1', field: 'model', value: 'opus'};
  let acknowledged = false;
  const pending = setSettings(runtime, command).then(receipt => { acknowledged = true; return receipt; });
  await Promise.resolve();
  assert.equal(acknowledged, false);
  complete!();
  assert.deepEqual(await pending, command);
  assert.deepEqual(await setSettings(runtime, {...command, field: 'effort', value: 'max'}), {...command, field: 'effort', value: 'max'});
  await setSettings(runtime, {...command, field: 'effort', value: 'default'});
  assert.deepEqual(calls, [['model', 'opus'], ['flags', {effortLevel: 'max'}], ['flags', {effortLevel: null}]]);
});

test('model default and native rejection are explicit receipts', async () => {
  const models: (string | undefined)[] = [];
  const runtime = {setModel: async (model?: string) => { models.push(model); if (model) throw new Error('Managed model restriction'); }, applyFlagSettings: async () => {}};
  const base: SettingsCommand = {kind: 'settings', id: '2', field: 'model', value: 'default'};
  assert.deepEqual(await setSettings(runtime, base), base);
  const denied = await setSettings(runtime, {...base, value: 'restricted'});
  assert.equal(denied.failed, true);
  assert.match(denied.text!, /Managed model restriction/);
  assert.deepEqual(models, [undefined, 'restricted']);
  const bad = await setSettings(runtime, {...base, field: 'effort', value: 'priority'});
  assert.equal(bad.failed, true);
});
