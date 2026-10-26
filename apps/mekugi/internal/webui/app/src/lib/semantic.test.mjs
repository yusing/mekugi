import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks, stripTypeScriptTypes } from "node:module";
import { afterEach, test } from "node:test";
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
      const source = stripTypeScriptTypes(readFileSync(new URL(url), "utf8"));
      return {
        format: "module",
        source: compileModule(source, { filename: new URL(url).pathname, generate: "client" }).js
          .code,
        shortCircuit: true,
      };
    }
    return nextLoad(url, context);
  },
});
// The selection store uses the browser's navigation primitives at module initialization.
globalThis.location = { hash: "" };
globalThis.addEventListener = () => {};
globalThis.history = { replaceState() {}, pushState() {} };
const { semantic, textDraft } = await import("./semantic.svelte.ts");
const { stream } = await import("./stream.svelte.ts");
const { doing } = await import("./agents.ts");

test("agent labels retain canonical display names before roster metadata arrives", () => {
  assert.equal(stream.label("Main"), "main");
  assert.equal(stream.label("/root"), "main");
  assert.equal(stream.label("/root/worker/reviewer"), "worker/reviewer");
  assert.equal(stream.label("unknown"), "unknown");
});

test("roster activity uses observed progress and collapsed reasoning labels", () => {
  assert.deepEqual(
    doing({ kind: "progress", values: [{ kind: "progress", body: "Connecting to provider" }] }),
    { verb: "", html: "Connecting to provider" },
  );
  assert.deepEqual(
    doing({
      kind: "reasoning",
      values: [
        {
          kind: "summary",
          label: "Checking callers",
          body: "Private to the expanded reasoning view",
          live: true,
        },
      ],
    }),
    { verb: "", html: "Checking callers" },
  );
  assert.deepEqual(
    doing({
      kind: "reasoning",
      values: [{ kind: "summary", body: "A long body\nwith another line", live: true }],
    }),
    { verb: "Thinking", html: "" },
  );
});
const { view } = await import("./view.svelte.ts");
const { prepareAttachments } = await import("./attachments.ts");
const { Connector, unknownOutcome } = await import("./transport.ts");
const tick = () => new Promise((resolve) => setImmediate(resolve));
afterEach(() => {
  semantic.close();
  stream.close();
  Object.assign(view, { machine: undefined, session: undefined, agent: undefined });
});

test("an unavailable thread can reopen with the same identity after leaving its view", () => {
  for (const status of [404, 410]) {
    let connections = 0;
    let detached = 0;
    let publish, state;
    const owner = {
      subscribe(method, params, event, connection) {
        assert.equal(method, "session/subscribe");
        assert.deepEqual(params, { thread: "same-thread" });
        connections++;
        publish = event;
        state = connection;
        return () => detached++;
      },
    };
    stream.connect("same-thread", owner);
    state("lost", { status, message: "Thread unavailable." });
    stream.connect("same-thread", owner);
    assert.equal(connections, 1, "an attached view does not duplicate subscriptions");
    stream.close();
    assert.equal(detached, 1);
    stream.connect("same-thread", owner);
    assert.equal(connections, 2, "explicit reopening creates a fresh subscription");
    assert.equal(stream.gone, undefined);
    state("open");
    publish({ kind: "snapshot", data: { session: { thread: "same-thread" } } });
    assert.equal(stream.connection, "open");
    assert.equal(stream.session.thread, "same-thread");
    stream.close();
  }
});

function machine(id = "machine-one", thread = "thread-one", attachment = "view-one") {
  const requests = [],
    views = [];
  const owner = {
    id,
    requests,
    views,
    subscribe(method, params, event, state) {
      assert.equal(method, "view/attach");
      if (!views.length) assert.deepEqual(params, { thread });
      const subscription = {
        thread: params.thread,
        parent: params.parent,
        stopped: 0,
        event,
        state,
        snapshot(revision = 1, effects = [], extra = {}) {
          event({
            kind: "snapshot",
            data: {
              attachment: params.thread === thread ? attachment : `${params.thread}-view`,
              frame: {
                view: {
                  Input: { ID: subscription.thread },
                  Main: { ID: subscription.thread },
                  Version: { Attachment: 1, Revision: revision },
                },
                effects,
                ...extra,
              },
            },
          });
        },
      };
      views.push(subscription);
      return () => subscription.stopped++;
    },
    request(method, params, options) {
      const reply = Promise.withResolvers();
      requests.push({ method, params, options, ...reply });
      return reply.promise;
    },
    snapshot(revision = 1, effects = []) {
      views[0].snapshot(revision, effects);
    },
    state(value) {
      views[0].state(value);
    },
  };
  semantic.connect(thread, owner);
  owner.state("open");
  owner.snapshot();
  return owner;
}

