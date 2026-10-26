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

// The agent view follows the page address from the moment it loads.
globalThis.location ??= new URL("http://localhost/");
globalThis.addEventListener ??= () => {};
const { default: EntryView } = await import("./EntryView.svelte");
const { default: ValueRow } = await import("./ValueRow.svelte");

const view = (values, kind = "tool") =>
  render(EntryView, {
    props: { entry: { seq: 1, kind, values }, at: "", stamp: "", owner: "/root" },
  }).body;
const edit = (path, sources) => ({ kind: "op", verb: "Edit", path, sources, effect: true });
const tags = (html, name) => html.split(`>${name}<`).length - 1;

test("deferred JSON retains its formatted disclosure count", () => {
  const html = view([
    { kind: "op", verb: "Run", code: "{", lang: "json", fenced: true, codeLines: 4 },
  ]);
  assert.match(html, /4 lines/);
  assert.doesNotMatch(html, /1 line</);
});

test("deferred replies keep their authored summary and Details disclosure", () => {
  const html = render(EntryView, {
    props: {
      entry: {
        seq: 1,
        agent: "/root/child",
        kind: "reply",
        autoContent: true,
        content: { attachment: "current", kind: "main", revision: 1, seq: 1 },
        values: [
          {
            kind: "message",
            from: "/root/child",
            to: "/root",
            body: "Supporting text.",
            summary: "A **compact summary**.",
          },
        ],
      },
      at: "",
      stamp: "",
      owner: "/root",
    },
  }).body;
  assert.match(html, /<strong>compact summary<\/strong>/);
  assert.match(html, /<summary[^>]*>Details<\/summary>/);
});

test("a run of edits names its shared programs once and keeps each edit's own", () => {
  const html = view([
    { kind: "op", verb: "Run", code: "python3 - <<'PY'", lang: "bash" },
    edit("a.go", ["rm", "python3", "gofmt"]),
    edit("b.go", ["python3", "gofmt"]),
    edit("c.go", ["python3", "gofmt", "rm"]),
  ]);
  assert.match(html, />Via</);
  assert.match(html, /for all 3 files/);
  assert.equal(tags(html, "python3"), 1);
  assert.equal(tags(html, "gofmt"), 1);
  assert.equal(tags(html, "rm"), 2);
});

test("captured edits name their program once across the fold, apart from commands", () => {
  const creates = Array.from({ length: 12 }, (_, i) => ({
    ...edit(`/tmp/tree/file${i}.go`, ["cp"]),
    verb: "Create",
    effect: undefined,
    hiddenEdits: i === 10 ? 2 : undefined,
  }));
  const html = view([
    { kind: "op", verb: "Run", code: "cp -r src /tmp/tree", sources: ["cp"] },
    ...creates,
  ]);
  assert.match(html, /for all 12 files/);
  assert.match(html, /2 more files/);
  assert.equal(tags(html, "cp"), 2);
  assert.doesNotMatch(html, /ml-3 border-l/);
});

test("edits without a shared program keep their own", () => {
  const html = view([edit("a.go", ["rm"]), edit("b.go", ["gofmt"])]);
  assert.doesNotMatch(html, />Via</);
  assert.equal(tags(html, "rm"), 1);
  assert.equal(tags(html, "gofmt"), 1);
});

test("a wait reads as a collaboration line naming its targets", () => {
  const html = view(
    [
      {
        kind: "progress",
        body: "Waiting for",
        waits: [{ name: "/root/detail-tests" }, { name: "/root/review", status: "completed" }],
        waitTimeout: "10m0s",
        live: true,
      },
    ],
    "progress",
  );
  assert.match(html, /animate-blink/);
  assert.match(html, />detail-tests</);
  assert.match(html, />review</);
  assert.match(html, /: completed/);
  assert.match(html, /\(timeout 10m0s\)/);
  assert.doesNotMatch(html, /rounded-card border/);
  assert.doesNotMatch(html, /\/root\//);
});

test("complete one-line output stays visible without a disclosure", () => {
  const value = { kind: "op", verb: "Run", code: "printf ok", tail: ["ok"] };
  const html = render(ValueRow, { props: { value } }).body;
  assert.match(html, /<pre[^>]*>.*ok/s);
  assert.doesNotMatch(html, /aria-label="Output:/);
  for (const extra of [{ tail: ["first", "last"] }, { tailOmitted: 2 }]) {
    const folded = render(ValueRow, { props: { value: { ...value, ...extra } } }).body;
    assert.match(folded, /aria-label="Output:/);
  }
});

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
