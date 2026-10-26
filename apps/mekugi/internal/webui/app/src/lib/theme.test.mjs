import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks, stripTypeScriptTypes } from "node:module";
import { test } from "node:test";
import { compileModule } from "svelte/compiler";

registerHooks({
  load(url, context, nextLoad) {
    if (!url.endsWith("/theme.svelte.ts")) return nextLoad(url, context);
    return {
      format: "module",
      source: compileModule(stripTypeScriptTypes(readFileSync(new URL(url), "utf8")), {
        filename: new URL(url).pathname,
        generate: "client",
      }).js.code,
      shortCircuit: true,
    };
  },
});

let saved = null;
let blocked = false;
let changed;
const media = { matches: false, addEventListener: (_name, listener) => (changed = listener) };
globalThis.matchMedia = () => media;
globalThis.document = { documentElement: { dataset: {} } };
globalThis.localStorage = {
  getItem: () => saved,
  setItem: (_key, value) => {
    if (blocked) throw new Error("Storage unavailable");
    saved = value;
  },
};
const { appearance } = await import("./theme.svelte.ts");
const Appearance = appearance.constructor;
const effective = () => document.documentElement.dataset.theme;

test("System defaults and invalid saved choices follow live system appearance", () => {
  for (saved of [null, "unknown"]) {
    const owner = new Appearance();
    media.matches = false;
    owner.apply();
    assert.equal(owner.choice, "system");
    assert.equal(effective(), "light");
    media.matches = true;
    changed();
    assert.equal(effective(), "dark");
  }
});

test("explicit choices persist, restore, and ignore system changes until System is chosen", () => {
  for (const choice of ["light", "dark"]) {
    appearance.set(choice);
    const restored = new Appearance();
    restored.apply();
    assert.equal(restored.choice, choice);
    assert.equal(effective(), choice);
    media.matches = choice === "light";
    changed();
    assert.equal(effective(), choice);
    restored.set("system");
    assert.equal(saved, "system");
    assert.equal(effective(), media.matches ? "dark" : "light");
  }
});

test("failed storage still switches appearance and a successful save clears the notice", () => {
  blocked = true;
  appearance.set("dark");
  assert.equal(effective(), "dark");
  assert.match(appearance.storageError, /could not be saved/);
  blocked = false;
  appearance.set("light");
  assert.equal(saved, "light");
  assert.equal(appearance.storageError, undefined);
});