test("mutations capture originating owner, attachment and version before entering the queue", async () => {
  const owner = machine();
  const first = semantic.send({ command: "/one" });
  const second = semantic.send({ command: "/two" });
  await tick();
  assert.equal(owner.requests.length, 1);
  const request = owner.requests[0];
  assert.equal(request.method, "view/intent");
  assert.deepEqual(request.params, {
    thread: "thread-one",
    body: {
      attachment: "view-one",
      version: { Attachment: 1, Revision: 1 },
      operation: { command: "/one" },
    },
  });
  owner.snapshot(2);
  await tick();
  assert.equal(owner.requests.length, 1, "publication alone must not release the queue");
  request.resolve({ version: { Attachment: 1, Revision: 3 } });
  await tick();
  assert.equal(owner.requests.length, 1, "acknowledgement awaits its version publication");
  owner.snapshot(3);
  assert.equal(await first, undefined);
  await tick();
  assert.equal(owner.requests.length, 2);
  assert.deepEqual(owner.requests[1].params.body.version, { Attachment: 1, Revision: 1 });
  owner.requests[1].resolve({ version: { Attachment: 1, Revision: 3 } });
  assert.equal(await second, undefined);
  assert.equal(semantic.pending, 0);
});

test("admitted child views preserve drafts and route captured input to its own runtime", async () => {
  const owner = machine();
  owner.views[0].snapshot(1, [], { childThreads: ["child"] });
  semantic.editor().text = "main draft";
  const parentEvent = semantic.event;
  semantic.select("child");
  assert.equal(owner.views[1].thread, "child");
  assert.equal(owner.views[1].parent, "thread-one");
  semantic.editor().text = "draft before attachment";
  owner.views[1].snapshot();
  assert.equal(semantic.editor().text, "draft before attachment");
  semantic.editor().text = "child draft";
  const childEvent = semantic.event;
  const childInput = semantic.send({ command: "/child" });
  await tick();
  semantic.select();
  assert.equal(semantic.editor().text, "main draft");
  assert.equal(owner.views[0].stopped, 0);
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
  owner.views[1].snapshot(2);
  assert.equal(await childInput, undefined, "inactive child confirmation remains scoped");
  assert.equal(owner.requests[0].params.thread, "child");
  const prepared = semantic.send({ command: "/prepared" }, undefined, false, childEvent);
  await tick();
  assert.equal(owner.requests[1].params.thread, "child");
  owner.requests[1].resolve({ version: { Attachment: 1, Revision: 2 } });
  assert.equal(await prepared, undefined);
  assert.equal(semantic.event.attachment, parentEvent.attachment);
  semantic.select("historical");
  assert.equal(owner.views.length, 2, "unadmitted history never attaches a runtime");
  semantic.select("child");
  assert.equal(semantic.editor().text, "child draft");
  assert.equal(owner.views.length, 2, "returning to a child reuses its attached view");
  semantic.release("thread-one", owner);
  assert.deepEqual(
    owner.views.map((subscription) => subscription.stopped),
    [1, 1],
  );
});

test("returning to a cached agent view never reapplies its editor effects", async () => {
  const owner = machine();
  const effect = { ID: 1, Restore: textDraft("restored main"), Clipboard: null };
  owner.views[0].snapshot(1, [effect], { childThreads: ["child"] });
  await tick();
  owner.requests[0].resolve({});
  await tick();
  semantic.select("child");
  owner.views[1].snapshot(1, [{ ...effect, Restore: textDraft("restored child") }]);
  await tick();
  owner.requests[1].resolve({});
  await tick();
  semantic.select();
  assert.equal(semantic.editor().text, "restored main");
  semantic.select("child");
  assert.equal(semantic.editor().text, "restored child");
  assert.equal(owner.requests.length, 2, "effect receipts belong to each retained view");
});

