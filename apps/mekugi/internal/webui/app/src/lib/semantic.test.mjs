import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks, stripTypeScriptTypes } from "node:module";
import { afterEach, test } from "node:test";
import { compileModule } from "svelte/compiler";
import { derived, get } from "svelte/internal/client";

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
const { Stream, stream } = await import("./stream.svelte.ts");
const { doing } = await import("./agents.ts");

test("agent labels and addresses consume presentation identities before roster metadata arrives", () => {
  const source = new Stream();
  source.main = {
    order: [1],
    entries: {
      1: {
        seq: 1,
        identities: [
          { name: "/root", label: "main", aliases: ["/root", "agent:main"] },
          {
            name: "/root/delegate",
            label: "delegate",
            aliases: ["/root/delegate", "agent:main/delegate", "Main"],
          },
        ],
      },
    },
  };
  assert.equal(source.label("agent:main/delegate"), "delegate");
  assert.equal(source.address("agent:main/delegate"), "/root/delegate");
  assert.equal(source.label("Main"), "delegate");
  assert.equal(source.agent("Main"), undefined);
  source.roster = {
    agents: [
      { name: "/root", label: "main", aliases: ["/root", "agent:main"], state: "working" },
      {
        name: "/root/delegate",
        label: "delegate",
        aliases: ["/root/delegate", "agent:main/delegate", "Main"],
        thread: "delegate-thread",
        state: "working",
      },
    ],
  };
  assert.equal(source.agent("Main")?.thread, "delegate-thread");
  assert.equal(source.agent("agent:main/delegate"), source.agent("/root/delegate"));
  for (const name of ["agent:main/unknown", "agent:mainland/delegate", "delegate"])
    assert.equal(source.label(name), name);
});

test("deferred identity retention follows its feed and historical entry owners", async () => {
  const source = new Stream();
  const attachment = "A";
  let publish;
  source.connect("thread", {
    subscribe(_method, _params, event) {
      publish = event;
      return () => {};
    },
    resource(_kind, { body: ref }) {
      return Promise.resolve({
        entry: {
          seq: ref.seq,
          identities: [
            {
              name: `/root/${ref.kind}-${ref.seq}`,
              label: `${ref.kind}-${ref.seq}`,
              aliases: [`wire-${ref.kind}-${ref.seq}`],
            },
          ],
        },
      });
    },
  });
  const ref = (kind, seq) => ({ attachment, kind, revision: 1, seq });
  publish({
    kind: "snapshot",
    data: {
      session: { thread: "thread" },
      main: { attachment: "A", revision: 1, order: [], entries: [] },
      agents: {
        attachment: "A",
        revision: 1,
        order: [1],
        entries: [{ seq: 1, content: ref("agents", 1) }],
      },
    },
  });
  await source.readContent(ref("agents", 1), new AbortController().signal);
  for (let seq = 1; seq <= 100; seq++) {
    publish({
      kind: "patch",
      data: {
        main: {
          attachment: "A",
          revision: 1,
          order: [seq],
          entries: [{ seq, content: ref("main", seq) }],
        },
      },
    });
    await source.readContent(ref("main", seq), new AbortController().signal);
    assert.equal(Object.keys(source.contentIdentities).length, 2);
    assert.equal(source.label(`wire-main-${seq}`), `main-${seq}`);
    assert.equal(source.label("wire-agents-1"), "agents-1");
    if (seq > 1) assert.equal(source.label(`wire-main-${seq - 1}`), `wire-main-${seq - 1}`);
  }
  source.histories = { "/root/worker": { ...source.agents, loaded: [1] } };
  publish({
    kind: "patch",
    data: { agents: { attachment: "A", revision: 1, order: [], entries: [] } },
  });
  assert.equal(source.label("wire-agents-1"), "agents-1", "paged history keeps its facts");
  publish({
    kind: "patch",
    data: { main: { attachment: "A", revision: 1, order: [], entries: [] } },
  });
  assert.equal(Object.keys(source.contentIdentities).length, 1);
  source.histories = {};
  publish({
    kind: "patch",
    data: { agents: { attachment: "A", revision: 1, order: [], entries: [] } },
  });
  await source.readContent(ref("main", 99), new AbortController().signal);
  assert.equal(
    Object.keys(source.contentIdentities).length,
    0,
    "late reads cannot restore retired facts",
  );
  source.close();
});

