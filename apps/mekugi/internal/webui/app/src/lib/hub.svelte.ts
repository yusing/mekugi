import type { Listed, Start, StartOptions } from "./protocol";
import {
  Connector,
  errorText,
  newIdentity,
  type Connection,
  type RequestOptions,
  type RPCEvent,
  type RPCMethod,
  type RPCParams,
  type SubscriptionMethod,
  type TransportError,
} from "./transport";
import { select, view } from "./view.svelte";

export class Machine {
  readonly id = newIdentity();
  readonly label: string;
  readonly endpoint: string;
  sessions = $state.raw<Listed[] | undefined>();
  connection = $state<Connection>("connecting");
  error = $state<string | undefined>();
  #connector: Connector;
  #stop?: () => void;

  constructor(label: string, endpoint: string, token: string) {
    this.label = label.trim();
    if (!this.label) throw new Error("Enter a machine label.");
    this.#connector = new Connector(endpoint, token);
    this.endpoint = this.#connector.endpoint;
  }

  request<T>(method: RPCMethod, params?: RPCParams, options?: RequestOptions): Promise<T> {
    return this.#connector.request<T>(method, params, options);
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
    this.connection = "connecting";
    this.error = undefined;
  }

  close(): void {
    this.#stop?.();
    this.#connector.close();
  }
}

class Hub {
  machines = $state.raw<Machine[]>([]);

  get selected(): Machine | undefined {
    return this.machines.find((machine) => machine.id === view.machine);
  }

  async add(label: string, endpoint: string, token: string): Promise<string | undefined> {
    try {
      const machine = new Machine(label, endpoint, token);
      this.machines = [...this.machines, machine];
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
    if (view.machine === id) select(undefined);
  }

  authorize(id: string, token: string): string | undefined {
    const machine = this.machines.find((candidate) => candidate.id === id);
    if (!machine) return "This machine connection was removed.";
    try {
      machine.authorize(token);
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
