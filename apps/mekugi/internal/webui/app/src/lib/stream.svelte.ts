import { agentDisplayName, roleColor } from "./agents";
import type { Machine } from "./hub.svelte";
import { errorText, TransportError, type Connection, type RPCMethod } from "./transport";
import type {
  Action,
  Decide,
  Prompts,
  Agent,
  Entry,
  FeedChange,
  Input,
  Listed,
  Message,
  Playback,
  Preview,
  Roster,
  SavedReview,
  Session,
  Status,
  PageCursor,
  PageResult,
  ContentResult,
  ContentReference,
  DeferredSections,
} from "./protocol";

export type { Connection } from "./transport";
export interface Feed {
  attachment?: string;
  order: number[];
  entries: Record<number, Entry>;
  revision?: number;
  earlier?: PageCursor | null;
  loaded?: number[];
}
export interface Gone {
  status: number;
  text: string;
}
const empty: Feed = { order: [], entries: {} };

export class Stream {
  connection = $state<Connection>("connecting");
  session = $state.raw<Session | undefined>();
  playback = $state.raw<Playback | undefined>();
  input = $state.raw<Input | undefined>();
  prompts = $state.raw<Prompts | undefined>();
  status = $state.raw<Status>({});
  main = $state.raw<Feed>(empty);
  agents = $state.raw<Feed>(empty);
  histories = $state.raw<Record<string, Feed>>({});
  historyLoading = $state.raw<Record<string, boolean>>({});
  historyErrors = $state.raw<Record<string, string | undefined>>({});
  roster = $state.raw<Roster>({ agents: [] });
  rosterLoading = $state(false);
  rosterRetryAgent = $state.raw<Agent>();
  rosterRetryCaller = $state<string>();
  rosterError = $state<string>();
  unavailableAgents = $state.raw<string[]>([]);
  #agentReads = new Set<string>();
  #rosterNames = new Set<string>();
  review = $state.raw<SavedReview | null>(null);
  live = $state.raw<Preview[]>([]);
  deferred = $state.raw<DeferredSections>({});
  sectionLoading = $state.raw<Partial<Record<keyof DeferredSections, boolean>>>({});
  sectionErrors = $state.raw<Partial<Record<keyof DeferredSections, string>>>({});
  #hydratedSections = new Map<string, ContentReference>();
  byName = $derived(new Map(this.roster.agents.map((agent) => [agent.name, agent])));
  // Roles in the order this viewer first saw them, which fixes their colours.
  // Like the terminal view's, the order outlives connections, so moving
  // between threads keeps each role's colour.
  roles = $state.raw<string[]>([]);
  controlError = $state<string | undefined>();
  error = $state<string | undefined>();
  notice = $state<string | undefined>();
  gone = $state.raw<Gone | undefined>();
  jumps = $state(0);

  #id?: string;
  #machine?: Machine;
  #source?: () => void;
  #discovery?: Listed[];
  #attachment?: string;
  #requests = new AbortController();
  #pages: Promise<unknown> = Promise.resolve();
  #historyRequests = new Map<string, AbortSignal>();

  connect(id: string, machine: Machine, preserveRoster = false): void {
    if (id === this.#id && machine === this.#machine && this.#source) return;
    preserveRoster &&= machine === this.#machine;
    this.close();
    this.#id = id;
    this.#machine = machine;
    this.#attachment = undefined;
    this.#requests = new AbortController();
    this.connection = "connecting";
    this.session = this.playback = this.input = this.gone = undefined;
    this.prompts = undefined;
    this.status = {};
    this.main = this.agents = empty;
    this.histories = {};
    this.historyErrors = {};
    this.roster = preserveRoster
      ? {
          ...this.roster,
          next: undefined,
          agents: this.roster.agents.map((agent) => ({ ...agent, content: undefined })),
        }
      : { agents: [] };
    this.rosterRetryAgent = undefined;
    this.rosterRetryCaller = undefined;
    this.rosterError = undefined;
    this.unavailableAgents = [];
    this.review = null;
    this.live = [];
    this.deferred = {};
    this.sectionErrors = {};
    this.#hydratedSections.clear();
    this.controlError = this.error = this.notice = undefined;
    this.#follow(id, machine);
  }

  // Fresh discovery or an acquired view permits one read retry. Prepared
  // children are addressable through their view but absent from discovery.
  recover(attachment?: string): void {
    const machine = this.#machine;
    const id = this.#id;
    if (
      !this.gone ||
      !machine ||
      !id ||
      !this.#source ||
      !(
        (attachment && attachment !== this.#attachment) ||
        (machine.sessions !== this.#discovery &&
          machine.sessions?.some((session) => session.id === id))
      )
    )
      return;
    this.#attachment = attachment;
    this.close();
    if (!this.session) {
      this.gone = undefined;
      this.error = undefined;
      this.connection = "connecting";
    }
    this.#requests = new AbortController();
    this.#follow(id, machine);
  }

  #follow(id: string, machine: Machine): void {
    this.#discovery = machine.sessions;
    const lifetime = this.#requests.signal;
    let snapshot = false;
    this.#source = machine.subscribe(
      "session/subscribe",
      { thread: id },
      (event) => {
        if (lifetime.aborted) return;
        if (event.kind !== "snapshot" && event.kind !== "patch") return;
        if (!snapshot && event.kind !== "snapshot")
          throw new Error("A fresh snapshot is required.");
        this.#apply(event.data as Message, event.kind === "snapshot");
        snapshot = true;
      },
      (connection, error) => {
        if (lifetime.aborted) return;
        this.connection = connection;
        this.error = error?.message;
        if (connection === "connecting") snapshot = false;
        if (error?.status === 404 || error?.status === 410)
          this.gone = { status: error.status, text: error.message };
      },
    );
  }

