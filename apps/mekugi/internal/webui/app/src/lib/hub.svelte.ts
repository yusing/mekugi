import type { Listed, Start, StartOptions } from "./protocol";
import {
  Connector,
  errorText,
  newIdentity,
  type Connection,
  type RequestOptions,
  type ResourceKind,
  type RPCEvent,
  type RPCMethod,
  type RPCParams,
  type SubscriptionMethod,
  type TransportError,
} from "./transport";
import { select, view } from "./view.svelte";

type SavedMachine = { id: string; label: string; endpoint: string; token: string };
const storagePrefix = "mekugi.machine.v1:";

export class Machine {
  readonly id: string;
  readonly label: string;
  readonly endpoint: string;
  sessions = $state.raw<Listed[] | undefined>();
  connection = $state<Connection>("connecting");
  error = $state<string | undefined>();
  #connector: Connector;
  #token: string;
  #stop?: () => void;

  constructor(label: string, endpoint: string, token: string, id = newIdentity()) {
    this.id = id;
    this.label = label.trim();
    if (!this.label) throw new Error("Enter a machine label.");
    this.#connector = new Connector(endpoint, token);
    this.endpoint = this.#connector.endpoint;
    this.#token = token.trim();
  }

  saved(): SavedMachine {
    return { id: this.id, label: this.label, endpoint: this.endpoint, token: this.#token };
  }

  get now(): number | undefined {
    return this.#connector.now;
  }

  request<T>(method: RPCMethod, params?: RPCParams, options?: RequestOptions): Promise<T> {
    return this.#connector.request<T>(method, params, options);
  }

  resource<T>(kind: ResourceKind, params: RPCParams, options?: RequestOptions): Promise<T> {
    return this.#connector.resource<T>(kind, params, options);
  }

  subscribe(
    method: SubscriptionMethod,
    params: RPCParams,
    event: (event: RPCEvent) => void,
    state: (connection: Connection, error?: TransportError) => void,
  ): () => void {
    return this.#connector.subscribe(method, params, event, state);
  }

  start(): void {
    this.#stop?.();
    this.#stop = this.subscribe(
      "session/subscribe",
      {},
      (event) => {
        if (event.kind === "sessions")
          this.sessions = (event.data as { sessions: Listed[] }).sessions;
      },
      (connection, error) => {
        // Retries keep reporting the failure until the machine answers again.
        if (connection === "connecting" && this.connection === "lost") return;
        this.connection = connection;
        this.error = error?.message;
      },
    );
  }

  // What the machine's connection means to someone choosing where to work.
  get state(): { text: string; tone: "open" | "warn" | "alert" | "" } {
    switch (this.connection) {
      case "open":
        return { text: "Connected", tone: "open" };
      case "lost":
        return { text: "Unreachable, retrying", tone: "warn" };
      case "required":
        return { text: "Token rejected", tone: "alert" };
      case "incompatible":
        return { text: "Incompatible version", tone: "alert" };
      default:
        return { text: "Connecting…", tone: "" };
    }
  }

  authorize(token: string): void {
    this.#connector.authorize(token);
    this.#token = token.trim();
    this.connection = "connecting";
    this.error = undefined;
  }

  close(): void {
    this.#stop?.();
    this.#connector.close();
    this.#token = "";
  }
}

class Hub {
  machines = $state.raw<Machine[]>([]);
  storageError = $state<string>();
  #restoreError?: string;
  #failedWrites = new Set<string>();

  constructor() {
    try {
      for (let index = 0; index < localStorage.length; index++) {
        const key = localStorage.key(index);
        if (!key?.startsWith(storagePrefix)) continue;
        try {
          const saved = localStorage.getItem(key);
          if (saved === null) continue;
          const record = JSON.parse(saved);
          if (
            !record ||
            typeof record.id !== "string" ||
            !record.id.trim() ||
            key !== storagePrefix + record.id ||
            typeof record.label !== "string" ||
            typeof record.endpoint !== "string" ||
            typeof record.token !== "string"
          )
            throw new Error("Invalid saved machine");
          this.machines.push(new Machine(record.label, record.endpoint, record.token, record.id));
        } catch {
          this.storageError =
            "Some saved machines could not be restored. Add them again under Machines.";
        }
      }
    } catch {
      this.storageError =
        "Saved machines could not be read. Check this site's browser storage settings.";
    }
    this.#restoreError = this.storageError;
    for (const machine of this.machines) machine.start();
  }

  #save(id: string, machine?: Machine): void {
    try {
      if (machine) localStorage.setItem(storagePrefix + id, JSON.stringify(machine.saved()));
      else localStorage.removeItem(storagePrefix + id);
      this.#failedWrites.delete(id);
    } catch {
      this.#failedWrites.add(id);
    }
    this.storageError = this.#failedWrites.size
      ? "Machine changes could not be saved. Reloading may restore older machines or tokens. Check this site's browser storage settings."
      : this.#restoreError;
  }

  get selected(): Machine | undefined {
    return this.machines.find((machine) => machine.id === view.machine);
  }

  async add(label: string, endpoint: string, token: string): Promise<string | undefined> {
    try {
      const machine = new Machine(label, endpoint, token);
      this.machines = [...this.machines, machine];
      this.#save(machine.id, machine);
      machine.start();
      // Adding a machine from a thread leaves that thread on screen.
      if (!view.session) select(machine.id);
      return undefined;
    } catch (error) {
      return error instanceof Error ? error.message : "Could not add this machine.";
    }
  }

  remove(id: string): void {
    this.machines.find((machine) => machine.id === id)?.close();
    this.machines = this.machines.filter((machine) => machine.id !== id);
    this.#save(id);
    if (view.machine === id) select(undefined);
  }

  authorize(id: string, token: string): string | undefined {
    const machine = this.machines.find((candidate) => candidate.id === id);
    if (!machine) return "This machine connection was removed.";
    try {
      machine.authorize(token);
      this.#save(id, machine);
      return undefined;
    } catch (error) {
      return error instanceof Error
        ? error.message
        : "Could not replace this machine's credential.";
    }
  }

  async startOptions(machine: Machine): Promise<{ options: StartOptions } | { error: string }> {
    try {
      return { options: await machine.request<StartOptions>("session/options") };
    } catch (error) {
      return { error: errorText(error) };
    }
  }

  async createSession(
    start: Start,
    machine: Machine,
    signal?: AbortSignal,
  ): Promise<{ id: string; notice?: string } | { error: string }> {
    try {
      return await machine.request<{ id: string; notice?: string }>(
        "session/start",
        { body: start },
        { signal },
      );
    } catch (error) {
      return { error: errorText(error, true) };
    }
  }

  async endSession(id: string, machine: Machine): Promise<string | undefined> {
    try {
      await machine.request("session/end", { thread: id });
      return undefined;
    } catch (error) {
      return errorText(error, true);
    }
  }
}

export const hub = new Hub();
