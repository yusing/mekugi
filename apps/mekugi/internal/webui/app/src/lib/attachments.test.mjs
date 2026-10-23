import assert from "node:assert/strict";
import { afterEach, beforeEach, test } from "node:test";
import { prepareAttachments, uploadLimit } from "./attachments.ts";

const originalFileReader = globalThis.FileReader;
let readers;
// A controllable FileReader boundary. Payloads come from actual File bytes;
// events remain held so cancellation and in-flight progress can be observed.
class Reader extends EventTarget {
  result = null;
  readAsDataURL(file) {
    this.file = file;
    readers.push(this);
  }
  progress(loaded) {
    const event = new Event("progress");
    Object.defineProperty(event, "loaded", { value: loaded });
    this.dispatchEvent(event);
  }
  async finish() {
    this.result = `data:${this.file.type};base64,${Buffer.from(await this.file.arrayBuffer()).toString("base64")}`;
    this.dispatchEvent(new Event("load"));
    this.dispatchEvent(new Event("loadend"));
  }
  abort() {
    this.dispatchEvent(new Event("abort"));
    this.dispatchEvent(new Event("loadend"));
  }
}
beforeEach(() => {
  readers = [];
  globalThis.FileReader = Reader;
});
afterEach(() => {
  globalThis.FileReader = originalFileReader;
});
const tick = () => new Promise((resolve) => setImmediate(resolve));

test("mixed File and path inputs retain order, bytes and cumulative progress", async () => {
  const progress = [];
  const result = prepareAttachments(
    [
      { id: "one", path: "before.txt" },
      { id: "two", file: new File(["hi"], "notes.txt", { type: "text/plain" }) },
      { id: "three", path: "middle.txt" },
      { id: "four", file: new File([new Uint8Array([0, 255, 1])], "binary.dat") },
      { id: "five", path: "after.txt" },
    ],
    new AbortController().signal,
    (read, total) => progress.push([read, total]),
  );
  assert.equal(readers.length, 1, "read one file at a time");
  readers[0].progress(1);
  await readers[0].finish();
  await tick();
  assert.equal(readers.length, 2);
  readers[1].progress(1);
  await readers[1].finish();
  assert.deepEqual(await result, [
    { source: "path", path: "before.txt" },
    { source: "upload", name: "notes.txt", data: "aGk=" },
    { source: "path", path: "middle.txt" },
    { source: "upload", name: "binary.dat", data: "AP8B" },
    { source: "path", path: "after.txt" },
  ]);
  assert.deepEqual(progress, [
    [0, 5],
    [1, 5],
    [2, 5],
    [3, 5],
    [5, 5],
  ]);
});

test("32 MiB total is admitted, excess is refused before reading", async () => {
  const controller = new AbortController();
  const boundary = new File([new Uint8Array(uploadLimit)], "boundary.dat");
  const progress = [];
  const result = prepareAttachments(
    [{ id: "one", file: boundary }],
    controller.signal,
    (read, total) => progress.push([read, total]),
  );
  assert.equal(readers.length, 1);
  assert.deepEqual(progress, [[0, 32 * 1024 * 1024]]);
  controller.abort();
  await assert.rejects(result, { name: "AbortError" });
  await assert.rejects(
    prepareAttachments(
      [
        { id: "one", file: boundary },
        { id: "two", file: new File(["x"], "excess.txt") },
      ],
      new AbortController().signal,
      () => assert.fail("oversized input must not report reading"),
    ),
    /32 MiB/,
  );
  assert.equal(readers.length, 1);
});

test("cancellation during reading prevents later files and pre-aborted paths are rejected", async () => {
  const controller = new AbortController();
  const result = prepareAttachments(
    [
      { id: "one", file: new File(["hi"], "first.txt") },
      { id: "two", file: new File(["later"], "second.txt") },
    ],
    controller.signal,
    () => {},
  );
  controller.abort();
  await assert.rejects(result, { name: "AbortError" });
  assert.equal(readers.length, 1);
  await assert.rejects(
    prepareAttachments([{ id: "path", path: "notes.txt" }], controller.signal, () => {}),
    { name: "AbortError" },
  );
});
