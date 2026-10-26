import type { Machine } from "./hub.svelte";
import { errorText, type Connection, type RPCMethod } from "./transport";
import type {
  Action,
  Answer,
  Decide,
  Prompts,
  Agent,
  Entry,
  FeedChange,
  Input,
  Message,
  Playback,
  Preview,
  Roster,
  SavedReview,
  Session,
  Status,
} from "./protocol";

export type { Connection } from "./transport";
export interface Feed {
  order: number[];
  entries: Record<number, Entry>;
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
  roster = $state.raw<Roster>({ agents: [] });
  review = $state.raw<SavedReview | null>(null);
  live = $state.raw<Preview[]>([]);
  byName = $derived(new Map(this.roster.agents.map((agent) => [agent.name, agent])));
  controlError = $state<string | undefined>();
  error = $state<string | undefined>();
  notice = $state<string | undefined>();
  gone = $state.raw<Gone | undefined>();
  jumps = $state(0);

  #id?: string;
  #machine?: Machine;
  #source?: () => void;
  #requests = new AbortController();

  connect(id: string, machine: Machine): void {
    if (id === this.#id && machine === this.#machine && this.#source) return;
    this.close();
    this.#id = id;
    this.#machine = machine;
    this.#requests = new AbortController();
    this.connection = "connecting";
    this.session = this.playback = this.input = this.gone = undefined;
    this.prompts = undefined;
    this.status = {};
    this.main = this.agents = empty;
    this.roster = { agents: [] };
    this.review = null;
    this.live = [];
    this.controlError = this.error = this.notice = undefined;
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
  }

  #apply(message: Message, snapshot: boolean): void {
    if (snapshot) {
      this.session = message.session;
      this.playback = message.playback;
      this.input = message.input;
      this.prompts = message.prompts;
      this.status = message.status ?? {};
      this.roster = message.roster ?? { agents: [] };
      this.review = message.review ?? null;
      this.live = message.live ?? [];
      this.main = merge(empty, message.main);
      this.agents = merge(empty, message.agents);
      this.jumps++;
      return;
    }
    if (message.session) this.session = message.session;
    if (message.playback) this.playback = message.playback;
    if (message.input) this.input = message.input;
    if (message.prompts) this.prompts = message.prompts;
    if (message.status) this.status = message.status;
    if (message.roster) this.roster = message.roster;
    if (message.review !== undefined) this.review = message.review;
    if (message.live !== undefined) this.live = message.live ?? [];
    if (message.main) this.main = merge(this.main, message.main);
    if (message.agents) this.agents = merge(this.agents, message.agents);
  }

  label(name: string | undefined): string {
    if (!name) return "main";
    if (name === "Main") name = this.session?.caller || "/root";
    return (
      this.byName.get(name)?.label ?? (name === "/root" ? "main" : name.replace(/^\/root\//, ""))
    );
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
  answer(body: Answer): Promise<string | undefined> {
    return this.#request("session/answer", body);
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

function merge(feed: Feed, change: FeedChange | undefined): Feed {
  if (!change) return feed;
  const entries = { ...feed.entries };
  for (const entry of change.entries ?? []) entries[entry.seq] = entry;
  const order = change.order ?? feed.order;
  if (change.order) {
    const kept = new Set(order);
    for (const key of Object.keys(entries)) if (!kept.has(Number(key))) delete entries[Number(key)];
  }
  return { order, entries };
}

export const stream = new Stream();