test("selected child resumes and switches navigate from the parent page", async () => {
  const owner = machine();
  Object.assign(view, { machine: owner.id, session: "thread-one", agent: "/root/child" });
  owner.views[0].snapshot(1, [], { childThreads: ["child"] });
  semantic.select("child");
  owner.views[1].snapshot(1, [{ ID: 1, Reattach: "resumed", Clipboard: null }]);
  await tick();
  assert.equal(owner.views[2].thread, "resumed");
  owner.views[2].snapshot();
  owner.requests[0].resolve({});
  await tick();
  assert.equal(view.session, "resumed");
  assert.equal(view.agent, undefined);
  semantic.close();

  const second = machine();
  Object.assign(view, { machine: second.id, session: "thread-one", agent: "/root/child" });
  second.views[0].snapshot(1, [], { childThreads: ["child"] });
  semantic.select("child");
  second.views[1].snapshot(2, [], {
    switch: { From: "child", To: "replacement", Origin: "child-view" },
  });
  assert.equal(view.session, "replacement");
  semantic.select("child");
  assert.equal(second.views.length, 3, "the original roster child cannot reuse its replacement");
  semantic.close();
  assert.equal(second.views[1].stopped, 1, "switched runtime views are released");
});

test("active controls do not materialize retained child views", async (t) => {
  const owner = machine();
  const children = Array.from({ length: 128 }, (_, i) => `child-${i}`);
  owner.views[0].snapshot(1, [], { childThreads: children });
  for (const child of children) {
    semantic.select(child);
    owner.views.at(-1).snapshot();
  }
  const values = Map.prototype.values;
  let visited = 0;
  let sent;
  try {
    t.mock.method(Map.prototype, "values", function* () {
      for (const value of values.call(this)) {
        if (value?.parent === "thread-one" && value.lifetime instanceof AbortController) visited++;
        yield value;
      }
    });
    sent = semantic.send({ command: "/active" });
  } finally {
    t.mock.restoreAll();
  }
  assert.equal(visited, 0);
  await tick();
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 1 } });
  assert.equal(await sent, undefined);
});

test("reconnection rejects an attachment captured before asynchronous preparation", async () => {
  const first = machine();
  const captured = semantic.event;
  const controller = new AbortController();
  const preparation = prepareAttachments(
    [{ id: "path", path: "notes.txt" }],
    controller.signal,
    () => {},
  );
  const submission = preparation.then((attachments) =>
    semantic.send(
      { composer: { input: { text: "once", attachments } } },
      controller.signal,
      false,
      captured,
    ),
  );
  first.state("lost");
  const second = machine("machine-two", "thread-two", "view-two");
  assert.match(await submission, /control view changed/);
  assert.equal(first.requests.length, 0);
  assert.equal(second.requests.length, 0);
});

test("unknown mutation outcome is visible once and never retried", async () => {
  const owner = machine();
  const result = semantic.send({ command: "/once" });
  await tick();
  owner.requests[0].reject({ kind: "unknown" });
  assert.equal(await result, unknownOutcome);
  assert.equal(semantic.error, unknownOutcome);
  owner.state("lost");
  owner.state("open");
  owner.snapshot(2);
  await tick();
  assert.equal(owner.requests.length, 1);
});

test("repeated effect publications apply and acknowledge each receipt only once", async () => {
  const owner = machine();
  const effects = [
    { ID: 1, Thread: "thread-one", Selection: { Text: "chosen" }, Clipboard: "copied" },
  ];
  owner.snapshot(2, effects);
  owner.snapshot(3, effects);
  await tick();
  assert.equal(semantic.editor().text, "\nchosen");
  assert.equal(semantic.editor().revision, 1);
  assert.equal(semantic.copied, "copied");
  assert.equal(owner.requests.length, 1);
  assert.deepEqual(owner.requests[0].params.body.operation.receipt, {
    Effect: 1,
    Notice: { Attachment: 0, Claim: "", Feedback: 0, Composer: false, Content: false },
  });
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 4 } });
  await tick();
  assert.equal(semantic.pending, 0, "quiet receipts do not wait for a publication");
});

