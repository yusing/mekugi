import { protocolVersion, subprotocol } from "../../../../sessionapi/protocol";
import type {
  RPCClosed,
  RPCError,
  RPCEvent,
  RPCInitialize,
  RPCMessage,
  RPCMethod,
  RPCParams,
  RPCSubscribed,
  RemoteMutation,
  InputRequest,
  Answer,
} from "../../../../sessionapi/protocol";
export type { RPCEvent, RPCMethod, RPCParams } from "../../../../sessionapi/protocol";

export type Connection = "connecting" | "open" | "lost" | "required" | "incompatible";
export interface RequestOptions {
  signal?: AbortSignal;
}
export type SubscriptionMethod = "session/subscribe" | "view/attach";
type StateListener = (state: Connection, error?: TransportError) => void;

export class TransportError extends Error {
  readonly kind: "network" | "unknown" | "required" | "incompatible" | "refused" | "canceled";
  readonly status?: number;
  constructor(message: string, kind: TransportError["kind"], status?: number) {
    super(message);
    this.kind = kind;
    this.status = status;
  }
}

export const unknownOutcome =
  "The request outcome is unknown. Check this machine's session before submitting again. Nothing was retried.";
export const unreachable =
  "Cannot reach this machine's connector. Check its address and allowed UI origin.";
const backoff = (attempt: number): number => Math.min(1000 * 2 ** attempt, 15_000);
const canceled = (): TransportError =>
  new TransportError("The browser request was canceled.", "canceled");
export function errorText(error: unknown, mutation = false): string {
  return error instanceof TransportError ? error.message : mutation ? unknownOutcome : unreachable;
}

// getRandomValues remains available when the UI is served over plain HTTP.
export function newIdentity(): string {
  return crypto.getRandomValues(new Uint32Array(4)).join("-");
}

export function endpointURL(endpoint: string): string {
  let url: URL;
  try {
    url = new URL(endpoint);
  } catch {
    throw new Error("Enter an absolute HTTP or HTTPS connector URL.");
  }
  if (
    !["http:", "https:"].includes(url.protocol) ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  )
    throw new Error(
      "Use an HTTP or HTTPS connector URL without credentials, query parameters or a fragment.",
    );
  return url.href.replace(/\/+$/, "");
}

function remoteError(error: RPCError, initialize = false): TransportError {
  const status = error.data?.status;
  if (status === 401)
    return new TransportError("This machine requires a valid bearer token.", "required", status);
  if (initialize && status === 409)
    return new TransportError(
      "This connector uses an incompatible Mekugi protocol.",
      "incompatible",
      status,
    );
  if (error.data?.outcome === "unknown")
    return new TransportError(`${error.message} ${unknownOutcome}`, "unknown", status);
  return new TransportError(error.message, "refused", status);
}

function readOnly(method: RPCMethod): boolean {
  return (
    method === "initialize" ||
    method === "session/options" ||
    method === "session/attachment-image" ||
    method === "session/list" ||
    method === "session/read" ||
    method === "session/subscribe" ||
    method === "view/attach" ||
    method === "subscription/cancel" ||
    method === "request/cancel"
  );
}

interface Pending {
  mutation: boolean;
  result: (value: unknown) => void;
  fail: (error: TransportError) => void;
}
interface Subscription {
  acknowledged: boolean;
  event: (event: RPCEvent) => void;
  fail: (error: TransportError) => void;
}

// One physical connection correlates replies and owns its subscription identities.
class Channel {
  readonly ready: Promise<void>;
  #socket: WebSocket;
  #closed = false;
  #next = 0;
  #pending = new Map<string, Pending>();
  #subscriptions = new Map<string, Subscription>();

  get closed(): boolean {
    return this.#closed;
  }