test("roster activity uses observed progress and collapsed reasoning labels", () => {
  const labels = { "agent:main/worker/reviewer": "worker/reviewer" };
  const label = (name) => labels[name] ?? name;
  assert.deepEqual(
    doing(
      {
        kind: "progress",
        values: [
          {
            body: "Waiting for",
            kind: "progress",
            waits: [{ name: "agent:main/worker/reviewer" }],
          },
        ],
      },
      label,
    ),
    { verb: "", html: "Waiting for worker/reviewer" },
  );
  assert.deepEqual(
    doing(
      { kind: "progress", values: [{ kind: "progress", body: "Connecting to provider" }] },
      label,
    ),
    { verb: "", html: "Connecting to provider" },
  );
  assert.deepEqual(
    doing(
      {
        kind: "reasoning",
        values: [
          {
            kind: "summary",
            label: "Checking callers",
            body: "Private to the expanded reasoning view",
            live: true,
          },
        ],
      },
      label,
    ),
    { verb: "", html: "Checking callers" },
  );
  assert.deepEqual(
    doing(
      {
        kind: "reasoning",
        values: [{ kind: "summary", body: "A long body\nwith another line", live: true }],
      },
      label,
    ),
    { verb: "Thinking", html: "" },
  );
});
const { view, show } = await import("./view.svelte.ts");
const { prepareAttachments } = await import("./attachments.ts");
const { Connector, TransportError, unknownOutcome } = await import("./transport.ts");
const { protocolVersion } = await import("../../../../sessionapi/protocol.ts");
const tick = () => new Promise((resolve) => setImmediate(resolve));
const nativeQuestion = (ID = "question", Thread = "thread-one") => ({
  ID,
  Thread,
  Sync: false,
  Local: false,
  Turn: "turn",
  Item: "item",
  Questions: [],
  Sent: false,
  Resolved: false,
});
const nativeApproval = (ID = "approval", Thread = "thread-one") => ({
  ID,
  Thread,
  Selected: 0,
  Turn: "turn",
  Item: "item",
  Title: "Approve",
  Subject: "command",
  Outcome: "",
  Details: [],
  Choices: [],
});
afterEach(() => {
  semantic.close();
  stream.close();
  stream.session = undefined;
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

test("ended streams recover only from fresh discovery and an authoritative snapshot", () => {
  const source = new Stream();
  for (const status of [404, 410]) {
    for (const discoveryFirst of [false, true]) {
      const subscriptions = [];
      const owner = {
        sessions: [{ id: "same-thread" }],
        subscribe(method, params, event, state) {
          const subscription = { event, state, stopped: false };
          subscriptions.push(subscription);
          return () => (subscription.stopped = true);
        },
      };
      source.connect("same-thread", owner);
      const first = subscriptions[0];
      first.event({ kind: "snapshot", data: { session: { thread: "same-thread" } } });
      if (discoveryFirst) owner.sessions = [{ id: "same-thread" }];
      first.state("lost", { status, message: "Thread ended." });
      if (!discoveryFirst) {
        source.recover();
        assert.equal(subscriptions.length, 1, "old discovery cannot revive an ended thread");
        owner.sessions = [{ id: "another-thread" }];
        source.recover();
        assert.equal(subscriptions.length, 1, "unrelated discovery cannot retarget the view");
        owner.sessions = [{ id: "same-thread" }];
      }
      source.recover();
      source.recover();
      assert.equal(subscriptions.length, 2, "one subscription per discovery value");
      assert.equal(first.stopped, true);
      assert.equal(source.gone.status, status, "last state stays ended until a snapshot arrives");
      first.event({ kind: "snapshot", data: { session: { thread: "stale" } } });
      assert.equal(source.session.thread, "same-thread");
      const resumed = subscriptions[1];
      const playback = discoveryFirst ? { paused: true, position: 0, until: 100 } : undefined;
      resumed.event({
        kind: "snapshot",
        data: { session: { thread: "same-thread", title: "Resumed" }, playback },
      });
      assert.equal(source.gone, undefined);
      assert.equal(source.session.title, "Resumed");
      assert.deepEqual(source.playback, playback, "a replay replacement stays read-only");
      source.recover();
      assert.equal(subscriptions.length, 2);
      source.close();
      owner.sessions = [{ id: "same-thread" }];
      resumed.state("lost", { status, message: "late closure" });
      source.recover();
      assert.equal(subscriptions.length, 2, "leaving the view prevents recovery");
    }
  }
});

test("a confirmed child attachment retries an unavailable shared read only once", () => {
  for (const attachmentFirst of [false, true]) {
    const source = new Stream();
    const subscriptions = [];
    const owner = {
      sessions: [{ id: "root" }],
      subscribe(_method, _params, event, state) {
        const subscription = { event, state, stopped: false };
        subscriptions.push(subscription);
        return () => (subscription.stopped = true);
      },
    };
    source.connect("child", owner);
    if (attachmentFirst) source.recover("child-view");
    subscriptions[0].state("lost", { status: 404, message: "No such thread." });
    source.recover();
    assert.equal(subscriptions.length, 1, "children are absent from discovery");
    source.recover("child-view");
    source.recover("child-view");
    assert.equal(subscriptions.length, 2);
    assert.equal(subscriptions[0].stopped, true);
    subscriptions[1].state("lost", { status: 404, message: "Thread ended." });
    source.recover("child-view");
    assert.equal(subscriptions.length, 2, "a failed retry remains visible");
    source.close();
    subscriptions[1].state("lost", { status: 404, message: "Late closure" });
    source.recover("another-view");
    assert.equal(subscriptions.length, 2, "leaving prevents reacquisition");
  }
});

test("family navigation keeps only the roster while the selected thread attaches", () => {
  const owner = machine();
  const subscribe = owner.subscribe;
  owner.subscribe = (method, ...args) =>
    method === "session/subscribe" ? () => {} : subscribe(method, ...args);
  const family = {
    root: "thread-one",
    threads: ["thread-one", "child", "sibling"],
    agents: {},
    status: {},
  };
  owner.views[0].snapshot(1, [], { family });
  stream.connect("thread-one", owner);
  stream.roster = { agents: family.threads.map((thread) => ({ name: `/root/${thread}`, thread })) };
  stream.session = { thread: "thread-one", workspace: "/parent" };
  stream.prompts = { questions: [{ id: "parent-question" }] };
  stream.connect("child", owner, true);
  semantic.connect("child", owner);
  assert.equal(stream.roster.agents.length, 3);
  assert.equal(stream.session, undefined);
  assert.equal(stream.prompts, undefined);
  assert.equal(semantic.frame, undefined);
  show("/root/sibling");
  assert.equal(view.session, "sibling", "navigation works before the child view is ready");
  semantic.connect("unrelated", owner);
  assert.equal(semantic.family, undefined);
  stream.connect("unrelated", owner);
  assert.deepEqual(stream.roster.agents, []);
});

const rosterAgent = (name) => ({ name, label: name, state: "working" });
const rosterCursor = (revision) => ({ attachment: "A", kind: "roster", revision, before: 1 });

test("roster revisions retain visible members and recover superseded page reads", async () => {
  const source = new Stream();
  let publish;
  source.connect("thread", {
    subscribe(_method, _params, event) {
      publish = event;
      return () => {};
    },
  });
  const snapshot = (agents, next) =>
    publish({
      kind: "snapshot",
      data: {
        session: { thread: "thread" },
        main: { attachment: "A", revision: 1 },
        roster: { agents, next },
      },
    });
  snapshot([rosterAgent("main"), rosterAgent("worker"), rosterAgent("retired")], null);
  let reject;
  source.page = () =>
    new Promise((_resolve, fail) => {
      reject = fail;
    });
  snapshot([rosterAgent("main")], rosterCursor(2));
  const pending = source.moreRoster();
  snapshot([{ ...rosterAgent("main"), turns: 3 }], rosterCursor(3));
  reject(new TransportError("The roster changed; read its current publication.", "refused", 409));
  await pending;
  assert.deepEqual(
    source.roster.agents.map((row) => row.name),
    ["main", "worker", "retired"],
  );
  assert.equal(source.roster.agents[0].turns, 3);
  assert.equal(source.rosterError, undefined);
  source.page = async () => {
    throw new TransportError("The roster changed; read its current publication.", "refused", 409);
  };
  await source.moreRoster();
  assert.equal(
    source.rosterError,
    undefined,
    "publication races wait for the next cursor without retry chrome",
  );
  snapshot([rosterAgent("main")], rosterCursor(4));
  source.page = async () => ({
    roster: { agents: [rosterAgent("worker"), rosterAgent("new")], next: null },
  });
  await source.moreRoster();
  assert.deepEqual(
    source.roster.agents.map((row) => row.name),
    ["main", "worker", "new"],
    "complete current membership retires old rows",
  );
  publish({
    kind: "snapshot",
    data: {
      session: { thread: "thread" },
      main: { attachment: "B", revision: 1 },
      roster: {
        agents: [rosterAgent("replacement")],
        next: { ...rosterCursor(1), attachment: "B" },
      },
    },
  });
  assert.deepEqual(
    source.roster.agents.map((row) => row.name),
    ["replacement"],
    "attachment replacement cannot retain old membership",
  );
  source.close();
});

test("targeted roster reads recover a revision arriving while their caller is in flight", async () => {
  for (const refused of [false, true]) {
    const source = new Stream();
    const caller = "/root/worker";
    source.roster = { agents: [], next: rosterCursor(1) };
    const reads = [];
    source.page = (cursor) => {
      const reply = Promise.withResolvers();
      reads.push({ cursor, ...reply });
      return reply.promise;
    };
    const pending = source.moreRoster(caller);
    source.roster = { agents: [], next: rosterCursor(2) };
    await source.moreRoster(caller);
    assert.equal(reads.length, 1, "one request per caller remains in flight");
    if (refused) reads[0].reject(new TransportError("Superseded roster", "refused", 409));
    else reads[0].resolve({ roster: { agents: [rosterAgent(caller)] } });
    await tick();
    assert.equal(reads.length, 2, "the superseded request retries the current cursor");
    assert.equal(source.agent(caller), undefined, "stale content stays discarded");
    assert.equal(reads[1].cursor.revision, 2);
    reads[1].resolve({ roster: { agents: [rosterAgent(caller)] } });
    await pending;
    assert.equal(source.agent(caller)?.name, caller);
    assert.equal(source.rosterError, undefined);
    source.close();
  }
});

test("same-thread recovery releases aborted reads without clearing replacement gates", async () => {
  for (const caller of [undefined, "/root/worker"]) {
    const source = new Stream();
    const subscriptions = [],
      requests = [];
    const owner = {
      sessions: [{ id: "same-thread" }],
      subscribe(_method, _params, event, state) {
        subscriptions.push({ event, state });
        return () => {};
      },
      resource(kind, params, options) {
        return this.request(kind, params, options);
      },
      request(method, _params, { signal }) {
        if (signal.aborted) return Promise.reject(signal.reason);
        const reply = Promise.withResolvers();
        requests.push({ method, signal, ...reply });
        return reply.promise;
      },
    };
    const snapshot = {
      kind: "snapshot",
      data: {
        session: { thread: "same-thread" },
        main: { attachment: "A", revision: 1, entries: [{ seq: 1, text: "retained" }] },
        roster: { agents: [], next: { attachment: "A", kind: "roster", revision: 1, before: 40 } },
        deferred: { status: { attachment: "A", kind: "status", revision: 1 } },
      },
    };
    source.connect("same-thread", owner);
    subscriptions[0].event(snapshot);
    const oldReads = [source.moreRoster(caller), source.loadSection("status")];
    await tick();
    assert.equal(requests.length, 2);
    subscriptions[0].state("lost", { status: 410, message: "Thread ended." });
    owner.sessions = [{ id: "same-thread" }];
    source.recover();
    subscriptions[1].event(snapshot);
    assert.equal(source.main.entries[1].text, "retained");
    const newReads = [source.moreRoster(caller), source.loadSection("status")];
    await tick();
    assert.equal(requests.length, 4, "recovery permits fresh reads while canceled replies lag");
    for (const request of requests.slice(0, 2)) {
      assert.equal(request.signal.aborted, true);
      request.resolve({ roster: { agents: [], next: null }, status: { text: "stale" } });
    }
    await Promise.all(oldReads);
    assert.equal(source.sectionLoading.status, true);
    await source.moreRoster(caller);
    await source.loadSection("status");
    assert.equal(requests.length, 4, "stale cleanup leaves replacement requests in flight");
    for (const request of requests.slice(2))
      request.resolve({
        roster: { agents: [{ name: "/root/worker" }], next: null },
        status: { text: "Ready" },
      });
    await Promise.all(newReads);
    assert.equal(source.roster.agents[0].name, "/root/worker");
    assert.equal(source.status.text, "Ready");
    assert.equal(source.rosterLoading, false);
    assert.equal(source.sectionLoading.status, false);
    source.close();
  }
});

test("deferred live reads recover from supersession and retain current failures", async () => {
  for (const replacement of ["conflict", "revision", "attachment", "none"]) {
    const source = new Stream();
    let publish;
    const requests = [];
    source.connect("thread", {
      subscribe(_method, _params, event) {
        publish = event;
        return () => {};
      },
      resource(_kind, params) {
        const reply = Promise.withResolvers();
        requests.push({ reference: params.body, ...reply });
        return reply.promise;
      },
    });
    const ref = { attachment: "A", kind: "live", revision: 1 };
    publish({ kind: "snapshot", data: { session: { thread: "thread" }, deferred: { live: ref } } });
    const read = source.loadSection("live");
    await source.loadSection("live");
    assert.equal(requests.length, 1, "one outstanding read per section");
    const next = {
      ...ref,
      attachment: replacement === "attachment" ? "B" : "A",
      revision: replacement === "revision" || replacement === "conflict" ? 2 : 1,
    };
    if (replacement === "revision" || replacement === "attachment")
      publish({ kind: "patch", data: { deferred: { live: next } } });
    requests[0].reject(
      new TransportError("Read failed", "refused", replacement === "conflict" ? 409 : 503),
    );
    await read;
    assert.equal(source.sectionLoading.live, false);
    assert.equal(source.sectionErrors.live, replacement === "none" ? "Read failed" : "");
    assert.deepEqual(source.live, []);
    assert.equal(requests.length, 1, "conflicts wait for publication rather than spin");
    if (replacement === "conflict") publish({ kind: "patch", data: { deferred: { live: next } } });
    const retry = source.loadSection("live");
    assert.deepEqual(requests[1].reference, next);
    requests[1].resolve({ live: [{ id: "edit", input: "latest source" }] });
    await retry;
    assert.equal(source.live[0].input, "latest source");
    assert.equal(source.deferred.live, undefined);
    assert.equal(source.sectionErrors.live, "");
    source.close();
  }
});

test("role colours survive roster reordering and thread navigation", () => {
  const source = new Stream();
  let publish;
  const owner = {
    subscribe(_method, _params, event) {
      publish = event;
      return () => {};
    },
  };
  source.connect("role-thread", owner);
  const agents = ["worker", "explorer", "worker"].map((role, i) => ({
    name: `/root/${i}`,
    label: role,
    role,
    state: "idle",
  }));
  publish({ kind: "snapshot", data: { roster: { agents } } });
  assert.equal(source.roleColor("worker"), "#5fafff");
  assert.equal(source.roleColor("explorer"), "#ffaf00");
  publish({
    kind: "patch",
    data: { roster: { agents: [{ ...agents[0], role: "review-correctness" }, agents[1]] } },
  });
  for (const thread of ["child-thread", "role-thread"]) {
    source.connect(thread, owner);
    publish({ kind: "snapshot", data: { roster: { agents: [agents[1], agents[0]] } } });
    assert.equal(source.roleColor("worker"), "#5fafff");
    assert.equal(source.roleColor("explorer"), "#ffaf00");
    assert.equal(source.roleColor("review-correctness"), "#af87ff");
  }
  assert.equal(source.roleColor(undefined), undefined);
  source.close();
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
      assert.deepEqual(params, { thread: params.thread });
      const subscription = {
        thread: params.thread,
        stopped: 0,
        event,
        state,
        snapshot(revision = 1, effects = [], extra = {}) {
          event({
            kind: "snapshot",
            data: {
              attachment: params.thread === thread ? attachment : `${params.thread}-view`,
              frame: {
                questions: [],
                approvals: [],
                ...extra,
                view: {
                  Input: { ID: subscription.thread },
                  Main: { ID: subscription.thread },
                  Version: { Attachment: 1, Revision: revision },
                  Questions: { Count: extra.questions?.length ?? 0 },
                  Approvals: { Count: extra.approvals?.length ?? 0 },
                  ...extra.view,
                },
                effects,
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

test("control recovery is independent of publication recovery and retains unsent input", async () => {
  const owner = machine();
  const fresh = structuredClone(semantic.event);
  fresh.attachment = "resumed-view";
  fresh.frame.view.Version.Attachment = 2;
  semantic.editor().text = "keep my draft";
  const pending = semantic.send({ command: "/status" });
  await tick();
  owner.views[0].state("lost", { status: 503, message: "Retrying" });
  owner.sessions = [{ id: "thread-one" }];
  semantic.recover();
  assert.equal(owner.views.length, 1, "ordinary transport retries keep their subscription");
  owner.views[0].state("lost", { status: 404, message: "Thread ended" });
  owner.sessions = [{ id: "another-thread" }];
  semantic.recover();
  assert.equal(owner.views.length, 1, "discovery must name the selected thread");
  owner.sessions = [{ id: "thread-one" }];
  semantic.recover();
  semantic.recover();
  assert.equal(owner.views.length, 2);
  assert.equal(owner.views[0].stopped, 1);
  assert.equal(semantic.editor().text, "keep my draft");
  assert.equal(owner.requests.length, 1, "unresolved input is never resubmitted");
  owner.requests[0].reject(new TransportError("Unknown outcome", "unknown"));
  assert.match(await pending, /Unknown outcome/);
  owner.views[1].event({ kind: "snapshot", data: fresh });
  owner.views[1].state("open");
  assert.equal(semantic.unavailable, false);
  assert.equal(semantic.frame.view.Version.Attachment, 2);
  const command = semantic.send({ command: "/status" });
  await tick();
  assert.equal(owner.requests[1].params.body.attachment, "resumed-view");
  owner.requests[1].resolve({ version: fresh.frame.view.Version });
  assert.equal(await command, undefined);
  semantic.close();
  owner.sessions = [{ id: "thread-one" }];
  semantic.recover();
  assert.equal(owner.views.length, 2, "leaving the view prevents control recovery");
});

test("a replay candidate has no live-view error and retains unsettled source input", () => {
  const owner = machine();
  semantic.editor().text = "source draft";
  const settle = semantic.retainInput();
  semantic.connect("replay", owner);
  owner.views[1].state(
    "lost",
    new TransportError("Recorded playback has no live view.", "refused", 405),
  );
  assert.equal(semantic.connectionError, undefined);
  assert.equal(semantic.event, undefined);
  assert.equal(owner.views[0].stopped, 0);
  assert.equal(owner.views.length, 2);
  settle();
  assert.equal(owner.views[0].stopped, 1);
  semantic.connect("thread-one", owner);
  owner.views.at(-1).snapshot();
  assert.equal(semantic.editor().text, "source draft");
});

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

test("ordinary thread views preserve drafts and captured requests across same-machine navigation", async () => {
  const owner = machine();
  Object.assign(view, { machine: owner.id, session: "thread-one" });
  semantic.editor().text = "main draft";
  view.session = "delegate";
  semantic.release("thread-one", owner);
  semantic.connect("delegate", owner);
  assert.equal(owner.views[1].thread, "delegate");
  semantic.editor().text = "draft before attachment";
  owner.views[1].snapshot();
  assert.equal(semantic.editor().text, "draft before attachment");
  const delegateEvent = semantic.event;
  const releaseDelegate = semantic.retainInput();
  const first = semantic.send({ command: "/first" });
  const queued = semantic.send({ command: "/queued" });
  await tick();
  view.session = "thread-one";
  semantic.release("delegate", owner);
  semantic.connect("thread-one", owner);
  assert.equal(semantic.editor().text, "main draft");
  owner.views[2].snapshot();
  assert.equal(semantic.pending, 0);
  assert.deepEqual(
    owner.views.map((item) => item.stopped),
    [1, 0, 0],
  );
  owner.requests[0].resolve({ version: { Attachment: 1, Revision: 2 } });
  owner.views[1].snapshot(2);
  assert.equal(await first, undefined);
  await tick();
  assert.equal(owner.requests[1].params.thread, "delegate");
  owner.requests[1].resolve({ version: { Attachment: 1, Revision: 2 } });
  assert.equal(await queued, undefined);
  const prepared = semantic.send({ command: "/prepared" }, undefined, false, delegateEvent);
  await tick();
  assert.equal(owner.requests[2].params.thread, "delegate");
  owner.requests[2].resolve({ version: { Attachment: 1, Revision: 2 } });
  assert.equal(await prepared, undefined);
  releaseDelegate();
  assert.equal(owner.views[1].stopped, 1);
  semantic.connect("delegate", owner);
  assert.equal(semantic.editor().text, "draft before attachment");
  assert.equal(owner.views.length, 4);
  owner.views[3].snapshot();
  view.session = undefined;
  semantic.release("delegate", owner);
  assert.deepEqual(
    owner.views.map((item) => item.stopped),
    [1, 1, 1, 1],
  );
});

test("idle source retirement waits for a valid destination snapshot and restores drafts on reattach", () => {
  const owner = machine();
  semantic.editor().text = "root draft";
  semantic.connect("delegate", owner);
  const delegate = owner.views[1];
  delegate.event({
    kind: "patch",
    data: {
      attachment: "delegate-view",
      frame: {
        view: {
          Main: { ID: "delegate" },
          Input: { ID: "delegate" },
          Version: { Attachment: 1, Revision: 1 },
        },
        effects: [],
      },
    },
  });
  assert.equal(owner.views[0].stopped, 0, "a patch cannot establish the destination");
  delegate.event({ kind: "snapshot", data: { attachment: "malformed" } });
  assert.equal(owner.views[0].stopped, 0, "a malformed snapshot cannot retire the source");
  delegate.state("lost", { message: "unavailable", status: 410 });
  assert.equal(owner.views[0].stopped, 0, "failed destination admission keeps the source alive");
  semantic.connect("thread-one", owner);
  assert.equal(semantic.editor().text, "root draft");
  assert.equal(owner.views.length, 2);
  semantic.connect("sibling", owner);
  owner.views[2].snapshot();
  assert.equal(owner.views[0].stopped, 1, "the original root has no permanent lifetime exception");
  owner.views[0].snapshot(2, [{ ID: 1, Restore: textDraft("late root effect") }]);
  assert.equal(semantic.frame.view.Main.ID, "sibling");
  semantic.connect("thread-one", owner);
  owner.views[3].snapshot();
  assert.equal(semantic.editor().text, "root draft");
});

test("the reactive composer owns destination input while its first snapshot is pending", () => {
  const owner = machine();
  semantic.editor().text = "source draft";
  owner.state("connecting");
  const composer = derived(() => semantic.editor());
  assert.equal(get(composer).text, "source draft");
  semantic.connect("delegate", owner);
  get(composer).text = "draft while attaching";
  owner.views[1].snapshot();
  assert.equal(get(composer).text, "draft while attaching");
  semantic.connect("thread-one", owner);
  assert.equal(get(composer).text, "source draft");
});

test("inactive native questions and approvals retain their source until both resolve", () => {
  const owner = machine();
  owner.views[0].snapshot(2, [], { questions: [nativeQuestion()], approvals: [nativeApproval()] });
  semantic.prompt("question").drafts.answer = { choice: undefined, text: "retained answer" };
  semantic.connect("sibling", owner);
  owner.views[1].snapshot();
  assert.equal(owner.views[0].stopped, 0);
  semantic.connect("thread-one", owner);
  assert.equal(semantic.prompt("question").drafts.answer.text, "retained answer");
  semantic.connect("sibling", owner);
  owner.views.at(-1).snapshot();
  owner.views[0].snapshot(3, [], { approvals: [nativeApproval()] });
  assert.equal(owner.views[0].stopped, 0, "an unresolved approval still retains its source");
  owner.views[0].snapshot(4);
  assert.equal(owner.views[0].stopped, 1, "resolved native prompts clear nonsending cached drafts");
  semantic.connect("thread-one", owner);
  owner.views.at(-1).snapshot();
  assert.deepEqual(semantic.prompt("question").drafts, {});
});

test("a captured prompt reply retains its resolved source until sending finishes", () => {
  const owner = machine();
  owner.views[0].snapshot(2, [], { questions: [nativeQuestion()] });
  const prompt = semantic.prompt("question");
  prompt.drafts.answer = { choice: undefined, text: "pending reply" };
  prompt.sending = true;
  const release = semantic.retainInput();
  semantic.connect("sibling", owner);
  owner.views[1].snapshot();
  owner.views[0].snapshot(3);
  assert.equal(prompt.resolved, true);
  assert.equal(prompt.drafts.answer.text, "pending reply");
  assert.equal(owner.views[0].stopped, 0);
  prompt.sending = false;
  release();
  assert.equal(owner.views[0].stopped, 1);
  semantic.connect("thread-one", owner);
  owner.views[2].snapshot();
  assert.deepEqual(semantic.prompt("question").drafts, {});
});

test("unacknowledged editor effects retain their source until its receipt settles", async () => {
  const owner = machine();
  owner.snapshot(2, [{ ID: 1, Restore: textDraft("restored source"), Clipboard: null }]);
  await tick();
  semantic.connect("sibling", owner);
  owner.views[1].snapshot();
  assert.equal(owner.views[0].stopped, 0);
  assert.equal(owner.requests[0].params.thread, "thread-one");
  owner.requests[0].resolve({});
  await tick();
  assert.equal(owner.views[0].stopped, 1);
  semantic.connect("thread-one", owner);
  owner.views[2].snapshot();
  assert.equal(semantic.editor().text, "restored source");
  assert.equal(owner.requests.length, 1, "retired receipts do not replay on a fresh attachment");
});

test("inactive server consequences and recovery keep their source until published settlement", () => {
  const owner = machine();
  owner.views[0].snapshot(2, [], { pendingEffects: true });
  semantic.connect("sibling", owner);
  owner.views[1].snapshot();
  assert.equal(owner.views[0].stopped, 0);
  owner.views[0].snapshot(3, [{ ID: 1, Recovery: { preview: "recover me", attachments: true } }]);
  assert.equal(owner.views[0].stopped, 0, "retained recovery is unsettled source work");
  owner.views[0].snapshot(4);
  assert.equal(owner.views[0].stopped, 1);
  assert.equal(semantic.frame.view.Main.ID, "sibling");
});

test("inactive refusals retire while unknown mutation outcomes remain visible on return", async () => {
  for (const message of [
    "Refused",
    unknownOutcome,
    `Server admission uncertain. ${unknownOutcome}`,
  ]) {
    const unknown = message !== "Refused";
    semantic.close();
    const owner = machine();
    const result = semantic.send({ command: "/status" });
    await tick();
    owner.requests[0].reject(new TransportError(message, unknown ? "unknown" : "refused"));
    await result;
    semantic.connect("sibling", owner);
    owner.views[1].snapshot();
    assert.equal(owner.views[0].stopped, unknown ? 0 : 1);
    semantic.connect("thread-one", owner);
    if (unknown) {
      assert.equal(semantic.error, message);
      assert.equal(owner.views.length, 2);
    } else {
      owner.views[2].snapshot();
      assert.equal(semantic.error, undefined);
    }
  }
});

test("nonattachable roster members cannot select live or recorded activity", () => {
  const previousPlayback = stream.playback;
  Object.assign(view, { machine: "machine-one", session: "root", agent: undefined });
  stream.roster = {
    agents: [{ name: "/root/worker", thread: "claude", nonAttachable: true }],
  };
  for (const playback of [undefined, { paused: true }]) {
    stream.playback = playback;
    show("/root/worker");
    assert.equal(view.session, "root");
    assert.equal(view.agent, undefined);
  }
  stream.playback = previousPlayback;
});

test("delegate, sibling and Main navigation retires idle views while preserving independent drafts", () => {
  const owner = machine("machine-one", "delegate");
  assert.equal(owner.views.length, 1, "cold selection needs only its ordinary thread attachment");
  stream.session = { thread: "delegate", caller: "/root/delegate" };
  Object.assign(view, { machine: owner.id, session: "delegate" });
  const family = {
    root: "main-thread",
    threads: ["main-thread", "delegate", "sibling"],
    agents: {},
    status: {},
  };
  stream.roster = {
    agents: [
      { name: "/root", thread: "main-thread", label: "Main", state: "working" },
      { name: "/root/delegate", thread: "delegate", label: "delegate", state: "working" },
      { name: "/root/sibling", thread: "sibling", label: "sibling", state: "working" },
    ],
  };
  owner.views[0].snapshot(2, [], { family });
  semantic.editor().text = "delegate draft";
  assert.equal(stream.agent("/root/sibling")?.thread, "sibling");
  assert.equal(semantic.frame.family.root, "main-thread");
  show("/root/sibling");
  assert.equal(view.session, "sibling");
  assert.equal(view.agent, undefined);
  semantic.connect(view.session, owner);
  owner.views[1].snapshot(1, [], { family });
  semantic.editor().text = "sibling draft";
  show("Main");
  assert.equal(view.session, "main-thread");
  semantic.connect(view.session, owner);
  owner.views[2].snapshot(1, [], { family });
  semantic.editor().text = "main draft";
  show("/root/delegate");
  assert.equal(view.session, "delegate");
  semantic.connect(view.session, owner);
  owner.views.at(-1).snapshot(1, [], { family });
  assert.equal(semantic.editor().text, "delegate draft");
  show("/root/sibling");
  assert.equal(view.session, "sibling");
  assert.equal(view.agent, undefined);
  semantic.connect(view.session, owner);
  owner.views.at(-1).snapshot(1, [], { family });
  assert.equal(semantic.editor().text, "sibling draft");
  show(undefined);
  assert.equal(view.session, "main-thread");
  semantic.connect(view.session, owner);
  owner.views.at(-1).snapshot(1, [], { family });
  assert.equal(semantic.editor().text, "main draft");
  assert.deepEqual(
    owner.views.map((item) => item.thread),
    ["delegate", "sibling", "main-thread", "delegate", "sibling", "main-thread"],
  );
  assert.deepEqual(
    owner.views.map((item) => item.stopped),
    [1, 1, 1, 1, 1, 0],
  );
});

test("historical agents and replay keep the selected thread and use recorded agent routing", (t) => {
  const owner = machine("machine-one", "delegate");
  Object.assign(view, { machine: owner.id, session: "delegate" });
  owner.views[0].snapshot(2, [], {
    family: { root: "main", threads: ["main", "delegate", "sibling"], agents: {}, status: {} },
  });
  stream.roster = {
    agents: [
      { name: "/root/old", thread: "historical", label: "old", state: "done" },
      { name: "/root/sibling", thread: "sibling", label: "sibling", state: "working" },
    ],
  };
  show("/root/old");
  assert.equal(view.session, "delegate");
  assert.equal(view.agent, "/root/old");
  stream.playback = { position: 1, until: 10, speed: 1, paused: true };
  t.after(() => {
    stream.playback = undefined;
  });
  show("/root/sibling");
  assert.equal(view.session, "delegate");
  assert.equal(view.agent, "/root/sibling");
  assert.equal(owner.views.length, 1);
});

test("returning to an unavailable family Main attempts its original thread and surfaces refusal", () => {
  const owner = machine("machine-one", "delegate");
  Object.assign(view, { machine: owner.id, session: "delegate" });
  owner.views[0].snapshot(2, [], {
    family: { root: "original-main", threads: ["delegate"], agents: {}, status: {} },
  });
  semantic.editor().text = "delegate draft";
  show("/root");
  assert.equal(view.session, "original-main");
  assert.equal(view.agent, undefined);
  semantic.release("delegate", owner);
  stream.connect(view.session, {
    subscribe(method, params, _event, state) {
      assert.equal(method, "session/subscribe");
      assert.equal(params.thread, "original-main");
      state("lost", { status: 410, message: "Original thread unavailable." });
      return () => {};
    },
  });
  assert.equal(stream.gone.text, "Original thread unavailable.");
  assert.equal(semantic.editor().text, "delegate draft");
  assert.equal(owner.views[0].stopped, 0);
});

test("prompt drafts and outcomes are thread-local and prune only after sending settles", () => {
  const owner = machine();
  owner.views[0].snapshot(2, [], { questions: [nativeQuestion("same-prompt")] });
  const main = semantic.prompt("same-prompt");
  Object.assign(main, {
    drafts: { question: { choice: 2, text: "main answer" } },
    reason: "main reason",
    accepted: true,
    error: "main outcome",
  });
  semantic.connect("sibling", owner);
  owner.views[1].snapshot();
  const sibling = semantic.prompt("same-prompt");
  assert.notEqual(sibling, main);
  assert.deepEqual(sibling, { drafts: {}, reason: "", sending: false, accepted: false });
  sibling.drafts.question = { choice: "skip", text: "sibling answer" };
  sibling.sending = true;
  owner.views[1].snapshot(2);
  assert.deepEqual(semantic.prompt("same-prompt"), sibling, "in-flight replies retain their state");
  semantic.connect("thread-one", owner);
  assert.deepEqual(semantic.prompt("same-prompt"), main);
  assert.equal(main.drafts.question.text, "main answer");
  assert.equal(main.accepted, true);
  assert.equal(main.error, "main outcome");
  owner.views[0].snapshot(3, [], { questions: [{ ...nativeQuestion("same-prompt"), Sent: true }] });
  assert.deepEqual(
    semantic.prompt("same-prompt"),
    main,
    "sent but unresolved answers remain owned",
  );
  owner.views[0].snapshot(4);
  assert.deepEqual(
    semantic.prompt("same-prompt"),
    { drafts: {}, reason: "", sending: false, accepted: false },
    "resolved prompts discard cached outcomes",
  );
  semantic.connect("sibling", owner);
  assert.deepEqual(semantic.prompt("same-prompt"), sibling);
  assert.equal(sibling.drafts.question.text, "sibling answer");
  sibling.sending = false;
  owner.views[1].snapshot(3);
  assert.deepEqual(semantic.prompt("same-prompt"), {
    drafts: {},
    reason: "",
    sending: false,
    accepted: false,
  });
});

test("selected connection, operation errors and retained recovery stay scoped to their source", async () => {
  const owner = machine();
  const failed = semantic.send({ command: "/unknown" });
  await tick();
  owner.requests[0].reject({ kind: "unknown" });
  assert.equal(await failed, unknownOutcome);
  owner.snapshot(2, [{ ID: 1, Recovery: { preview: "main recovery", attachments: true } }]);
  semantic.connect("sibling", owner);
  owner.views[1].snapshot();
  assert.equal(semantic.error, undefined);
  assert.deepEqual(semantic.frame.effects, []);
  owner.views[0].state("lost", { message: "main disconnected" });
  assert.equal(semantic.connection, "open");
  assert.equal(semantic.connectionError, undefined);
  semantic.connect("thread-one", owner);
  assert.equal(semantic.connection, "lost");
  assert.equal(semantic.connectionError, "main disconnected");
  assert.equal(semantic.error, unknownOutcome);
  owner.views[0].state("open");
  owner.snapshot(3, [{ ID: 1, Recovery: { preview: "main recovery", attachments: true } }]);
  assert.equal(semantic.frame.effects[0].Recovery.preview, "main recovery");
  semantic.connect("sibling", owner);
  owner.views.at(-1).snapshot();
  assert.equal(semantic.error, undefined);
  assert.equal(semantic.connectionError, undefined);
  assert.deepEqual(semantic.frame.effects, []);
});

test("returning to a cached agent view never reapplies its editor effects", async () => {
  const owner = machine();
  const effect = { ID: 1, Restore: textDraft("restored main"), Clipboard: null };
  owner.views[0].snapshot(1, [effect], {
    family: { root: "thread-one", threads: ["thread-one", "child"], agents: {}, status: {} },
  });
  await tick();
  owner.requests[0].resolve({});
  await tick();
  const releaseMain = semantic.retainInput();
  semantic.connect("child", owner);
  owner.views[1].snapshot(1, [{ ...effect, Restore: textDraft("restored child") }]);
  await tick();
  owner.requests[1].resolve({});
  await tick();
  const releaseChild = semantic.retainInput();
  semantic.editor().text = "edited child";
  semantic.connect("thread-one", owner);
  assert.equal(semantic.editor().text, "restored main");
  semantic.connect("child", owner);
  assert.equal(semantic.editor().text, "edited child");
  assert.equal(owner.requests.length, 2, "effect receipts belong to each retained view");
  owner.views[0].snapshot(2, [{ ...effect, ID: 2, Restore: textDraft("pending main effect") }]);
  assert.equal(semantic.editor().text, "edited child");
  assert.equal(semantic.frame.view.Main.ID, "child");
  semantic.connect("thread-one", owner);
  assert.equal(semantic.editor().text, "pending main effect\nrestored main");
  await tick();
  assert.equal(owner.requests[2].params.thread, "thread-one");
  owner.requests[2].resolve({});
  await tick();
  semantic.connect("child", owner);
  assert.equal(semantic.editor().text, "edited child");
  releaseMain();
  assert.equal(owner.views[0].stopped, 1);
  releaseChild();
});

test("selected ordinary threads resume and switch their own URL selection", async () => {
  const owner = machine();
  Object.assign(view, { machine: owner.id, session: "child" });
  owner.views[0].snapshot(1, [], {
    family: { root: "thread-one", threads: ["thread-one", "child"], agents: {}, status: {} },
  });
  semantic.connect("child", owner);
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
  Object.assign(view, { machine: second.id, session: "child" });
  second.views[0].snapshot(1, [], {
    family: { root: "thread-one", threads: ["thread-one", "child"], agents: {}, status: {} },
  });
  semantic.connect("child", second);
  second.views[1].snapshot(2, [], {
    switch: { From: "child", To: "replacement", Origin: "child-view" },
  });
  assert.equal(view.session, "replacement");
  semantic.connect("child", second);
  assert.equal(second.views.length, 3, "the original roster child cannot reuse its replacement");
  semantic.close();
  assert.equal(second.views[1].stopped, 1, "switched runtime views are released");
});

test("active controls do not scan retained ordinary views", async (t) => {
  const owner = machine();
  const children = Array.from({ length: 128 }, (_, i) => `child-${i}`);
  for (const child of children) {
    semantic.connect(child, owner);
    owner.views.at(-1).snapshot(1, [], { questions: [nativeQuestion("question", child)] });
  }
  assert.ok(owner.views.slice(1).every((subscription) => subscription.stopped === 0));
  const values = Map.prototype.values;
  let visited = 0;
  let sent;
  try {
    t.mock.method(Map.prototype, "values", function* () {
      for (const value of values.call(this)) {
        if (value?.lifetime instanceof AbortController) visited++;
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
    {
      ID: 1,
      Thread: "thread-one",
      Selection: { Description: "Answer", Text: "chosen", Source: "source.go" },
      Clipboard: "copied",
    },
  ];
  owner.snapshot(2, effects);
  owner.snapshot(3, effects);
  await tick();
  assert.equal(semantic.editor().text, "[Selected Answer] ");
  assert.equal(semantic.editor().items.length, 1);
  assert.equal(semantic.editor().items[0].reference, "source.go");
  assert.equal(semantic.editor().items[0].file.name, "Answer");
  assert.equal(await semantic.editor().items[0].file.text(), "chosen");
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

test("a late reference refuses a changed draft and never reaches another thread", async () => {
  const owner = machine();
  semantic.editor().text = "newer draft";
  semantic.editor().revision = 2;
  owner.snapshot(2, [
    { ID: 1, Thread: "thread-one", Target: { Revision: 1 }, Selection: { Text: "old selection" } },
  ]);
  assert.equal(semantic.editor().text, "newer draft");
  assert.deepEqual(semantic.editor().items, []);
  assert.match(semantic.error, /draft changed/);
  owner.snapshot(3, [
    {
      ID: 2,
      Thread: "other-thread",
      Target: { Revision: 0 },
      Selection: { Text: "other selection" },
    },
  ]);
  assert.deepEqual(semantic.editor().items, []);
  assert.equal(await semantic.editors["other-thread"].items[0].file.text(), "other selection");
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

test("references and completed presets retain the middle caret and surrounding Unicode text", () => {
  const owner = machine();
  const editor = semantic.editor();
  editor.text = "日本語 Why this?";
  editor.cursorBack = 5;
  owner.snapshot(2, [
    {
      ID: 1,
      Thread: "thread-one",
      Target: semantic.target(),
      Selection: { Description: "dialog", Text: "quote", Source: "file.go" },
    },
  ]);
  assert.equal(editor.text, "日本語 Why [Selected dialog] this?");
  assert.equal(editor.cursorBack, 5);
  const before = editor.text + " $review after";
  editor.text = before;
  const expanded = before.replace("$review", "Review carefully.");
  owner.snapshot(3, [
    {
      ID: 2,
      Thread: "thread-one",
      Target: semantic.target(),
      Action: { Kind: 2, Start: 0, End: new TextEncoder().encode(before).length },
      Restore: textDraft(expanded, 6),
    },
  ]);
  assert.equal(editor.text, expanded);
  assert.equal(editor.cursorBack, 6);
  assert.equal(editor.items.length, 1, "preset text creates no attachment and retains the quote");
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
  assert.equal(semantic.editor().text, "new draft[Selected output] ");
  assert.equal(await semantic.editor().items[0].file.text(), "later selection");
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
  assert.equal(semantic.editor().text, "new draft[Selected output] ");
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

test("an inactive receipt failure stays with its source while another thread resumes", async () => {
  const owner = machine();
  Object.assign(view, { machine: owner.id, session: "thread-one" });
  owner.snapshot(2, [{ ID: 1, Restore: textDraft("source draft") }]);
  await tick();
  semantic.connect("sibling", owner);
  view.session = "sibling";
  owner.views[1].snapshot(1, [{ ID: 1, Reattach: "destination" }]);
  await tick();
  owner.requests[0].reject(new TransportError("Receipt refused", "refused"));
  await tick();
  assert.equal(owner.views[2].stopped, 0);
  assert.equal(semantic.error, undefined);
  owner.requests[1].resolve({});
  await tick();
  owner.views[2].snapshot();
  assert.equal(view.session, "destination");
  semantic.connect("thread-one", owner);
  assert.equal(semantic.error, "Receipt refused");
  assert.equal(semantic.editor().text, "source draft");
});

test("automatic resume adopts one snapshot subscription after source input and receipts settle", async () => {
  const owner = machine();
  Object.assign(view, { machine: owner.id, session: "thread-one" });
  const source = owner.views[0];
  const releaseSource = semantic.retainInput();
  semantic.connect("sibling", owner);
  const sibling = owner.views[1];
  sibling.snapshot();
  semantic.editor().text = "sibling draft";
  semantic.connect("thread-one", owner);
  releaseSource();
  source.snapshot(2, [{ ID: 1, Reattach: "destination" }]);
  const destination = owner.views[2];
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
  assert.equal(owner.views.length, 3, "keyed remount adopts rather than probes and reconnects");
  source.snapshot(8, [{ ID: 4, Restore: textDraft("late source content") }]);
  assert.equal(semantic.editor().text, "");
  destination.snapshot(2, [{ ID: 1, Restore: textDraft("destination input") }]);
  assert.equal(semantic.editor().text, "destination input");
  await tick();
  assert.equal(owner.requests[4].params.thread, "destination");
  assert.equal(owner.requests[4].params.body.attachment, "destination-view");
  owner.requests[4].resolve({ version: { Attachment: 1, Revision: 2 } });
  await tick();
  semantic.connect("sibling", owner);
  assert.equal(semantic.editor().text, "sibling draft");
  assert.equal(sibling.stopped, 1, "idle sibling views retire while their editor draft survives");
  owner.views.at(-1).snapshot();
  semantic.connect("destination", owner);
  view.session = undefined;
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
          this.receive({
            id: request.id,
            result: { version: protocolVersion, serverTime: 1_800_000_000_000 },
          });
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

const historyRow = (seq, position) => ({ seq, position, revision: 1, text: `entry ${seq}` });

test("pages preserve loaded history and acknowledge stale attachment replies after discard", async () => {
  const source = new Stream();
  let publish;
  const replies = [];
  const owner = {
    subscribe(_method, _params, event) {
      publish = event;
      return () => {};
    },
    request(method) {
      assert.equal(method, "session/page");
      const reply = Promise.withResolvers();
      replies.push(reply);
      return reply.promise;
    },
  };
  source.connect("thread", owner);
  const cursor = { attachment: "first", kind: "main", revision: 1, before: 2 };
  publish({
    kind: "snapshot",
    data: {
      session: { thread: "thread" },
      main: {
        attachment: "first",
        revision: 1,
        order: [2],
        entries: [historyRow(2, 1)],
        earlier: cursor,
      },
    },
  });
  const page = source.earlier();
  await tick();
  publish({
    kind: "patch",
    data: {
      main: {
        attachment: "first",
        revision: 1,
        order: [2, 1],
        entries: [historyRow(1, 2)],
        earlier: cursor,
      },
    },
  });
  assert.deepEqual(source.main.entries[2], historyRow(2, 1), "omitted entries retain their values");
  let acknowledgements = 0;
  replies[0].resolve({
    entries: [historyRow(3, 0)],
    earlier: cursor,
    acknowledge() {
      acknowledgements++;
      assert.deepEqual(source.main.order, [3, 2, 1]);
    },
  });
  await page;
  publish({
    kind: "patch",
    data: {
      main: {
        attachment: "first",
        revision: 1,
        order: [1],
        entries: [],
        earlier: cursor,
      },
    },
  });
  assert.deepEqual(
    source.main.order,
    [3, 2, 1],
    "latest membership cannot drop explicitly acquired rows",
  );
  const jumps = source.jumps;
  publish({
    kind: "snapshot",
    data: {
      session: { thread: "thread" },
      main: {
        attachment: "first",
        revision: 1,
        order: [1],
        entries: [historyRow(1, 2)],
        earlier: cursor,
      },
    },
  });
  assert.deepEqual(source.main.order, [3, 2, 1], "catch-up snapshots retain acquired pages");
  assert.equal(source.jumps, jumps, "catch-up does not interrupt reading");
  const stale = source.earlier();
  await tick();
  publish({
    kind: "snapshot",
    data: {
      session: { thread: "thread" },
      main: {
        attachment: "replacement",
        revision: 1,
        order: [9],
        entries: [historyRow(9, 0)],
        earlier: null,
      },
    },
  });
  replies[1].resolve({
    entries: [historyRow(8, 0)],
    earlier: null,
    acknowledge() {
      acknowledgements++;
      assert.deepEqual(source.main.order, [9]);
    },
  });
  await stale;
  assert.equal(acknowledgements, 2);
  source.close();
});

test("roster retries retain the failed page or agent operation", async () => {
  const source = new Stream();
  const content = { attachment: "A", kind: "roster", revision: 1, seq: 1 };
  const agent = { name: "/root/worker", content };
  source.roster = {
    agents: [agent],
    next: { attachment: "A", kind: "roster", revision: 1, before: 40 },
  };
  let pages = 0,
    agents = 0;
  source.page = async () => {
    if (++pages === 1) throw new Error("page unavailable");
    return { roster: { agents: [agent], next: null } };
  };
  source.readContent = async () => {
    if (++agents === 1) throw new Error("agent unavailable");
    return { agent: { name: agent.name } };
  };
  const retry = () =>
    source.rosterRetryAgent ? source.completeAgent(source.rosterRetryAgent) : source.moreRoster();
  await source.moreRoster();
  assert.equal(source.rosterRetryAgent, undefined);
  await retry();
  assert.equal(pages, 2);
  assert.equal(agents, 0);
  assert.equal(source.roster.next, null);
  await source.completeAgent(agent);
  assert.equal(source.rosterRetryAgent, agent);
  await retry();
  assert.equal(agents, 2);
  assert.equal(source.rosterError, undefined);
  assert.equal(source.roster.agents[0].content, undefined);
});