test("completion replaces its captured text before restoring the selected command", async () => {
  const owner = machine();
  semantic.editor().text = "/";
  const effects = [
    {
      ID: 1,
      Thread: "thread-one",
      Target: semantic.target(),
      Action: { Kind: 2, Start: 0, End: 1 },
      Restore: textDraft("/status "),
    },
  ];
  owner.snapshot(2, effects);
  owner.snapshot(3, effects);
  assert.equal(semantic.editor().text, "/status ");
  assert.equal(semantic.error, undefined);
  await tick();
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 3 } });
});

test("completed files, images and skills keep their text and attach host paths", async () => {
  const owner = machine();
  semantic.editor().text = "see @ma";
  const text = "see @main.go [Image 1] $commit ";
  const effects = [
    {
      ID: 1,
      Thread: "thread-one",
      Target: semantic.target(),
      Action: { Kind: 2, Start: 0, End: 7 },
      Restore: {
        ...textDraft(text),
        Files: [{ Start: 4, End: 12, Path: "main.go", Name: "", Text: "", Source: "" }],
        Images: [{ Start: 13, End: 22, Path: "/w/shot.png", Name: "", Text: "", Source: "" }],
        Skills: [{ Start: 23, End: 30, Path: "/s/commit", Name: "commit", Text: "", Source: "" }],
      },
    },
  ];
  owner.snapshot(2, effects);
  assert.equal(semantic.editor().text, "see @main.go  $commit ");
  assert.deepEqual(
    semantic.editor().items.map((item) => item.path),
    ["main.go", "/w/shot.png"],
  );
  assert.equal(semantic.error, undefined);
  await tick();
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
});

test("retained recovery stays separate from the editor and never retries itself", async () => {
  const owner = machine();
  semantic.editor().text = "new draft";
  const effects = [
    {
      ID: 1,
      Thread: "thread-one",
      Recovery: { preview: "original", attachments: true, shell: false },
    },
    { ID: 2, Thread: "thread-one", Selection: { Text: "later selection" } },
  ];
  owner.snapshot(2, effects);
  owner.snapshot(3, effects);
  await tick();
  assert.equal(semantic.editor().text, "new draft\nlater selection");
  assert.deepEqual(semantic.editor().items, []);
  assert.equal(owner.requests.length, 1);
  assert.equal(owner.requests[0].params.body.operation.receipt.Effect, 2);
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 3 } });
  await tick();
  const retry = semantic.send({ recovery: { effect: 1, action: "retry" } });
  await tick();
  assert.deepEqual(owner.requests[1].params.body.operation, {
    recovery: { effect: 1, action: "retry" },
  });
  owner.requests[1].reject({ kind: "unknown" });
  assert.equal(await retry, unknownOutcome);
  owner.snapshot(4, effects);
  await tick();
  assert.equal(owner.requests.length, 2);
  assert.equal(semantic.editor().text, "new draft\nlater selection");
});

test("switching machines cancels in-flight settlement and drops old queued work", async () => {
  const first = machine();
  const admitted = semantic.send({ command: "/admitted" });
  const queued = semantic.send({ command: "/queued" });
  await tick();
  first.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
  await tick();
  const second = machine("machine-two", "thread-two", "view-two");
  assert.ok(first.requests[0].options.signal.aborted);
  assert.equal(await admitted, unknownOutcome);
  assert.match(await queued, /control view changed/);
  assert.equal(first.requests.length, 1);
  assert.equal(second.requests.length, 0);
  const fresh = semantic.send({ command: "/fresh" });
  await tick();
  assert.equal(second.requests.length, 1);
  assert.equal(second.requests[0].params.thread, "thread-two");
  assert.equal(second.requests[0].params.body.attachment, "view-two");
  second.requests[0].resolve({ version: { Attachment: 1, Revision: 1 } });
  assert.equal(await fresh, undefined);
});

