import assert from "node:assert/strict";
import { afterEach, beforeEach, test } from "node:test";
import { registerHooks } from "node:module";
registerHooks({
  resolve(specifier, context, nextResolve) {
    return nextResolve(
      specifier === "../../../../sessionapi/protocol" ? `${specifier}.ts` : specifier,
      context,
    );
  },
});
const { Connector, endpointURL, errorText, parseSelection, selectionHash, unknownOutcome } =
  await import("./transport.ts");
const { protocolVersion, subprotocol } = await import("../../../../sessionapi/protocol.ts");

const originalWebSocket = globalThis.WebSocket;
let sockets;
let connectors;
function connect(endpoint, token) {
  const connector = new Connector(endpoint, token);
  connectors.push(connector);
  return connector;
}
let handler;
class Socket extends EventTarget {
  static OPEN = 1;
  readyState = 0;
  bufferedAmount = 0;
  sent = [];
  constructor(url, protocol) {
    super();
    this.url = new URL(url);
    this.protocol = protocol;
    sockets.push(this);
    queueMicrotask(() => {
      if (this.readyState === 0) {
        this.readyState = 1;
        this.dispatchEvent(new Event("open"));
      }
    });
  }
  send(data) {
    assert.equal(this.readyState, 1);
    const message = JSON.parse(data);
    assert.equal(message.jsonrpc, "2.0");
    assert.ok(message.id);
    this.sent.push(message);
    queueMicrotask(() => handler(this, message));
  }
  reply(request, result) {
    this.receive({ jsonrpc: "2.0", id: request.id, result });
  }
  refuse(request, status, outcome = "not_admitted") {
    this.receive({
      jsonrpc: "2.0",
      id: request.id,
      error: { code: -32000, message: "Request refused", data: { status, outcome } },
    });
  }
  receive(message) {
    if (this.readyState === 1)
      this.dispatchEvent(new MessageEvent("message", { data: JSON.stringify(message) }));
  }
  publish(subscription, kind, data) {
    this.receive({
      jsonrpc: "2.0",
      method: "subscription/event",
      params: { subscription, kind, data },
    });
  }
  close() {
    if (this.readyState === 3) return;
    this.readyState = 3;
    queueMicrotask(() => this.dispatchEvent(new Event("close")));
  }
}
function initialize(socket, message) {
  if (message.method !== "initialize") return false;
  assert.equal(socket.protocol, subprotocol);
  assert.equal(message.params.version, protocolVersion);
  socket.reply(message, { version: protocolVersion, serverTime: 1_800_000_000_000 });
  return true;
}
function publication(socket, message, data = { generation: 1 }) {
  assert.ok(message.params.subscription);
  socket.reply(message, { subscription: message.params.subscription });
  socket.publish(message.params.subscription, "snapshot", data);
}
beforeEach(() => {
  sockets = [];
  connectors = [];
  globalThis.WebSocket = Socket;
});
afterEach(() => {
  for (const connector of connectors) connector.close();
  for (const socket of sockets) socket.close();
  globalThis.WebSocket = originalWebSocket;
});

test("connector URLs and viewer addresses preserve explicit composite identity", () => {
  assert.equal(endpointURL("https://example.test/connector/"), "https://example.test/connector");
  for (const input of [
    "/api",
    "file:///etc/passwd",
    "https://user:secret@example.test",
    "https://example.test/?token=secret",
    "https://example.test/#token",
  ])
    assert.throws(() => endpointURL(input));
  const first = { machine: "one", session: "same/id", agent: "/root/child one" };
  assert.deepEqual(parseSelection(selectionHash(first)), first);
  assert.notEqual(selectionHash(first), selectionHash({ ...first, machine: "two" }));
  assert.equal(parseSelection("#thread=same").session, undefined);
});

