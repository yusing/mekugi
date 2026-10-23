import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks, stripTypeScriptTypes } from "node:module";
import { afterEach, beforeEach, test } from "node:test";
import { compileModule } from "svelte/compiler";

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (
      specifier.startsWith("./") &&
      context.parentURL?.includes("/app/src/lib/") &&
      !/\.(?:ts|mjs|js)$/.test(specifier)
    )
      specifier += ".ts";
    if (specifier === "../../../../sessionapi/protocol") specifier += ".ts";
    return nextResolve(specifier, context);
  },
  load(url, context, nextLoad) {
    if (url.endsWith(".svelte.ts")) {
      return {
        format: "module",
        source: compileModule(stripTypeScriptTypes(readFileSync(new URL(url), "utf8")), {
          filename: new URL(url).pathname,
          generate: "client",
        }).js.code,
        shortCircuit: true,
      };
    }
    return nextLoad(url, context);
  },
});

const prefix = "mekugi.machine.v1:";
const values = new Map();
let blockedRead = false;
let blockedWrite = false;
globalThis.localStorage = {
  get length() {
    if (blockedRead) throw new Error("Storage unavailable");
    return values.size;
  },
  key(index) {
    return [...values.keys()][index] ?? null;
  },
  getItem(name) {
    if (blockedRead) throw new Error("Storage unavailable");
    return values.get(name) ?? null;
  },
  setItem(name, value) {
    if (blockedWrite) throw new Error("Storage full");
    values.set(name, value);
  },
  removeItem(name) {
    if (blockedWrite) throw new Error("Storage unavailable");
    values.delete(name);
  },
};
globalThis.location = { hash: "", pathname: "/", search: "" };
globalThis.history = { pushState: (_state, _title, url) => (location.hash = url) };
globalThis.addEventListener = () => {};
const { hub, Machine } = await import("./hub.svelte.ts");
const { view } = await import("./view.svelte.ts");
const Hub = hub.constructor;
const owners = [];
const started = [];
// Network authentication and reconnection are covered through the actual browser fixture.
Machine.prototype.start = function () {
  started.push(this.id);
};
function restore() {
  const owner = new Hub();
  owners.push(owner);
  return owner;
}
beforeEach(() => {
  values.clear();
  blockedRead = blockedWrite = false;
  started.length = 0;
  Object.assign(view, { machine: undefined, session: undefined, agent: undefined });
  location.hash = "";
});
afterEach(() => {
  for (const owner of owners.splice(0)) for (const machine of owner.machines) machine.close();
});

test("machines, identities, replacements and removals survive fresh hubs", async () => {
  const owner = restore();
  assert.equal(await owner.add(" Alpha ", "https://alpha.example/", " alpha-token "), undefined);
  assert.equal(await owner.add("Beta", "https://beta.example", "beta-token"), undefined);
  const [alpha, beta] = owner.machines;
  assert.equal(values.size, 2, "added machines must be persisted");
  const records = [...values.values()].map((value) => JSON.parse(value));
  assert.deepEqual(records, [
    { id: alpha.id, label: "Alpha", endpoint: "https://alpha.example", token: "alpha-token" },
    { id: beta.id, label: "Beta", endpoint: "https://beta.example", token: "beta-token" },
  ]);
  const address = location.hash;
  const reloaded = restore();
  assert.equal(reloaded.selected.id, beta.id);
  assert.equal(location.hash, address);
  assert.deepEqual(
    reloaded.machines.map((machine) => machine.saved()),
    records,
  );
  assert.deepEqual(started, [alpha.id, beta.id, alpha.id, beta.id]);

  assert.equal(reloaded.authorize(alpha.id, " replacement-token "), undefined);
  const rotated = restore();
  assert.equal(rotated.machines[0].saved().token, "replacement-token");
  rotated.remove(alpha.id);
  assert.equal(rotated.selected.id, beta.id);
  assert.deepEqual(
    restore().machines.map((machine) => machine.saved()),
    [records[1]],
  );
  rotated.remove(beta.id);
  assert.equal(view.machine, undefined);
  assert.deepEqual(restore().machines, []);
});

test("storage failures warn without blocking current-page connection changes", async () => {
  blockedRead = blockedWrite = true;
  const owner = restore();
  assert.match(owner.storageError, /could not be read/);
  assert.equal(await owner.add("Alpha", "https://alpha.example", "alpha-token"), undefined);
  const [alpha] = owner.machines;
  assert.deepEqual(started, [alpha.id]);
  assert.match(owner.storageError, /could not be saved/);
  assert.equal(owner.authorize(alpha.id, "replacement-token"), undefined);
  assert.equal(alpha.saved().token, "replacement-token");
  owner.remove(alpha.id);
  assert.deepEqual(owner.machines, []);
  assert.match(owner.storageError, /older machines or tokens/);
  blockedRead = blockedWrite = false;
  await owner.add("Beta", "https://beta.example", "beta-token");
  assert.match(
    owner.storageError,
    /could not be saved/,
    "another machine's save cannot hide a failure",
  );
  assert.equal(restore().machines[0].label, "Beta");
});

test("invalid storage is reported while valid machine records still reconnect", () => {
  const valid = { id: "alpha", label: "Alpha", endpoint: "https://alpha.example", token: "token" };
  values.set("theme", "dark");
  values.set(prefix + "json", "{");
  values.set(prefix + "null", "null");
  values.set(prefix + "empty", "{}");
  for (const record of [
    { ...valid, id: "bad-url", endpoint: "javascript:invalid" },
    { ...valid, id: "bad-token", token: "" },
    { ...valid, id: 42 },
    valid,
  ])
    values.set(prefix + record.id, JSON.stringify(record));
  values.set(prefix + "mismatched", JSON.stringify(valid));
  const saved = [...values];
  const owner = restore();
  assert.deepEqual(
    owner.machines.map((machine) => machine.saved()),
    [valid],
  );
  assert.deepEqual(started, [valid.id]);
  assert.match(owner.storageError, /could not be restored/);
  assert.deepEqual([...values], saved, "restoration does not rewrite saved data");
});

test("refused additions and token replacements leave saved machines unchanged", async () => {
  const owner = restore();
  await owner.add("Alpha", "https://alpha.example", "alpha-token");
  const saved = [...values];
  assert.match(await owner.add("", "https://beta.example", "beta-token"), /label/);
  assert.match(owner.authorize(owner.machines[0].id, "invalid token"), /token/);
  assert.deepEqual([...values], saved);
  assert.equal(owner.machines[0].saved().token, "alpha-token");
});

test("stale pages cannot overwrite unrelated removals or token replacements", async () => {
  const first = restore();
  await first.add("Alpha", "https://alpha.example", "alpha-token");
  await first.add("Beta", "https://beta.example", "beta-token");
  const [alpha, beta] = first.machines;
  const second = restore();
  first.remove(alpha.id);
  first.authorize(beta.id, "replacement-token");
  await second.add("Gamma", "https://gamma.example", "gamma-token");
  const fresh = restore();
  assert.equal(
    fresh.machines.find((machine) => machine.id === alpha.id),
    undefined,
  );
  assert.equal(
    fresh.machines.find((machine) => machine.id === beta.id).saved().token,
    "replacement-token",
  );
  assert.deepEqual(
    fresh.machines.map((machine) => machine.label),
    ["Beta", "Gamma"],
  );
});