test("failed automatic resume preserves the source draft, attachments and in-flight controls", async () => {
  const owner = machine();
  Object.assign(view, { machine: owner.id, session: "thread-one" });
  const editor = semantic.editor();
  editor.text = "keep this draft";
  editor.items = [{ id: "upload", path: "notes.txt" }];
  const command = semantic.send({ command: "/status" });
  await tick();
  const effects = [{ ID: 1, Reattach: "destination" }];
  owner.snapshot(2, effects);
  const candidate = owner.views[1];
  assert.equal(view.session, "thread-one");
  candidate.state("lost", { message: "Destination unavailable", status: 410 });
  assert.equal(candidate.stopped, 1);
  assert.equal(owner.views[0].stopped, 0);
  assert.equal(semantic.editor(), editor);
  assert.equal(editor.text, "keep this draft");
  assert.deepEqual(editor.items, [{ id: "upload", path: "notes.txt" }]);
  assert.equal(owner.requests[0].options.signal.aborted, false);
  assert.match(semantic.error, /Destination unavailable.*current thread is still open/);
  owner.snapshot(3, effects);
  assert.equal(owner.views.length, 2, "repeated effects do not retry attachment");
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
  assert.equal(await command, undefined);
  await tick();
  owner.requests[1].resolve({ version: { Attachment: 1, Revision: 3 } });
  await tick();
  const next = semantic.send({ command: "/status" });
  await tick();
  assert.equal(owner.requests[2].params.thread, "thread-one");
  owner.requests[2].resolve({ version: { Attachment: 1, Revision: 3 } });
  assert.equal(await next, undefined);
});

test("automatic resume adopts one snapshot subscription after source input and receipts settle", async () => {
  const owner = machine();
  Object.assign(view, { machine: owner.id, session: "thread-one" });
  const source = owner.views[0];
  source.snapshot(2, [{ ID: 1, Reattach: "destination" }]);
  const destination = owner.views[1];
  await tick();
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
  await tick();
  destination.state("open");
  destination.event({
    kind: "patch",
    data: {
      attachment: "destination-view",
      frame: {
        view: {
          Main: { ID: "destination" },
          Input: { ID: "destination" },
          Version: { Attachment: 1, Revision: 1 },
        },
        effects: [],
      },
    },
  });
  assert.equal(view.session, "thread-one", "acknowledgement and patches are not initial snapshots");
  source.snapshot(3, [{ ID: 2, Reattach: "destination" }]);
  destination.snapshot();
  await tick();
  assert.equal(view.session, "thread-one", "the source receipt is still in flight");
  source.snapshot(4, [], { pendingEffects: true });
  owner.requests[1].resolve({ version: { Attachment: 1, Revision: 4 } });
  await tick();
  assert.equal(view.session, "thread-one", "server consequences alone keep the source alive");
  const command = semantic.send({ command: "/status" });
  source.snapshot(5);
  await tick();
  assert.equal(view.session, "thread-one", "ordinary source work alone delays handoff");
  source.snapshot(6, [{ ID: 3, Recovery: { preview: "retained", attachments: true } }]);
  owner.requests[2].resolve({ version: { Attachment: 1, Revision: 6 } });
  await command;
  await tick();
  owner.requests[3].resolve({ version: { Attachment: 1, Revision: 6 } });
  await tick();
  assert.equal(view.session, "thread-one", "receipts do not discard retained recovery");
  const releaseInput = semantic.retainInput();
  source.snapshot(7);
  assert.equal(
    view.session,
    "thread-one",
    "local preparation alone keeps the source component alive",
  );
  releaseInput();
  assert.equal(view.session, "destination");
  assert.equal(source.stopped, 1);
  assert.equal(destination.stopped, 0);
  semantic.release("thread-one", owner);
  semantic.connect("destination", owner);
  assert.equal(owner.views.length, 2, "keyed remount adopts rather than probes and reconnects");
  source.snapshot(8, [{ ID: 4, Restore: textDraft("late source content") }]);
  assert.equal(semantic.editor().text, "");
  destination.snapshot(2, [{ ID: 1, Restore: textDraft("destination input") }]);
  assert.equal(semantic.editor().text, "destination input");
  await tick();
  assert.equal(owner.requests[4].params.thread, "destination");
  assert.equal(owner.requests[4].params.body.attachment, "destination-view");
  owner.requests[4].resolve({ version: { Attachment: 1, Revision: 2 } });
  await tick();
  semantic.release("destination", owner);
  assert.equal(destination.stopped, 1);
});