test("observations pipeline behind initialization while clock and mutation gates remain ordered", async (t) => {
  t.mock.method(performance, "now", () => 100);
  const connector = connect("https://example.test", "private-token");
  const calls = [];
  handler = (socket, message) => calls.push({ socket, message });
  const events = [];
  const observe = (event) => {
    assert.equal(connector.now, 1_800_000_000_000);
    events.push(event);
  };
  const stops = [
    connector.subscribe("session/subscribe", {}, observe, () => {}),
    connector.subscribe("view/attach", { thread: "thread" }, observe, () => {}),
  ];
  const mutation = connector.request("session/interrupt", { thread: "thread" });
  void mutation.catch(() => {});
  await new Promise(setImmediate);
  assert.deepEqual(
    calls.map((call) => call.message.method),
    ["initialize", "session/subscribe", "view/attach"],
  );
  assert.equal(connector.now, undefined);
  const first = calls[0];
  first.socket.reply(first.message, { version: protocolVersion, serverTime: 1_800_000_000_000 });
  // Consume immediate wire-ordered replies, before a promise continuation can
  // initialize the clock or reinterpret an acknowledgement.
  for (const call of calls.slice(1)) publication(call.socket, call.message);
  assert.equal(events.length, 2);
  await new Promise(setImmediate);
  assert.equal(calls.at(-1).message.method, "session/interrupt");
  calls.at(-1).socket.reply(calls.at(-1).message, null);
  await mutation;
  for (const stop of stops) stop();
});

test("queued observations retain initialization refusals and cancellation", async () => {
  for (const failure of ["token", "version", "clock", "cancel"]) {
    const connector = connect(`https://${failure}.test`, "token");
    const calls = [];
    handler = (socket, message) => calls.push({ socket, message });
    const refused = Promise.withResolvers();
    const stop = connector.subscribe(
      "session/subscribe",
      { thread: "thread" },
      () => assert.fail("retired observation published"),
      (state, error) => {
        if (error) refused.resolve({ state, error });
      },
    );
    await new Promise(setImmediate);
    assert.deepEqual(
      calls.map((call) => call.message.method),
      ["initialize", "session/subscribe"],
    );
    const { socket, message } = calls[0];
    if (failure === "cancel") {
      stop();
      socket.reply(message, { version: protocolVersion, serverTime: 1_800_000_000_000 });
      publication(socket, calls[1].message);
      assert.ok(
        socket.sent.some(
          (call) =>
            call.method === "subscription/cancel" &&
            call.params.subscription === calls[1].message.params.subscription,
        ),
      );
    } else {
      if (failure === "token") socket.refuse(message, 401);
      else
        socket.reply(message, {
          version: failure === "version" ? "old" : protocolVersion,
          serverTime: failure === "clock" ? 0 : 1_800_000_000_000,
        });
      const result = await refused.promise;
      assert.equal(
        result.state,
        failure === "token" ? "required" : failure === "version" ? "incompatible" : "lost",
      );
      if (failure === "clock") assert.match(result.error.message, /clock/);
      assert.equal(connector.now, undefined);
      stop();
    }
    connector.close();
  }
});

test("server clock advances monotonically without depending on browser wall time", async (t) => {
  let monotonic = 100;
  t.mock.method(performance, "now", () => monotonic);
  t.mock.method(Date, "now", () => 123);
  handler = (socket, message) => {
    if (!initialize(socket, message)) socket.reply(message, []);
  };
  const connector = connect("https://example.test", "token");
  assert.equal(connector.now, undefined);
  await connector.request("session/list");
  assert.equal(connector.now, 1_800_000_000_000);
  monotonic += 2000;
  assert.equal(connector.now, 1_800_000_002_000);
  const reinitializing = Promise.withResolvers();
  handler = (socket, message) => {
    if (message.method === "initialize") reinitializing.resolve(message);
    else socket.reply(message, []);
  };
  sockets[0].close();
  await Promise.resolve();
  const refreshed = connector.request("session/list");
  const initialization = await reinitializing.promise;
  monotonic += 3000;
  assert.equal(connector.now, 1_800_000_005_000);
  sockets[1].reply(initialization, { version: protocolVersion, serverTime: 1_800_000_005_100 });
  await refreshed;
  assert.equal(connector.now, 1_800_000_005_100);
});