  close(): void {
    this.#source?.();
    this.#source = undefined;
    this.#requests.abort();
    this.#pages = Promise.resolve();
    this.#historyRequests.clear();
    this.historyLoading = {};
    this.rosterLoading = false;
    this.#agentReads.clear();
    this.sectionLoading = {};
  }

  #apply(message: Message, snapshot: boolean): void {
    if (snapshot) this.gone = undefined;
    if (
      snapshot &&
      this.session &&
      (message.session?.thread !== this.session.thread ||
        message.main?.attachment !== this.main.attachment)
    )
      this.roster = { agents: [] };
    snapshot &&=
      message.session?.thread !== this.session?.thread ||
      message.main?.attachment !== this.main.attachment ||
      message.main?.revision !== this.main.revision ||
      message.agents?.attachment !== this.agents.attachment ||
      message.agents?.revision !== this.agents.revision;
    if (snapshot) this.#hydratedSections.clear();
    message = { ...message };
    if (message.deferred !== undefined) {
      this.deferred = Object.fromEntries(
        Object.entries(message.deferred ?? {}).filter(([kind, ref]) => {
          const loaded = this.#hydratedSections.get(kind);
          if (ref && loaded?.attachment === ref.attachment && loaded?.revision === ref.revision)
            delete message[kind as keyof DeferredSections];
          return (
            ref && (loaded?.attachment !== ref.attachment || loaded?.revision !== ref.revision)
          );
        }),
      );
    }
    if (snapshot) {
      this.gone = undefined;
      this.session = message.session;
      this.playback = message.playback;
      this.input = message.input;
      this.prompts = message.prompts;
      this.status = message.status ?? {};
      this.#updateRoster(message.roster ?? { agents: [] });
      this.unavailableAgents = [];
      this.#observeRoles();
      this.review = message.review ?? null;
      this.live = message.live ?? [];
      this.main = merge(empty, message.main);
      this.agents = merge(empty, message.agents);
      this.histories = {};
      this.jumps++;
      return;
    }
    if (message.session) this.session = message.session;
    if (message.playback) this.playback = message.playback;
    if (message.input) this.input = message.input;
    if (message.prompts) this.prompts = message.prompts;
    if (message.status) this.status = message.status;
    if (message.roster) {
      this.#updateRoster(message.roster);
      this.unavailableAgents = [];
      this.#observeRoles();
    }
    if (message.review !== undefined) this.review = message.review;
    if (message.live !== undefined) this.live = message.live ?? [];
    if (message.main) this.main = merge(this.main, message.main);
    if (message.agents) {
      this.agents = merge(this.agents, message.agents);
      const histories: Record<string, Feed> = {};
      for (const [caller, feed] of Object.entries(this.histories)) {
        if (feed.revision !== this.agents.revision) continue;
        const added = (message.agents.entries ?? []).filter((entry) => entry.agent === caller);
        const order = ordered(
          { ...feed.entries, ...Object.fromEntries(added.map((entry) => [entry.seq, entry])) },
          feed.order,
          added.map((entry) => entry.seq),
        );
        histories[caller] = merge(feed, {
          entries: added,
          order: feed.loaded?.length ? order : order.slice(-50),
          revision: feed.revision,
          attachment: feed.attachment,
          earlier:
            feed.earlier && !feed.loaded?.length
              ? { ...feed.earlier, before: order.slice(-50)[0] }
              : feed.earlier,
        });
      }
      this.histories = histories;
    }
  }

  history(caller?: string): Feed {
    return caller ? (this.histories[caller] ?? this.agents) : this.main;
  }

  async prepareHistory(caller: string, signal?: AbortSignal): Promise<void> {
    const pending = this.#historyRequests.get(caller);
    if (this.histories[caller] || (pending && !pending.aborted)) return;
    const cursor = this.agents.earlier;
    if (cursor) await this.#page({ ...cursor, caller, before: undefined }, caller, true, signal);
  }

  async earlier(caller?: string, signal?: AbortSignal): Promise<void> {
    if (caller && !this.histories[caller]) return this.prepareHistory(caller, signal);
    const cursor = this.history(caller).earlier;
    if (cursor) await this.#page({ ...cursor, caller }, caller, false, signal);
  }

  async #page(
    cursor: PageCursor,
    caller?: string,
    initial = false,
    signal?: AbortSignal,
  ): Promise<void> {
    const key = caller ?? "";
    const pending = this.#historyRequests.get(key);
    if ((pending && !pending.aborted) || !this.#machine || !this.#id) return;
    const session = this.#requests.signal;
    const lifetime = AbortSignal.any([session, ...(signal ? [signal] : [])]);
    this.#historyRequests.set(key, lifetime);
    this.historyLoading = { ...this.historyLoading, [key]: true };
    this.historyErrors = { ...this.historyErrors, [key]: undefined };
    let result: PageResult | undefined;
    try {
      result = await this.page(cursor, lifetime);
      const current = caller ? this.agents : this.main;
      if (
        lifetime.aborted ||
        cursor.attachment !== current.attachment ||
        cursor.revision !== current.revision
      )
        return;
      const entries = result.entries as Entry[];
      if (initial) {
        const last = entries.at(-1)?.position ?? -1;
        for (const value of Object.values(current.entries)) {
          if (value.agent !== caller) continue;
          const index = entries.findIndex((entry) => entry.seq === value.seq);
          if (index < 0 && (value.position ?? -1) > last) entries.push(value);
          else if (index >= 0 && (value.revision ?? 0) > (entries[index].revision ?? 0))
            entries[index] = value;
        }
      }
      const previous = initial ? empty : this.history(caller);
      const loaded = initial
        ? []
        : [...new Set([...previous.order, ...entries.map((entry) => entry.seq)])];
      const combined = {
        ...previous.entries,
        ...Object.fromEntries(entries.map((entry) => [entry.seq, entry])),
      };
      const next: Feed = {
        order: ordered(
          combined,
          previous.order,
          entries.map((entry) => entry.seq),
        ),
        entries: combined,
        attachment: cursor.attachment,
        revision: cursor.revision,
        earlier: result.earlier,
        loaded,
      };
      if (caller) this.histories = { ...this.histories, [caller]: next };
      else this.main = next;
    } catch (error) {
      if (!lifetime.aborted)
        this.historyErrors = { ...this.historyErrors, [key]: errorText(error) };
    } finally {
      result?.acknowledge?.();
      if (this.#historyRequests.get(key) === lifetime) {
        this.#historyRequests.delete(key);
        if (!session.aborted) this.historyLoading = { ...this.historyLoading, [key]: false };
      }
    }
  }

  async content(entry: Entry, signal: AbortSignal): Promise<Entry> {
    if (!entry.content || !this.#machine || !this.#id) return entry;
    const result = await this.readContent(entry.content, signal);
    if (!result.entry) throw new Error("The complete entry is unavailable.");
    return result.entry as Entry;
  }

  async readContent(reference: ContentReference, signal: AbortSignal): Promise<ContentResult> {
    if (!this.#machine || !this.#id) throw new Error("The content's machine is disconnected.");
    const result = await this.#machine.request<ContentResult>(
      "session/content",
      { thread: this.#id, body: reference },
      { signal: AbortSignal.any([signal, this.#requests.signal]) },
    );
    return result;
  }

  async page(cursor: PageCursor, signal: AbortSignal): Promise<PageResult> {
    if (!this.#machine || !this.#id) throw new Error("The history's machine is disconnected.");
    const machine = this.#machine,
      thread = this.#id;
    const lifetime = AbortSignal.any([signal, this.#requests.signal]);
    const read = this.#pages.then(() =>
      machine.request<PageResult>("session/page", { thread, body: cursor }, { signal: lifetime }),
    );
    this.#pages = read.then(
      () => undefined,
      () => undefined,
    );
    return read;
  }

  async moreRoster(caller?: string, signal?: AbortSignal): Promise<void> {
    const cursor = this.roster.next;
    if (!cursor || (caller ? this.#agentReads.has(caller) : this.rosterLoading)) return;
    const session = this.#requests.signal;
    const lifetime = signal ? AbortSignal.any([session, signal]) : session;
    if (caller) this.#agentReads.add(caller);
    else this.rosterLoading = true;
    this.rosterError = undefined;
    let result: PageResult | undefined;
    try {
      result = await this.page(
        { ...cursor, caller, before: caller ? undefined : cursor.before },
        lifetime,
      );
      if (
        lifetime.aborted ||
        this.roster.next?.attachment !== cursor.attachment ||
        this.roster.next?.revision !== cursor.revision ||
        !result.roster
      )
        return;
      const page = result.roster as Roster;
      this.#updateRoster(
        {
          ...this.roster,
          agents: page.agents,
          next: caller ? this.roster.next : page.next,
        },
        true,
      );
      if (caller && !page.agents.length)
        this.unavailableAgents = [...this.unavailableAgents, caller];
      this.#observeRoles();
    } catch (error) {
      if (
        !lifetime.aborted &&
        this.roster.next?.revision === cursor.revision &&
        !(error instanceof TransportError && error.status === 409)
      ) {
        this.rosterRetryAgent = undefined;
        this.rosterRetryCaller = caller;
        this.rosterError = errorText(error);
      }
    } finally {
      result?.acknowledge?.();
      if (!session.aborted) {
        if (caller) this.#agentReads.delete(caller);
        else this.rosterLoading = false;
      }
    }
  }

  async completeAgent(agent: Agent, signal?: AbortSignal): Promise<void> {
    if (!agent.content) return;
    this.rosterError = undefined;
    const reference = agent.content,
      lifetime = signal ? AbortSignal.any([this.#requests.signal, signal]) : this.#requests.signal;
    try {
      const result = await this.readContent(reference, lifetime);
      if (lifetime.aborted || !result.agent) return;
      const current = this.agent(agent.name)?.content;
      if (current?.attachment !== reference.attachment || current.revision !== reference.revision)
        return;
      this.roster = {
        ...this.roster,
        agents: this.roster.agents.map((value) =>
          value.name === agent.name ? (result.agent as Agent) : value,
        ),
      };
    } catch (error) {
      if (
        !lifetime.aborted &&
        this.agent(agent.name)?.content === reference &&
        !(error instanceof TransportError && error.status === 409)
      ) {
        this.rosterRetryAgent = agent;
        this.rosterError = errorText(error);
      }
    }
  }

  // Keep acquired rows visible while the next revision's bounded pages arrive.
  // Only a complete publication can prove that an older member disappeared.
  #updateRoster(page: Roster, append = false): void {
    if (!append) this.#rosterNames.clear();
    const agents = new Map(
      this.roster.agents.map((agent) => [
        agent.name,
        append ? agent : { ...agent, content: undefined },
      ]),
    );
    for (const agent of page.agents) {
      this.#rosterNames.add(agent.name);
      agents.set(agent.name, agent);
    }
    this.roster = {
      ...page,
      agents: page.next
        ? [...agents.values()]
        : [...this.#rosterNames].map((name) => agents.get(name)!),
    };
    this.rosterError = undefined;
  }

  async loadSection(kind: keyof DeferredSections, signal?: AbortSignal): Promise<void> {
    const ref = this.deferred[kind],
      session = this.#requests.signal,
      lifetime = signal ? AbortSignal.any([session, signal]) : session;
    if (!ref || this.sectionLoading[kind]) return;
    this.sectionLoading = { ...this.sectionLoading, [kind]: true };
    this.sectionErrors = { ...this.sectionErrors, [kind]: "" };
    try {
      const result = await this.readContent(ref, lifetime);
      const current = this.deferred[kind];
      if (
        lifetime.aborted ||
        current?.attachment !== ref.attachment ||
        current.revision !== ref.revision
      )
        return;
      switch (kind) {
        case "session":
          if (result.session) this.session = result.session;
          break;
        case "input":
          if (result.input) this.input = result.input;
          break;
        case "prompts":
          if (result.prompts) this.prompts = result.prompts as Prompts;
          break;
        case "status":
          if (result.status) this.status = result.status as Status;
          break;
        case "review":
          if (result.reviewInfo)
            this.review = { ...(result.reviewInfo as SavedReview), page: this.review?.page };
          break;
        case "live":
          this.live = result.live ?? [];
          break;
      }
      this.#hydratedSections.set(kind, ref);
      const next = { ...this.deferred };
      delete next[kind];
      this.deferred = next;
    } catch (error) {
      if (!lifetime.aborted)
        this.sectionErrors = { ...this.sectionErrors, [kind]: errorText(error) };
    } finally {
      if (!session.aborted) this.sectionLoading = { ...this.sectionLoading, [kind]: false };
    }
  }

  #observeRoles(): void {
    const seen = this.roles;
    const added = this.roster.agents
      .map((agent) => agent.role)
      .filter((role, i, all): role is string => !!role && all.indexOf(role) === i)
      .filter((role) => !seen.includes(role));
    if (added.length) this.roles = [...seen, ...added];
  }

  // A role's colour, once the roster has shown it.
  roleColor(role: string | undefined): string | undefined {
    const index = role ? this.roles.indexOf(role) : -1;
    return index < 0 ? undefined : roleColor(index);
  }

  label(name: string | undefined): string {
    if (!name) return "main";
    if (name === "Main") name = this.session?.caller || "/root";
    return this.byName.get(name)?.label ?? agentDisplayName(name);
  }

  agent(name: string | undefined): Agent | undefined {
    return name
      ? this.byName.get(name === "Main" ? this.session?.caller || "/root" : name)
      : undefined;
  }

  async control(action: Action, position?: number): Promise<void> {
    const lifetime = this.#requests.signal;
    if (action !== "toggle-pause" && action !== "faster" && action !== "slower") this.jumps++;
    const error = await this.#request("session/control", { action, position });
    if (!lifetime.aborted) this.controlError = error;
  }

  stop(): Promise<string | undefined> {
    return this.#request("session/interrupt", {});
  }
  decide(body: Decide): Promise<string | undefined> {
    return this.#request("session/decide", body);
  }

  async image(entry: Entry, index: number, image: string, signal: AbortSignal): Promise<Blob> {
    const machine = this.#machine,
      id = this.#id;
    if (!machine || !id) throw new Error("The image's machine is disconnected.");
    const result = await machine.request<{ mime: string; data: string }>(
      "session/image",
      { thread: id, body: { seq: entry.seq, agent: entry.agent ?? "", index, image } },
      { signal: AbortSignal.any([signal, this.#requests.signal]) },
    );
    const bytes = Uint8Array.from(atob(result.data), (c) => c.charCodeAt(0));
    return new Blob([bytes], { type: result.mime });
  }

  async #request(method: RPCMethod, body: unknown): Promise<string | undefined> {
    const machine = this.#machine;
    const id = this.#id;
    const lifetime = this.#requests.signal;
    if (!machine || !id || lifetime.aborted) return "Open a session on an execution machine first.";
    try {
      const result = await machine.request<{ notice?: string } | null>(
        method,
        { thread: id, body },
        {
          signal: lifetime,
        },
      );
      if (!lifetime.aborted) this.notice = result?.notice;
      return undefined;
    } catch (error) {
      return errorText(error, true);
    }
  }
}

function ordered(entries: Record<number, Entry>, ...groups: number[][]): number[] {
  const values = [...new Set(groups.flat())];
  // ES2022 has no toSorted; this array is newly owned by the merge.
  // oxlint-disable-next-line unicorn/no-array-sort
  return values.sort((a, b) => (entries[a]?.position ?? a) - (entries[b]?.position ?? b));
}

function merge(feed: Feed, change: FeedChange | undefined): Feed {
  if (!change) return feed;
  if (
    change.revision !== undefined &&
    (change.attachment !== feed.attachment || change.revision !== feed.revision)
  )
    feed = empty;
  const entries = { ...feed.entries };
  for (const entry of change.entries ?? []) {
    if ((entry.revision ?? 0) >= (entries[entry.seq]?.revision ?? 0)) entries[entry.seq] = entry;
  }
  const order = change.order ? ordered(entries, feed.loaded ?? [], change.order) : feed.order;
  if (change.order) {
    const kept = new Set(order);
    for (const key of Object.keys(entries)) if (!kept.has(Number(key))) delete entries[Number(key)];
  }
  return {
    order,
    entries,
    attachment: change.attachment ?? feed.attachment,
    revision: change.revision ?? feed.revision,
    earlier: feed.loaded?.length
      ? feed.earlier
      : change.earlier !== undefined
        ? change.earlier
        : feed.earlier,
    loaded: feed.loaded,
  };
}

export const stream = new Stream();