test("invalid destination snapshots and failed receipts leave the source selected", async () => {
  for (const failure of ["snapshot", "receipt"]) {
    semantic.close();
    const owner = machine();
    Object.assign(view, { machine: owner.id, session: "thread-one" });
    owner.snapshot(2, [{ ID: 1, Reattach: "destination" }]);
    const candidate = owner.views[1];
    if (failure === "snapshot")
      candidate.event({ kind: "snapshot", data: { attachment: "wrong" } });
    else candidate.snapshot();
    await tick();
    if (failure === "receipt") owner.requests[0].reject(new Error("receipt failed"));
    else owner.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
    await tick();
    assert.equal(view.session, "thread-one");
    assert.equal(owner.views[0].stopped, 0);
    assert.equal(candidate.stopped, 1);
    assert.match(semantic.error, /Could not resume/);
  }
});

test("manual navigation cancels a pending resume and ignores its late callbacks", async () => {
  const owner = machine();
  Object.assign(view, { machine: owner.id, session: "thread-one" });
  owner.snapshot(2, [{ ID: 1, Reattach: "destination" }]);
  await tick();
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
  await tick();
  view.session = undefined;
  semantic.release("thread-one", owner);
  owner.views[1].snapshot();
  owner.views[1].state("lost", { message: "late failure" });
  assert.deepEqual(
    owner.views.map((item) => item.stopped),
    [1, 1],
  );
  assert.equal(view.session, undefined);
  assert.equal(semantic.error, undefined);
  assert.equal(semantic.event, undefined);
});

test("saved resume switches routing on the existing subscription across component cleanup", async () => {
  const owner = machine();
  Object.assign(view, { machine: owner.id, session: "thread-one" });
  const source = owner.views[0];
  source.thread = "saved";
  source.snapshot(2, [], { switch: { From: "thread-one", To: "saved", Origin: "view-one" } });
  assert.equal(view.session, "saved");
  semantic.release("thread-one", owner);
  semantic.connect("saved", owner);
  assert.equal(owner.views.length, 1);
  assert.equal(source.stopped, 0);
  const command = semantic.send({ command: "/status" });
  await tick();
  assert.equal(owner.requests[0].params.thread, "saved");
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
  assert.equal(await command, undefined);
});

test("failed source preparation keeps restored input in its usable view", async () => {
  const owner = machine();
  Object.assign(view, { machine: owner.id, session: "thread-one" });
  const release = semantic.retainInput();
  owner.snapshot(2, [{ ID: 1, Reattach: "destination" }]);
  owner.views[1].snapshot();
  await tick();
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
  await tick();
  semantic.editor().text = "restored after preparation failed";
  release("Attachment preparation cancelled.");
  assert.equal(view.session, "thread-one");
  assert.equal(semantic.editor().text, "restored after preparation failed");
  assert.equal(owner.views[0].stopped, 0);
  assert.equal(owner.views[1].stopped, 1);
  assert.match(semantic.error, /Attachment preparation cancelled/);
});

test("an in-place saved resume cancels an older destination acquisition", async () => {
  const owner = machine();
  Object.assign(view, { machine: owner.id, session: "thread-one" });
  owner.snapshot(2, [{ ID: 1, Reattach: "destination" }]);
  await tick();
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
  await tick();
  const source = owner.views[0];
  source.thread = "saved";
  source.snapshot(3, [], { switch: { From: "thread-one", To: "saved", Origin: "view-one" } });
  owner.views[1].snapshot();
  assert.equal(view.session, "saved");
  assert.deepEqual(
    owner.views.map((item) => item.stopped),
    [0, 1],
  );
});