test(
  "one authenticated connection correlates replies and supplied subscription acknowledgements",
  { timeout: 2000 },
  async () => {
    const connector = connect("https://example.test/prefix", "private-token");
    const snapshot = Promise.withResolvers();
    handler = (socket, message) => {
      if (initialize(socket, message)) return;
      if (message.method === "session/subscribe") publication(socket, message, { sessions: [] });
      else socket.reply(message, { ready: true });
    };
    const stop = connector.subscribe("session/subscribe", {}, snapshot.resolve, () => {});
    const event = await snapshot.promise;
    assert.deepEqual(event.data, { sessions: [] });
    assert.deepEqual(await connector.request("session/options"), { ready: true });
    assert.equal(sockets.length, 1);
    assert.equal(sockets[0].url.href, "wss://example.test/prefix/api/control");
    assert.deepEqual(sockets[0].sent[0].params, {
      version: protocolVersion,
      token: "private-token",
    });
    assert.ok(!JSON.stringify(connector).includes("private-token"));
    assert.ok(
      !sockets[0].sent
        .slice(1)
        .some((message) => JSON.stringify(message).includes("private-token")),
    );
    stop();
    assert.equal(sockets[0].sent.at(-1).method, "subscription/cancel");
    assert.equal(sockets[0].sent.at(-1).params.subscription, event.subscription);
    connector.close();
  },
);

test(
  "physical reconnect replaces subscriptions with fresh identities and snapshots",
  { timeout: 4000 },
  async () => {
    const connector = connect("http://example.test", "token");
    const resumed = Promise.withResolvers();
    const received = [];
    handler = (socket, message) => {
      if (initialize(socket, message)) return;
      if (message.method !== "session/subscribe") return;
      publication(socket, message, { generation: sockets.length });
      if (sockets.length === 1) socket.close();
    };
    const stop = connector.subscribe(
      "session/subscribe",
      { thread: "same" },
      (event) => {
        received.push(event);
        if (received.length === 2) resumed.resolve();
      },
      () => {},
    );
    await resumed.promise;
    assert.deepEqual(
      received.map((event) => event.data.generation),
      [1, 2],
    );
    assert.notEqual(received[0].subscription, received[1].subscription);
    stop();
    connector.close();
  },
);

test("content survives the control deadline and remains explicitly cancelable", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const connector = connect("https://example.test", "token");
  const requested = Promise.withResolvers();
  handler = (socket, message) => {
    if (initialize(socket, message)) return;
    if (message.method === "session/content") requested.resolve({ socket, message });
    else socket.reply(message, null);
  };
  const controller = new AbortController();
  const content = connector.request(
    "session/content",
    { thread: "large" },
    { signal: controller.signal },
  );
  const { socket, message } = await requested.promise;
  t.mock.timers.tick(60_000);
  assert.equal(socket.readyState, Socket.OPEN);
  assert.equal(
    socket.sent.some((call) => call.method === "request/cancel"),
    false,
  );
  controller.abort();
  await assert.rejects(content, /canceled/);
  assert.equal(socket.readyState, Socket.OPEN);
  assert.deepEqual(socket.sent.at(-1).params, { body: { id: message.id } });
  await connector.request("session/image", { thread: "large", body: { path: "image.png" } });
  assert.equal(sockets.length, 1);
});

test("timed-out subscription admission cancels its known identity and ignores late data", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const connector = connect("https://example.test", "token");
  const requested = Promise.withResolvers();
  const canceled = Promise.withResolvers();
  const events = [];
  handler = (socket, message) => {
    if (initialize(socket, message)) return;
    if (message.method === "session/subscribe") requested.resolve(message);
    if (message.method === "subscription/cancel") canceled.resolve(message);
  };
  const stop = connector.subscribe(
    "session/subscribe",
    { thread: "large" },
    (event) => events.push(event),
    () => {},
  );
  const request = await requested.promise;
  t.mock.timers.tick(15_000);
  assert.equal((await canceled.promise).params.subscription, request.params.subscription);
  publication(sockets[0], request);
  assert.deepEqual(events, []);
  stop();
});

