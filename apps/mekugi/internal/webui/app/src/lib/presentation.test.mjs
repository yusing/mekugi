import assert from "node:assert/strict";
import { test } from "node:test";
import { markedBlock, toolLabel } from "./markdown.ts";
import { hasDetails, isView, reasoningBody, responseText } from "./kinds.ts";
import { diffRows, workspacePath } from "./diff.ts";
import { progressText } from "./format.ts";

test("response copy keeps complete authored Markdown and excludes other entry kinds", () => {
  const markdown = "## Summary\nAnswer.\n\n## Details\n**Evidence**\n";
  assert.equal(responseText("reply", "preview", markdown), markdown);
  assert.equal(responseText("final", markdown), markdown);
  assert.equal(responseText("text", markdown), markdown);
  for (const kind of ["user", "tool", "reasoning", "start", "progress"])
    assert.equal(responseText(kind, markdown, markdown), "");
  assert.equal(responseText("final", "  \n"), "");
});

test("progress text preserves named wait targets, optional timeout and settlement", () => {
  const labels = { "/root/worker": "worker", "/root/reader": "reader" };
  const label = (name) => labels[name] ?? name;
  const waiting = {
    body: "Waiting for",
    waits: [{ name: "/root/worker" }, { name: "/root/reader", status: "done" }],
    waitTimeout: "5s",
  };
  assert.equal(progressText(waiting, label), "Waiting for worker, reader: done (timeout 5s)");
  assert.equal(
    progressText(
      { ...waiting, body: "Finished waiting for", waitTimeout: undefined, waitReason: "paused" },
      label,
    ),
    "Finished waiting for worker, reader: done: paused",
  );
  assert.equal(
    progressText({ body: "Waiting for", waits: [{ name: "unknown" }] }, label),
    "Waiting for unknown",
  );
});

test("only bound UTF-8 spans become attachment markers", () => {
  const text = "圖 [Image 1] and [Image 1] <script>";
  const html = markedBlock(text, [{ Start: 4, End: 13, Kind: 0 }]);
  assert.equal((html.match(/text-marker-image/g) ?? []).length, 1);
  assert.match(html, /and \[Image 1\] &lt;script&gt;/);
  assert.match(html, /圖 <span[^>]+>\[Image 1\]<\/span>/);
  assert.doesNotMatch(markedBlock(text, [{ Start: 1, End: 13, Kind: 0 }]), /text-marker/);
});

test("bound markers remain escaped inside code and emphasis", () => {
  assert.match(
    markedBlock("`<b>`", [{ Start: 1, End: 4, Kind: 1 }]),
    /<code><span[^>]+>&lt;b&gt;<\/span><\/code>/,
  );
  assert.match(markedBlock("**[Image 1]**", [{ Start: 2, End: 11, Kind: 0 }]), /<strong><span/);
});

test("inline authored content has no duplicate details action", () => {
  assert.equal(reasoningBody("**Checking**\n\n", "Checking"), "");
  assert.equal(
    hasDetails({ kind: "reasoning", values: [{ label: "Checking", body: "**Checking**" }] }),
    false,
  );
  assert.equal(hasDetails({ kind: "tool", values: [{ tail: ["", " "] }] }), false);
  assert.equal(
    hasDetails({
      kind: "reasoning",
      values: [{ label: "Checking", body: "**Checking**\nEvidence" }],
    }),
    false,
  );
  for (const kind of ["user", "text", "final", "reply", "start", "message"])
    assert.equal(
      hasDetails({ kind, text: "Complete text", values: [{ body: "Complete text" }] }),
      false,
    );
  assert.equal(hasDetails({ kind: "tool", values: [{ code: "echo ok", tail: ["ok"] }] }), false);
  assert.equal(
    hasDetails({ kind: "tool", values: [{ code: "{}", detailCode: "{}", fenced: true }] }),
    false,
  );
  assert.equal(
    hasDetails({ kind: "tool", values: [{ detailCode: "original tool arguments" }] }),
    true,
  );
  assert.equal(hasDetails({ kind: "tool", values: [{ tail: ["last"], tailOmitted: 5 }] }), true);
  assert.equal(
    hasDetails({ kind: "tool", values: [{ tail: ["bounded…"], tailClipped: true }] }),
    true,
  );
  assert.equal(
    hasDetails({
      kind: "tool",
      values: [{ code: "python3 - <<'PY'", fenced: true, pending: true, sourceOmitted: true }],
    }),
    true,
  );
  assert.equal(
    hasDetails({ kind: "tool", values: [{ code: "printf ready", fenced: true, pending: true }] }),
    false,
  );
});

test("completed questions have no duplicate details, while hidden choices remain available", () => {
  const value = {
    kind: "op",
    verb: "Asked",
    body: "Which audience?\n└ Developers",
    questions: [{ text: "Which audience?", answer: "Developers" }],
  };
  assert.equal(hasDetails({ kind: "question", values: [value] }), false);
  assert.equal(hasDetails({ kind: "question", values: [{ ...value, sourceOmitted: true }] }), true);
});

test("search patterns and paths retain distinct operands", () => {
  const html = toolLabel("`a|b, c` in `src/a.go` `src/b.go`", "Search");
  assert.match(html, /<code class="text-accent">a\|b, c<\/code>/);
  assert.match(html, /in <code class="tool-operand">src\/a.go<\/code>/);
  assert.equal(
    toolLabel("`cat`, `python3`", "Run"),
    '<code class="tool-operand">cat</code> <code class="tool-operand">python3</code>',
  );
  assert.match(toolLabel("`printf 'a, b'`", "Run"), /a, b/);
});

test("diff rows number each side from its hunk and skip file and operation headers", () => {
  const rows = diffRows(
    'update "x" -> "x"\n' +
      "--- a/x\n+++ b/x\n@@ -1,2 +1,3 @@\n a\n+insert\n b\n@@ -20 +21 @@\n-old\n+new\n\\ No newline at end of file\n",
  );
  assert.deepEqual(rows, [
    { kind: "hunk", text: "@@ -1,2 +1,3 @@" },
    { kind: "context", text: "a", old: 1, new: 1 },
    { kind: "add", text: "insert", new: 2 },
    { kind: "context", text: "b", old: 2, new: 3 },
    { kind: "hunk", text: "@@ -20 +21 @@" },
    { kind: "remove", text: "old", old: 20 },
    { kind: "add", text: "new", new: 21 },
    { kind: "note", text: "No newline at end of file" },
  ]);
});

test("workspace paths drop only their own workspace", () => {
  assert.equal(workspacePath("/project/src/a.go", "/project/"), "src/a.go");
  assert.equal(workspacePath("/project-other/a.go", "/project"), "/project-other/a.go");
  assert.equal(workspacePath("/tmp/a", ""), "/tmp/a");
});

test("only a bound viewed image is a View entry", () => {
  const view = {
    seq: 1,
    kind: "tool",
    images: ["id"],
    values: [{ verb: "View", reads: [{ path: "a.png" }] }],
  };
  assert.ok(isView(view));
  assert.ok(isView({ ...view, values: [{ verb: "Generated", reads: [{ path: "a.png" }] }] }));
  assert.ok(!isView({ ...view, images: [] }));
  assert.ok(!isView({ ...view, values: [{ verb: "Read", reads: [{ path: "a.png" }] }] }));
  assert.ok(!isView({ ...view, kind: "text" }));
});
