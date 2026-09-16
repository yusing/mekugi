import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { createContext, runInContext } from 'node:vm';

const html = readFileSync(new URL('./dashboard.html', import.meta.url), 'utf8');
const script = html.split('<script>')[1].split('</script>')[0];

function dashboard() {
  const requests = [];
  const timers = [];
  const nodes = new Map();
  const context = createContext({
    document: {
      getElementById(id) {
        if (!nodes.has(id)) nodes.set(id, { dataset: {}, addEventListener() {} });
        return nodes.get(id);
      },
      querySelectorAll() { return []; },
    },
    fetch() {
      return new Promise((resolve, reject) => requests.push({ resolve, reject }));
    },
    setTimeout(callback, delay) { timers.push({ callback, delay }); },
    setInterval() { assert.fail('polling must not run on an independent interval'); },
  });
  runInContext(script, context);
  // Exercise the shipped scheduler independently of the metrics presentation.
  runInContext('render = data => { currentSnapshot = data; }', context);
  return { requests, timers, nodes, context };
}

const settle = () => new Promise(resolve => setImmediate(resolve));

test('wait for fetch and JSON completion before scheduling the next snapshot', async () => {
  const app = dashboard();
  assert.equal(app.requests.length, 1);
  assert.equal(app.timers.length, 0);
  let finishJSON;
  app.requests[0].resolve({ ok: true, json: () => new Promise(resolve => { finishJSON = resolve; }) });
  await settle();
  assert.equal(app.timers.length, 0);
  finishJSON({ sequence: 1 });
  await settle();
  assert.equal(runInContext('currentSnapshot.sequence', app.context), 1);
  assert.equal(app.timers.length, 1);
  const timer = app.timers.shift();
  assert.equal(timer.delay, 2000);
  const pending = timer.callback();
  assert.equal(app.requests.length, 2);
  assert.equal(app.timers.length, 0);
  app.requests[1].resolve({ ok: true, json: async () => ({ sequence: 2 }) });
  await pending;
  assert.equal(runInContext('currentSnapshot.sequence', app.context), 2);
  assert.equal(app.timers.length, 1);
});

for (const failure of ['network', 'HTTP', 'JSON']) {
  test(`resume polling after ${failure} failure`, async () => {
    const app = dashboard();
    if (failure === 'network') app.requests[0].reject(new Error('offline'));
    else app.requests[0].resolve({
      ok: failure !== 'HTTP', status: 503,
      json: async () => { throw new Error('invalid JSON'); },
    });
    await settle();
    assert.equal(app.nodes.get('state').textContent, 'metrics unavailable');
    assert.equal(app.nodes.get('status-pill').dataset.state, 'error');
    assert.equal(app.timers.length, 1);
    const pending = app.timers.shift().callback();
    app.requests[1].resolve({ ok: true, json: async () => ({ sequence: 3 }) });
    await pending;
    assert.equal(runInContext('currentSnapshot.sequence', app.context), 3);
    assert.equal(app.timers.length, 1);
  });
}

test('render unknown carriers separately from rejection and host confirmation', () => {
  const app = dashboard();
  // Restore the shipped renderer, keeping the existing scheduler test harness.
  runInContext(script.slice(script.indexOf('function render(data){'), script.indexOf('const tabs=')), app.context);
  runInContext(`
    renderedRows = {};
    rows = (id, entries) => { renderedRows[id] = entries; };
    tableRows = () => {};
    renderDetails = () => {};
    sample = {
      schema: 'mekugi.capture.metrics.v4', requests: {}, usage: {}, cache: {},
      transport: {}, semantic: {}, protocol: {}, capture: {}, exchanges: [],
      mekugi: { calls: 21, successful: 0, rejected: 2, unclassified: 19, unmatched: 0 }
    };
    render(sample);
  `, app.context);
  const metric = label => runInContext(`renderedRows.mekugi.find(([label]) => label === ${JSON.stringify(label)})[1]`, app.context);
  assert.equal(metric('Rejected'), '2');
  assert.equal(metric('Unclassified'), '19');
  assert.equal(metric('Translated'), '0');
  runInContext('delete sample.mekugi.unclassified; render(sample)', app.context);
  assert.equal(metric('Unclassified'), 'unavailable');
});