test(
  "subscription/closed refreshes only that subscription without ending server work",
  { timeout: 4000 },
  async () => {
    const connector = connect("https://example.test", "token");
    const resumed = Promise.withResolvers();
    let publications = 0;
    handler = (socket, message) => {
      if (initialize(socket, message)) return;
      if (message.method !== "session/subscribe") return;
      publication(socket, message);
      if (++publications === 1)
        socket.receive({
          jsonrpc: "2.0",
          method: "subscription/closed",
          params: {
            subscription: message.params.subscription,
            error: {
              code: -32000,
              message: "Slow subscription",
              data: { status: 503, outcome: "not_admitted" },
            },
          },
        });
    };
    let count = 0;
    const stop = connector.subscribe(
      "session/subscribe",
      {},
      () => {
        if (++count === 2) resumed.resolve();
      },
      () => {},
    );
    await resumed.promise;
    assert.equal(sockets.length, 1);
    assert.ok(sockets[0].sent.every((message) => message.method !== "session/end"));
    stop();
    connector.close();
  },
);

test(
  "lost mutation replies are unknown without retries and explicit refusals retain outcome",
  { timeout: 2000 },
  async () => {
    const connector = connect("https://example.test", "token");
    handler = (socket, message) => {
      if (!initialize(socket, message)) socket.close();
    };
    await assert.rejects(
      connector.request("session/input", { thread: "same", body: { text: "once" } }),
      (error) => {
        assert.equal(error.kind, "unknown");
        assert.equal(errorText(error, true), unknownOutcome);
        return true;
      },
    );
    assert.equal(sockets[0].sent.filter((message) => message.method === "session/input").length, 1);
    handler = (socket, message) => {
      if (!initialize(socket, message)) socket.refuse(message, 409);
    };
    await assert.rejects(connector.request("session/input", { thread: "same" }), {
      kind: "refused",
      status: 409,
    });
    handler = (socket, message) => {
      if (!initialize(socket, message)) socket.refuse(message, 503, "unknown");
    };
    await assert.rejects(connector.request("session/input", { thread: "same" }), {
      kind: "unknown",
      status: 503,
    });
    connector.close();
  },
);

test(
  "content, images and attachments reuse one bulk connection independently of control",
  { timeout: 2000 },
  async () => {
    const connector = connect("https://example.test", "token");
    const snapshot = Promise.withResolvers();
    const dispatched = Promise.withResolvers();
    let attachmentRequest;
    handler = (socket, message) => {
      if (initialize(socket, message)) return;
      if (message.method === "view/attach") publication(socket, message);
      else if (message.method === "view/intent") {
        attachmentRequest = { socket, message };
        dispatched.resolve();
      } else socket.reply(message, null);
    };
    const stop = connector.subscribe("view/attach", { thread: "same" }, snapshot.resolve, () => {});
    await snapshot.promise;
    const body = {
      attachment: "view-one",
      version: { Attachment: 1, Revision: 4 },
      operation: {
        composer: {
          input: { text: "", attachments: [{ source: "upload", name: "notes.txt", data: "aGk=" }] },
        },
      },
    };
    const submission = connector.request("view/intent", { thread: "same", body });
    await dispatched.promise;
    assert.equal(sockets.length, 2);
    assert.equal(sockets[1].sent[0].params.token, "token");
    assert.deepEqual(attachmentRequest.message.params.body, body);
    await connector.request("session/interrupt", { thread: "same" });
    assert.equal(sockets[0].sent.at(-1).method, "session/interrupt");
    attachmentRequest.socket.reply(attachmentRequest.message, {
      version: { Attachment: 1, Revision: 5 },
    });
    await submission;
    assert.equal(sockets[1].readyState, 1);
    assert.equal(sockets[0].readyState, 1);
    for (const method of ["session/start", "session/input"]) {
      await connector.request(method, {
        thread: "same",
        body: { text: "", attachments: [{ source: "path", path: "notes.txt" }] },
      });
      assert.equal(sockets[1].sent.at(-1).method, method);
      assert.equal(sockets[1].readyState, 1);
      assert.equal(sockets[0].readyState, 1);
    }
    assert.equal(sockets.length, 2);
    handler = (socket, message) => {
      if (!initialize(socket, message)) socket.reply(message, null);
    };
    for (const [method, payload] of [
      ["session/content", { attachment: "retained", kind: "main", seq: 1, revision: 1 }],
      ["session/attachment-image", { path: "image.png" }],
      ["session/image", { path: "image.png" }],
      [
        "session/answer",
        {
          call: "ask",
          answers: [
            { question: "q", attachments: [{ source: "upload", name: "image.png", data: "aGk=" }] },
          ],
        },
      ],
      [
        "view/intent",
        {
          operation: {
            answers: {
              call: "ask",
              answers: [
                {
                  question: "q",
                  attachments: [{ source: "upload", name: "image.png", data: "aGk=" }],
                },
              ],
            },
          },
        },
      ],
    ]) {
      await connector.request(method, { thread: "same", body: payload });
      assert.equal(sockets[1].sent.at(-1).method, method);
      assert.equal(sockets[1].readyState, 1);
      assert.equal(sockets[0].readyState, 1);
    }
    assert.equal(sockets.length, 2);
    stop();
    connector.close();
  },
);