  constructor(endpoint: string, token: string) {
    const url = new URL(endpoint + "/api/control");
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    this.#socket = new WebSocket(url, subprotocol);
    this.#socket.addEventListener("message", (message) => this.#receive(message.data));
    this.ready = new Promise((resolve, reject) => {
      const timer = setTimeout(() => this.close(), 10_000);
      const fail = () => {
        clearTimeout(timer);
        reject(new TransportError(unreachable, "network"));
      };
      this.#socket.addEventListener("close", fail, { once: true });
      this.#socket.addEventListener(
        "open",
        () => {
          if (this.#socket.protocol !== subprotocol) {
            clearTimeout(timer);
            reject(
              new TransportError(
                "This connector uses an incompatible Mekugi protocol.",
                "incompatible",
              ),
            );
            this.close();
            return;
          }
          this.#dispatch<RPCInitialize>("initialize", { version: protocolVersion, token })
            .then((result) => {
              if (result?.version !== protocolVersion)
                throw new TransportError(
                  "This connector uses an incompatible Mekugi protocol.",
                  "incompatible",
                );
              clearTimeout(timer);
              this.#socket.removeEventListener("close", fail);
              resolve();
            })
            .catch((error) => {
              clearTimeout(timer);
              reject(error);
              this.close();
            });
        },
        { once: true },
      );
    });
    this.#socket.addEventListener("error", () => this.close());
    this.#socket.addEventListener("close", () => this.close());
    // A connection may be removed while its first consumer is waiting to start.
    void this.ready.catch(() => {});
  }

  close(): void {
    if (this.#closed) return;
    this.#closed = true;
    this.#socket.close();
    for (const pending of this.#pending.values())
      pending.fail(
        new TransportError(
          pending.mutation ? unknownOutcome : unreachable,
          pending.mutation ? "unknown" : "network",
        ),
      );
    for (const subscription of this.#subscriptions.values())
      subscription.fail(new TransportError(unreachable, "network"));
    this.#pending.clear();
    this.#subscriptions.clear();
  }

  async request<T>(
    method: RPCMethod,
    params: RPCParams,
    signal?: AbortSignal,
    admitted?: (result: unknown) => void,
  ): Promise<T> {
    await waitFor(this.ready, signal);
    if (signal?.aborted) throw canceled();
    return this.#dispatch<T>(method, params, signal, admitted);
  }

  #dispatch<T>(
    method: RPCMethod,
    params: RPCParams | RPCInitialize,
    signal?: AbortSignal,
    admitted?: (result: unknown) => void,
  ): Promise<T> {
    if (signal?.aborted) return Promise.reject(canceled());
    if (this.#closed || this.#socket.readyState !== WebSocket.OPEN)
      return Promise.reject(new TransportError(unreachable, "network"));
    if (this.#pending.size >= 32 || this.#socket.bufferedAmount > 48 * 1024 * 1024)
      return Promise.reject(
        new TransportError(
          "This connection is busy. Wait for its pending requests.",
          "refused",
          503,
        ),
      );
    const id = String(++this.#next);
    const encoded = JSON.stringify({ jsonrpc: "2.0", id, method, params });
    const mutation = !readOnly(method);
    return new Promise<T>((resolve, reject) => {
      const finish = () => {
        clearTimeout(timer);
        signal?.removeEventListener("abort", abort);
        this.#pending.delete(id);
      };
      const abort = () => {
        this.#cancel("request/cancel", { body: { id } });
        finish();
        reject(mutation ? new TransportError(unknownOutcome, "unknown") : canceled());
      };
      const timer = setTimeout(abort, mutation ? 120_000 : 15_000);
      this.#pending.set(id, {
        mutation,
        result: (value) => {
          finish();
          try {
            admitted?.(value);
            resolve(value as T);
          } catch {
            reject(
              new TransportError(
                "The connector returned an invalid subscription acknowledgement.",
                "network",
              ),
            );
            this.close();
          }
        },
        fail: (error) => {
          finish();
          reject(error);
        },
      });
      signal?.addEventListener("abort", abort, { once: true });
      try {
        this.#socket.send(encoded);
      } catch {
        this.close();
      }
    });
  }

  #cancel(method: "request/cancel" | "subscription/cancel", params: RPCParams): void {
    if (this.#closed || this.#socket.readyState !== WebSocket.OPEN) return;
    try {
      this.#socket.send(
        JSON.stringify({ jsonrpc: "2.0", id: String(++this.#next), method, params }),
      );
    } catch {
      this.close();
    }
  }

  async follow(
    method: SubscriptionMethod,
    params: RPCParams,
    event: (event: RPCEvent) => void,
    signal: AbortSignal,
  ): Promise<void> {
    await waitFor(this.ready, signal);
    if (signal.aborted) return;
    const id = newIdentity();
    return new Promise((resolve, reject) => {
      const finish = (error?: TransportError) => {
        signal.removeEventListener("abort", abort);
        this.#subscriptions.delete(id);
        if (error) reject(error);
        else resolve();
      };
      const abort = () => {
        this.#cancel("subscription/cancel", { subscription: id });
        finish();
      };
      if (signal.aborted) return finish();
      this.#subscriptions.set(id, { acknowledged: false, event, fail: finish });
      signal.addEventListener("abort", abort, { once: true });
      void this.request<RPCSubscribed>(method, { ...params, subscription: id }, signal, (value) => {
        const ack = value as RPCSubscribed;
        if (ack?.subscription !== id) throw new Error("Wrong subscription");
        const active = this.#subscriptions.get(id);
        if (active) active.acknowledged = true;
      }).catch((error) => {
        if (this.#subscriptions.has(id)) this.#cancel("subscription/cancel", { subscription: id });
        finish(
          error instanceof TransportError ? error : new TransportError(unreachable, "network"),
        );
      });
    });
  }

  #receive(data: unknown): void {
    let message: RPCMessage;
    try {
      // The server bounds each publication, envelope included, to 64 MiB.
      if (typeof data !== "string" || data.length > 64 * 1024 * 1024)
        throw new Error("Invalid frame");
      message = JSON.parse(data);
      if (!message || Array.isArray(message) || message.jsonrpc !== "2.0")
        throw new Error("Invalid RPC");
      if (typeof message.id === "string" || typeof message.id === "number") {
        const pending = this.#pending.get(String(message.id));
        if (!pending) return;
        if (message.error) pending.fail(remoteError(message.error, String(message.id) === "1"));
        else if ("result" in message) pending.result(message.result);
        else throw new Error("Missing result");
        return;
      }
      if (message.method === "subscription/event") {
        const event = message.params as RPCEvent;
        const subscription = this.#subscriptions.get(event?.subscription);
        if (!subscription) return;
        if (!subscription.acknowledged) throw new Error("Publication before acknowledgement");
        subscription.event(event);
        return;
      }
      if (message.method === "subscription/closed") {
        const closed = message.params as RPCClosed;
        this.#subscriptions
          .get(closed?.subscription)
          ?.fail(
            closed.error
              ? remoteError(closed.error)
              : new TransportError("The session subscription closed.", "network"),
          );
        return;
      }
      throw new Error("Unexpected notification");
    } catch {
      this.close();
    }
  }
}

function waitFor<T>(promise: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return promise;
  if (signal.aborted) return Promise.reject(canceled());
  return new Promise((resolve, reject) => {
    const abort = () => reject(canceled());
    signal.addEventListener("abort", abort, { once: true });
    promise.then(resolve, reject).finally(() => signal.removeEventListener("abort", abort));
  });
}

function attachmentBearing(method: RPCMethod, params: RPCParams): boolean {
  if (method === "session/image" || method === "session/attachment-image") return true;
  if (method === "session/start" || method === "session/input")
    return !!(params.body as InputRequest | undefined)?.attachments?.length;
  if (method === "session/answer")
    return !!(params.body as Answer | undefined)?.answers?.some(
      (answer) => answer.attachments?.length,
    );
  if (method === "view/intent") {
    const operation = (params.body as RemoteMutation | undefined)?.operation;
    return (
      !!operation?.composer?.input?.attachments?.length ||
      !!operation?.answers?.answers.some((answer) => answer.attachments?.length)
    );
  }
  return false;
}

export class Connector {
  readonly endpoint: string;
  #token: string;
  #lifetime = new AbortController();
  #disposed = false;
  #shared?: Channel;
  #uploads = new Set<Channel>();

  constructor(endpoint: string, token: string) {
    this.endpoint = endpointURL(endpoint);
    this.#token = this.#credential(token);
  }
  #credential(token: string): string {
    const value = token.trim();
    if (!value || /\s/.test(value)) throw new Error("Enter this machine's bearer token.");
    return value;
  }
  authorize(token: string): void {
    const value = this.#credential(token);
    if (this.#disposed) throw new Error("This machine connection was removed.");
    const previous = this.#lifetime;
    this.#token = value;
    this.#lifetime = new AbortController();
    previous.abort();
    this.#shared?.close();
    this.#shared = undefined;
    for (const channel of this.#uploads) channel.close();
  }
  close(): void {
    this.#disposed = true;
    this.#token = "";
    this.#lifetime.abort();
    this.#shared?.close();
    for (const channel of this.#uploads) channel.close();
  }
  #channel(): Channel {
    if (!this.#shared || this.#shared.closed)
      this.#shared = new Channel(this.endpoint, this.#token);
    return this.#shared;
  }
  async request<T>(
    method: RPCMethod,
    params: RPCParams = {},
    options: RequestOptions = {},
  ): Promise<T> {
    if (this.#disposed || options.signal?.aborted) throw canceled();
    const signal = AbortSignal.any([
      this.#lifetime.signal,
      ...(options.signal ? [options.signal] : []),
    ]);
    if (!attachmentBearing(method, params))
      return this.#channel().request<T>(method, params, signal);
    const channel = new Channel(this.endpoint, this.#token);
    this.#uploads.add(channel);
    try {
      return await channel.request<T>(method, params, signal);
    } finally {
      this.#uploads.delete(channel);
      channel.close();
    }
  }
  subscribe(
    method: SubscriptionMethod,
    params: RPCParams,
    event: (event: RPCEvent) => void,
    state: StateListener,
  ): () => void {
    const controller = new AbortController();
    const run = async () => {
      let attempt = 0;
      while (!controller.signal.aborted && !this.#disposed) {
        const lifetime = this.#lifetime.signal;
        const signal = AbortSignal.any([controller.signal, lifetime]);
        state("connecting");
        try {
          await this.#channel().follow(
            method,
            params,
            (next) => {
              event(next);
              attempt = 0;
              state("open");
            },
            signal,
          );
        } catch (error) {
          if (controller.signal.aborted || this.#disposed) return;
          if (lifetime !== this.#lifetime.signal) continue;
          const failure =
            error instanceof TransportError ? error : new TransportError(unreachable, "network");
          const blocked = failure.kind === "required" || failure.kind === "incompatible";
          state(blocked ? (failure.kind as "required" | "incompatible") : "lost", failure);
          if (failure.status === 404 || failure.status === 410 || failure.status === 405) return;
          await pause(blocked ? undefined : backoff(attempt++), signal);
        }
      }
    };
    void run();
    return () => controller.abort();
  }
}

function pause(milliseconds: number | undefined, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const finish = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", finish);
      resolve();
    };
    if (signal.aborted) return finish();
    signal.addEventListener("abort", finish, { once: true });
    if (milliseconds !== undefined) timer = setTimeout(finish, milliseconds);
  });
}

export interface Selection {
  machine?: string;
  session?: string;
  agent?: string;
}

export function selectionHash(selection: Selection): string {
  const params = new URLSearchParams();
  if (selection.machine) params.set("machine", selection.machine);
  if (selection.machine && selection.session) params.set("thread", selection.session);
  if (selection.machine && selection.session && selection.agent && selection.agent !== "/root")
    params.set("agent", selection.agent);
  return params.size ? `#${params}` : "";
}

export function parseSelection(hash: string): Selection {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const machine = params.get("machine") || undefined;
  const session = machine ? params.get("thread") || undefined : undefined;
  const agent = session ? params.get("agent") || undefined : undefined;
  return { machine, session, agent: agent === "/root" ? undefined : agent };
}