test("saved resume reconnects through the connector to the switched thread", async (t) => {
  const original = globalThis.WebSocket;
  const sockets = [],
    attached = [];
  let serverThread = "thread-one";
  let admission = Promise.withResolvers();
  class Socket extends EventTarget {
    static OPEN = 1;
    readyState = 0;
    bufferedAmount = 0;
    constructor(_url, protocol) {
      super();
      this.protocol = protocol;
      sockets.push(this);
      queueMicrotask(() => {
        this.readyState = 1;
        this.dispatchEvent(new Event("open"));
      });
    }
    receive(message) {
      this.dispatchEvent(
        new MessageEvent("message", { data: JSON.stringify({ jsonrpc: "2.0", ...message }) }),
      );
    }
    send(data) {
      const request = JSON.parse(data);
      queueMicrotask(() => {
        if (request.method === "initialize")
          this.receive({ id: request.id, result: { version: "3" } });
        if (request.method !== "view/attach") return;
        attached.push(request.params.thread);
        this.subscription = request.params.subscription;
        if (request.params.thread === serverThread) {
          this.receive({ id: request.id, result: { subscription: this.subscription } });
          this.publish();
        } else
          this.receive({
            id: request.id,
            error: {
              code: -32000,
              message: "Thread ended",
              data: { status: 404, outcome: "not_admitted" },
            },
          });
        admission.resolve();
      });
    }
    publish(switched) {
      this.receive({
        method: "subscription/event",
        params: {
          subscription: this.subscription,
          kind: "snapshot",
          data: {
            attachment: `view-${sockets.length}`,
            frame: {
              view: {
                Main: { ID: serverThread },
                Input: { ID: serverThread },
                Version: { Attachment: 1, Revision: 2 },
              },
              effects: [],
              switch: switched,
            },
          },
        },
      });
    }
    close() {
      if (this.readyState === 3) return;
      this.readyState = 3;
      queueMicrotask(() => this.dispatchEvent(new Event("close")));
    }
  }
  globalThis.WebSocket = Socket;
  const connector = new Connector("http://fixture.test", "fixture-token");
  t.after(() => {
    semantic.close();
    connector.close();
    globalThis.WebSocket = original;
  });
  const owner = {
    id: "machine",
    subscribe: connector.subscribe.bind(connector),
    request: connector.request.bind(connector),
  };
  Object.assign(view, { machine: owner.id, session: "thread-one" });
  semantic.connect("thread-one", owner);
  await admission.promise;
  serverThread = "saved";
  sockets[0].publish({ From: "thread-one", To: "saved", Origin: "view-1" });
  semantic.release("thread-one", owner);
  semantic.connect("saved", owner);
  admission = Promise.withResolvers();
  connector.authorize("replacement-token");
  await admission.promise;
  assert.deepEqual(attached, ["thread-one", "saved"]);
  assert.equal(view.session, "saved");
  assert.equal(semantic.connection, "open");
  assert.equal(semantic.frame.view.Main.ID, "saved");
});

function menuFrame(owner, menu, revision, target = semantic.editor().revision, model = "model-a") {
  owner.views[0].snapshot(revision, [], {
    view: {
      Input: { ID: "thread-one" },
      Main: { ID: "thread-one" },
      Displayed: { ID: "thread-one" },
      Caption: { Model: model },
      Version: { Attachment: 1, Revision: revision },
      Menu: menu,
    },
    completionTarget: { Call: "", Question: "", Request: "", Revision: target },
  });
}
const cachedMenu = (kind, setting = "") => ({
  ID: 7,
  Kind: kind,
  Setting: setting,
  Loading: false,
  Problem: "",
  Dialog: false,
  Choices: [{ ID: 1, Name: "choice", Path: "" }],
  Preferred: { ID: 1, Name: "choice" },
});