test("bulk cancellation, loss and reauthorization preserve channel ownership", async () => {
  const connector = connect("https://example.test", "old-token");
  const pending = [];
  handler = (socket, message) => {
    if (initialize(socket, message)) return;
    if (message.method === "session/input" || message.method === "session/content")
      pending.push({ socket, message });
    else socket.reply(message, null);
  };
  await connector.request("session/options");
  const controller = new AbortController();
  const canceled = connector.request("session/content", {}, { signal: controller.signal });
  const retained = connector.request("session/content");
  await new Promise(setImmediate);
  controller.abort();
  await assert.rejects(canceled, { kind: "canceled" });
  pending[1].socket.reply(pending[1].message, { complete: true });
  assert.deepEqual(await retained, { complete: true });
  assert.equal(sockets.length, 2);
  const submission = connector.request("session/input", {
    body: { attachments: [{ source: "path", path: "notes.txt" }] },
  });
  await new Promise(setImmediate);
  sockets[1].close();
  await assert.rejects(submission, { kind: "unknown" });
  await connector.request("session/image");
  assert.equal(sockets.length, 3);
  assert.equal(sockets[0].readyState, Socket.OPEN);
  assert.equal(pending.filter(({ message }) => message.method === "session/input").length, 1);
  connector.authorize("new-token");
  assert.ok(sockets.every((socket) => socket.readyState === 3));
  await Promise.all([connector.request("session/options"), connector.request("session/image")]);
  assert.equal(sockets.length, 5);
  assert.ok(sockets.slice(3).every((socket) => socket.sent[0].params.token === "new-token"));
  connector.close();
  assert.ok(sockets.every((socket) => socket.readyState === 3));
});

test(
  "request cancellation addresses the dispatched request and preserves other machines",
  { timeout: 2000 },
  async () => {
    const first = connect("https://first.test", "one");
    const second = connect("https://second.test", "two");
    const sent = Promise.withResolvers();
    handler = (socket, message) => {
      if (initialize(socket, message)) return;
      if (message.method === "session/input") sent.resolve(message);
      else socket.reply(message, null);
    };
    await second.request("session/list");
    const controller = new AbortController();
    const request = first.request(
      "session/input",
      { thread: "same", body: { text: "once" } },
      { signal: controller.signal },
    );
    const original = await sent.promise;
    controller.abort();
    await assert.rejects(request, { kind: "unknown" });
    const firstSocket = sockets.find((socket) => socket.url.hostname === "first.test");
    assert.deepEqual(firstSocket.sent.at(-1).params, { body: { id: original.id } });
    assert.equal(firstSocket.sent.at(-1).method, "request/cancel");
    first.close();
    assert.equal(sockets.find((socket) => socket.url.hostname === "second.test").readyState, 1);
    first.close();
    second.close();
  },
);

