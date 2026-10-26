import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { registerHooks, stripTypeScriptTypes } from "node:module";
import { test } from "node:test";
import { compile, compileModule } from "svelte/compiler";
import { render } from "svelte/server";

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (
      specifier.startsWith("./") &&
      context.parentURL?.includes("/app/src/lib/") &&
      !/\.(?:ts|mjs|js)$/.test(specifier) &&
      !existsSync(new URL(specifier, context.parentURL))
    )
      specifier += ".ts";
    if (specifier === "../../../../sessionapi/protocol") specifier += ".ts";
    return nextResolve(specifier, context);
  },
  load(url, context, nextLoad) {
    if (url.endsWith(".svelte") || url.endsWith(".svelte.ts")) {
      const source = readFileSync(new URL(url), "utf8");
      const options = { filename: new URL(url).pathname, generate: "server" };
      const result = url.endsWith(".svelte")
        ? compile(source, options)
        : compileModule(stripTypeScriptTypes(source), options);
      return { format: "module", source: result.js.code, shortCircuit: true };
    }
    return nextLoad(url, context);
  },
});

const { default: ValueRow } = await import("./ValueRow.svelte");

test("unreached commands render Skipped while retaining their command", () => {
  const value = { kind: "op", verb: "Run", code: "printf unreachable", lang: "bash" };
  const skipped = render(ValueRow, { props: { value: { ...value, skipped: true } } }).body;
  assert.match(skipped, />Skipped</);
  assert.doesNotMatch(skipped, />Run</);
  assert.match(skipped, /printf unreachable/);

  const completed = render(ValueRow, { props: { value } }).body;
  assert.match(completed, />Run</);
  assert.doesNotMatch(completed, />Skipped</);

  const pending = render(ValueRow, { props: { value: { ...value, pending: true } } }).body;
  assert.match(pending, />pending</);
  assert.doesNotMatch(pending, />Skipped</);
});