test("settings reopen from cache, refresh identity, and isolate model and connection", async () => {
  const owner = machine();
  const menu = cachedMenu("settings", "/effort");
  menuFrame(owner, menu, 2);
  menuFrame(owner, undefined, 3);
  const opening = semantic.openSetting("/effort");
  assert.deepEqual(semantic.settingsMenu.Choices, menu.Choices);
  assert.equal(semantic.settingsMenu.Loading, true);
  await tick();
  menuFrame(owner, { ...menu, ID: 8 }, 4);
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 4 } });
  await opening;
  assert.equal(semantic.settingsMenu.ID, 8);
  menuFrame(owner, undefined, 5, 0, "model-b");
  semantic.openingSetting = "/effort";
  assert.equal(semantic.settingsMenu, undefined);
  owner.state("lost");
  owner.state("open");
  menuFrame(owner, undefined, 6);
  semantic.openingSetting = "/effort";
  assert.equal(semantic.settingsMenu, undefined);
});

test("completion cache matches text and caret without reusing actionable identities", () => {
  const owner = machine();
  const editor = semantic.editor();
  editor.text = "/";
  const menu = cachedMenu("");
  menuFrame(owner, menu, 2);
  editor.text = "/m";
  editor.revision++;
  assert.equal(semantic.completionMenu, undefined);
  editor.text = "/";
  editor.revision++;
  assert.deepEqual(semantic.completionMenu.Choices, menu.Choices);
  assert.equal(semantic.completionMenu.Loading, true);
  assert.equal(semantic.liveCompletion, undefined);
  editor.cursorBack = 1;
  assert.equal(semantic.completionMenu, undefined);
  editor.cursorBack = 0;
  menuFrame(owner, undefined, 3);
  assert.equal(semantic.completionMenu, undefined, "a current dismissal wins over cached choices");
  owner.state("lost");
  assert.equal(semantic.completionMenu, undefined);
});

test("queued Edit withdraws captured work before opening new retained editors", async () => {
  const owner = machine();
  const original = semantic.frame.view;
  original.Interrupt = { Thread: "thread-one", Turn: "turn-one", Wait: 0, Stop: 0, Start: "" };
  const editing = semantic.editWaiting();
  await tick();
  assert.equal(owner.requests[0].params.body.operation.composer.Intent.Operation, 6);
  assert.deepEqual(
    owner.requests[0].params.body.operation.composer.Intent.Work,
    original.Interrupt,
  );
  owner.snapshot(2, [
    { ID: 1, Thread: "thread-one", Recovery: { preview: "queued", attachments: true } },
  ]);
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
  for (let i = 1; i <= 2; i++) {
    await tick();
    owner.requests[i].resolve({ version: { Attachment: 1, Revision: 2 } });
  }
  await editing;
  assert.ok(owner.requests.some((r) => r.params.body.operation.recovery?.action === "edit"));
  assert.ok(!owner.requests.some((r) => r.params.body.operation.recovery?.action === "retry"));
});

test("Steer names captured work and leaves the editor untouched", async () => {
  const owner = machine();
  const frame = semantic.frame.view;
  frame.Interrupt = { Thread: "thread-one", Turn: "active", Start: "", Wait: "", Stop: "" };
  const editor = semantic.editor("thread-one");
  editor.text = "unsent draft";
  const steering = semantic.steerWaiting();
  await tick();
  const operation = owner.requests[0].params.body.operation;
  assert.equal(operation.composer.Intent.Operation, 10);
  assert.deepEqual(operation.composer.Intent.Work, frame.Interrupt);
  assert.equal(operation.composer.Consumed, false);
  assert.equal(operation.composer.Draft.Text, "");
  owner.requests[0].resolve({ version: frame.Version });
  await steering;
  assert.equal(editor.text, "unsent draft");
});

test("preset recall fills the composer once without submitting", async () => {
  const owner = machine();
  semantic.connect("thread-one", owner);
  owner.snapshot();
  semantic.editor().text = "keep my draft";
  semantic.editor().cursorBack = 4;
  const effects = [{ ID: 1, Thread: "thread-one", Recall: "/clear", Clipboard: null }];
  owner.snapshot(2, effects);
  const revision = semantic.editor().revision;
  owner.snapshot(3, effects);
  await tick();
  const inputs = owner.requests.filter((request) => request.params.body.operation?.composer);
  assert.equal(inputs.length, 0);
  assert.equal(semantic.editor().text, "/clear");
  assert.equal(semantic.editor().cursorBack, 0);
  assert.equal(semantic.editor().revision, revision);
});