test(
  "credential replacement reconnects only its machine; initialization cancellation dispatches no mutation",
  { timeout: 2000 },
  async () => {
    const first = connect("https://first.test", "expired");
    const second = connect("https://second.test", "two");
    const denied = Promise.withResolvers();
    const admitted = Promise.withResolvers();
    handler = (socket, message) => {
      if (message.method === "initialize" && message.params.token === "expired") {
        socket.refuse(message, 401);
        return;
      }
      if (initialize(socket, message)) return;
      if (message.method === "session/subscribe") publication(socket, message);
      else socket.reply(message, null);
    };
    const stop = first.subscribe("session/subscribe", {}, admitted.resolve, (state) => {
      if (state === "required") denied.resolve();
    });
    await Promise.all([denied.promise, second.request("session/list")]);
    const secondSocket = sockets.find((socket) => socket.url.hostname === "second.test");
    first.authorize("replacement");
    await admitted.promise;
    assert.equal(secondSocket.readyState, 1);
    assert.equal(sockets.filter((socket) => socket.url.hostname === "second.test").length, 1);
    stop();
    first.close();
    second.close();
    const waiting = connect("https://waiting.test", "token");
    const initialization = Promise.withResolvers();
    handler = (_socket, message) => {
      if (message.method === "initialize") initialization.resolve();
    };
    const controller = new AbortController();
    const pending = waiting.request(
      "session/start",
      { body: { text: "once" } },
      { signal: controller.signal },
    );
    await initialization.promise;
    controller.abort();
    await assert.rejects(pending, { kind: "canceled" });
    assert.ok(sockets.at(-1).sent.every((message) => message.method === "initialize"));
    waiting.close();
  },
);

test(
  "incompatible initialization is visible and never resubmits subscriptions",
  { timeout: 2000 },
  async () => {
    const connector = connect("https://example.test", "token");
    const rejected = Promise.withResolvers();
    handler = (socket, message) => socket.refuse(message, 409);
    const stop = connector.subscribe(
      "session/subscribe",
      {},
      () => assert.fail("unexpected publication"),
      (state) => {
        if (state === "incompatible") rejected.resolve();
      },
    );
    await rejected.promise;
    assert.equal(sockets[0].sent.length, 1);
    stop();
    connector.close();
  },
);

test(
  "a mismatched subscription acknowledgement closes its connection before publication",
  { timeout: 2000 },
  async () => {
    const connector = connect("https://example.test", "token");
    const rejected = Promise.withResolvers();
    handler = (socket, message) => {
      if (!initialize(socket, message)) socket.reply(message, { subscription: "different" });
    };
    const stop = connector.subscribe(
      "session/subscribe",
      {},
      () => assert.fail("unexpected publication"),
      (state) => {
        if (state === "lost") rejected.resolve();
      },
    );
    await rejected.promise;
    assert.equal(sockets[0].readyState, 3);
    stop();
    connector.close();
  },
);

test("delivery receipts follow application or stale discard on their originating channel", async () => {
  const connector = connect("https://example.test", "token");
  handler = (socket, message) => {
    if (initialize(socket, message)) return;
    if (message.method === "session/page")
      socket.reply(message, { entries: [], earlier: null, delivery: "page" });
    else if (message.method === "session/subscribe") publication(socket, message);
    else socket.reply(message, null);
  };
  const page = await connector.request("session/page", { thread: "one" });
  assert.equal(
    sockets[0].sent.some((message) => message.method === "publication/ack"),
    false,
  );
  page.acknowledge();
  assert.equal(sockets[0].sent.at(-1).params.body.delivery, "page");
  const stale = await connector.request("session/page", { thread: "one" });
  connector.authorize("replacement");
  await connector.request("session/list");
  stale.acknowledge();
  assert.equal(
    sockets[1].sent.some((message) => message.method === "publication/ack"),
    false,
    "a stale receipt cannot release a new connection's credit",
  );
  const ready = Promise.withResolvers();
  let received = 0;
  const stop = connector.subscribe(
    "session/subscribe",
    { thread: "one" },
    (event) => {
      received++;
      if (event.delivery)
        assert.notEqual(sockets[1].sent.at(-1).params.body?.delivery, event.delivery);
      ready.resolve();
    },
    () => {},
  );
  await ready.promise;
  const subscription = sockets[1].sent.find((message) => message.method === "session/subscribe")
    .params.subscription;
  const event = (delivery) => ({
    jsonrpc: "2.0",
    method: "subscription/event",
    params: { subscription, kind: "patch", data: {}, delivery },
  });
  sockets[1].receive(event("applied"));
  assert.equal(sockets[1].sent.at(-1).params.body.delivery, "applied");
  stop();
  await new Promise(setImmediate);
  const before = received;
  sockets[1].receive(event("discarded"));
  assert.equal(received, before);
  assert.equal(sockets[1].sent.at(-1).params.body.delivery, "discarded");
});
