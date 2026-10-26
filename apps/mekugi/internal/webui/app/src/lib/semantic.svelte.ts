import type {
  Draft,
  EditorTarget,
  NativeEvent,
  NativeResult,
  RemoteMutation,
  RemoteOperation,
  Version,
} from "../../../../sessionapi/protocol";
import type { Machine } from "./hub.svelte";
import { errorText, newIdentity, TransportError, type Connection } from "./transport";
import { open, view } from "./view.svelte";
import type { PendingAttachment } from "./attachments";

export function textDraft(text = "", cursorBack = 0): Draft {
  return {
    Text: text,
    CursorBack: cursorBack,
    Images: [],
    Files: [],
    Skills: [],
    Selections: [],
    AnswerImages: [],
    Attachments: [],
    AttachmentNotice: "",
    InHistory: false,
  };
}
const stale = "The thread changed before this arrived, so nothing was applied. Try again.";
export type Editor = {
  text: string;
  cursorBack: number;
  revision: number;
  items: PendingAttachment[];
  submission?: AbortController;
};
export type PromptState = {
  drafts: Record<string, { choice: number | "skip" | undefined; text: string }>;
  deny?: number;
  reason: string;
  sending: boolean;
  accepted: boolean;
  error?: string;
  resolved?: boolean;
};

type ControlView = {
  machine: Machine;
  thread: string;
  lifetime: AbortController;
  stop?: () => void;
  event?: NativeEvent;
  received: boolean;
  connection: Connection;
  error?: string;
  operationError?: string;
  outcome?: TransportError["kind"];
  work: number;
  inputs: number;
  pending: number;
  queue: Promise<unknown>;
  effect: number;
  acknowledged: number;
  receipt: number;
  applied?: string;
  prompts: Record<string, PromptState>;
};

function controlView(thread: string, machine: Machine): ControlView {
  const prompts = $state<Record<string, PromptState>>({});
  return {
    thread,
    machine,
    lifetime: new AbortController(),
    connection: "connecting",
    received: false,
    work: 0,
    inputs: 0,
    pending: 0,
    queue: Promise.resolve(),
    effect: 0,
    acknowledged: 0,
    receipt: 0,
    prompts,
  };
}

class Semantic {
  event = $state.raw<NativeEvent>();
  connection = $state<Connection>("connecting");
  error = $state<string>();
  connectionError = $state<string>();
  pending = $state(0);
  resuming = $state<string>();
  copied = $state<string>();
  editors = $state<Record<string, Editor>>({});
  emptyEditor = $state<Editor>({ text: "", cursorBack: 0, revision: 0, items: [] });
  #settings = new Map<string, NonNullable<NativeEvent["frame"]["view"]["Menu"]>>();
  #completions = new Map<string, NonNullable<NativeEvent["frame"]["view"]["Menu"]>>();
  openingSetting = $state<string>();
  #source = $state.raw<ControlView>();
  #views = new Map<string, ControlView>();
  #destination?: ControlView;
  #settle = new Set<() => void>();

  get frame() {
    return this.event?.frame;
  }
  // Cached menus are presentation only. Actions always use the live frame.
  get settingsMenu() {
    const shown = this.frame?.view;
    if (!shown || shown.Displayed?.ID !== shown.Main.ID) return undefined;
    const menu = shown.Menu;
    if (menu?.Kind === "settings" && ["/model", "/effort", "/reasoning"].includes(menu.Setting))
      return menu.Loading && !menu.Choices.length
        ? { ...menu, Choices: this.#settings.get(this.settingKey(menu.Setting))?.Choices ?? [] }
        : menu;
    if (!this.openingSetting) return undefined;
    const cached = this.#settings.get(this.settingKey(this.openingSetting));
    return cached ? { ...cached, Loading: true } : undefined;
  }
  private settingKey(command: string): string {
    return `${this.frame?.view.Input.ID}:${this.frame?.view.Caption?.Model}:${command}`;
  }
  async openSetting(command: string): Promise<void> {
    const source = this.#source;
    this.openingSetting = command;
    await this.send({ command });
    if (this.#source === source && this.openingSetting === command) this.openingSetting = undefined;
  }
  private completionKey(): string {
    const editor = this.editor();
    return JSON.stringify([this.frame?.view.Input.ID, editor.text, editor.cursorBack]);
  }
  get liveCompletion() {
    const frame = this.frame;
    const menu = frame?.view.Menu;
    const target = frame?.completionTarget;
    if (
      !menu ||
      menu.Kind !== "" ||
      menu.Dialog ||
      !target ||
      target.Call ||
      target.Question ||
      target.Request ||
      target.Revision !== this.editor().revision
    )
      return undefined;
    return menu;
  }
  get completionMenu() {
    if (this.liveCompletion) {
      const menu = this.liveCompletion;
      return menu.Loading && !menu.Choices.length
        ? { ...menu, Choices: this.#completions.get(this.completionKey())?.Choices ?? [] }
        : menu;
    }
    // A current response with no menu also represents an intentional dismissal.
    if (this.frame?.completionTarget?.Revision === this.editor().revision) return undefined;
    const cached = this.#completions.get(this.completionKey());
    return cached ? { ...cached, Loading: true } : undefined;
  }
  private clearMenus(): void {
    this.#settings.clear();
    this.#completions.clear();
    this.openingSetting = undefined;
  }
  editor(): Editor {
    const id = this.frame?.view.Input.ID || this.#source?.thread || "";
    return this.editors[id] ?? this.emptyEditor;
  }
  private ensureEditor(thread: string): Editor {
    return (this.editors[thread] ??= { text: "", cursorBack: 0, revision: 0, items: [] });
  }
  target(): EditorTarget {
    return { Call: "", Question: "", Request: "", Revision: this.editor().revision };
  }
  metadata() {
    const editor = this.editor();
    return { Target: this.target(), Draft: textDraft(editor.text, editor.cursorBack) };
  }

  prompt(id: string): PromptState {
    const source = this.#source;
    if (!source) throw new Error("A prompt requires an attached control view.");
    source.prompts[id] ??= { drafts: {}, reason: "", sending: false, accepted: false };
    return source.prompts[id];
  }

  private reconcilePrompts(source: ControlView, ids: string[]): void {
    const kept = new Set(ids);
    for (const [id, prompt] of Object.entries(source.prompts)) {
      if (kept.has(id)) delete prompt.resolved;
      else if (prompt.sending) prompt.resolved = true;
      else delete source.prompts[id];
    }
  }

  private retire(source: ControlView): void {
    if (
      source === this.#source ||
      source === this.#destination ||
      this.#views.get(source.thread) !== source ||
      !this.#source?.event ||
      this.#source.connection !== "open" ||
      source.work ||
      source.inputs ||
      source.outcome === "unknown"
    )
      return;
    const frame = source.event?.frame;
    if (
      (!frame && source.received) ||
      source.acknowledged < source.effect ||
      frame?.pendingEffects ||
      frame?.effects.some(
        (effect) =>
          effect.Recovery ||
          source.event?.attachment !== source.applied ||
          effect.ID > source.acknowledged,
      ) ||
      frame?.questions?.length ||
      frame?.approvals?.length ||
      Object.values(source.prompts).some((prompt) => !prompt.resolved || prompt.sending)
    )
      return;
    this.#views.delete(source.thread);
    source.lifetime.abort();
    source.stop?.();
  }

  private retireInactive(): void {
    for (const source of this.#views.values()) this.retire(source);
  }

  connect(thread: string, machine: Machine): void {
    if (this.#source?.thread === thread && this.#source.machine === machine) return;
    if (this.#source && this.#source.machine !== machine) this.close();
    this.cancelResume();
    let source = this.#views.get(thread);
    if (!source) {
      source = controlView(thread, machine);
      this.#views.set(thread, source);
    }
    this.#source = source;
    this.ensureEditor(source.thread);
    this.event = undefined;
    this.clearMenus();
    this.pending = source.pending;
    this.connection = source.connection;
    this.error = source.operationError;
    this.copied = undefined;
    this.connectionError = source.error;
    if (source.event) {
      this.receive(source.event);
      this.retireInactive();
    }
    if (!source.stop) this.subscribe(source);
  }

  // Component cleanup releases only the view it borrowed, not an adopted successor.
  release(thread: string, machine: Machine): void {
    if (this.#source?.machine !== machine) return;
    // Keyed thread components are replaced during ordinary navigation. Their
    // control views retain unresolved input and drafts on the selected machine.
    if (view.machine === machine.id && view.session && view.session !== thread) return;
    this.close();
  }

  retainInput(): (error?: string) => void {
    const source = this.#source;
    if (!source) return () => {};
    source.inputs++;
    return (error) => {
      source.inputs--;
      if (error) source.operationError = error;
      if (this.#source !== source) {
        this.retire(source);
        return;
      }
      if (error) this.failResume(error);
      this.advanceResume();
    };
  }

  private subscribe(source: ControlView): void {
    source.stop = source.machine.subscribe(
      "view/attach",
      {
        get thread() {
          return source.thread;
        },
      },
      (event) => {
        if (source.lifetime.signal.aborted || !["snapshot", "patch"].includes(event.kind)) return;
        if (!source.event && event.kind !== "snapshot") return;
        const initial = !source.event;
        const next = event.data as NativeEvent;
        if (
          initial &&
          (!next.attachment ||
            !next.frame?.view?.Version ||
            (next.frame.view.Main?.ID !== source.thread &&
              !(
                next.frame.switch?.From === source.thread &&
                next.frame.switch.To === next.frame.view.Main?.ID &&
                next.frame.switch.Origin === next.attachment
              )))
        ) {
          const error = "The destination did not provide a valid thread view.";
          if (source === this.#destination) this.failResume(error);
          else {
            source.connection = "lost";
            source.error = error;
            if (source === this.#source) {
              this.connection = "lost";
              this.connectionError = error;
            }
          }
          return;
        }
        source.event = next;
        source.received = true;
        source.connection = "open";
        this.reconcilePrompts(source, [
          ...(next.frame.questions ?? []).map((call) => call.ID),
          ...(next.frame.approvals ?? []).map((approval) => approval.ID),
        ]);
        for (const settle of this.#settle) settle();
        if (source === this.#source) {
          this.connection = "open";
          this.receive(next);
          if (initial) this.retireInactive();
        } else if (source === this.#destination) this.advanceResume();
        else this.retire(source);
      },
      (connection, failure) => {
        if (source.lifetime.signal.aborted) return;
        source.connection = connection;
        source.error = failure?.message;
        if (connection !== "open") source.event = undefined;
        if (source === this.#source) {
          this.connection = connection;
          if (connection !== "open") {
            this.clearMenus();
            this.event = undefined;
          }
          this.connectionError = failure?.message;
        } else if (source === this.#destination && failure) this.failResume(failure.message);
      },
    );
  }

  private receive(next: NativeEvent): void {
    const source = this.#source;
    if (!source) return;
    if (next.frame.view.Input.ID !== this.frame?.view.Input.ID) this.clearMenus();
    if (next.attachment !== source.applied) {
      this.clearMenus();
      source.effect = source.receipt = source.acknowledged = 0;
      source.applied = next.attachment;
    }
    const switched = next.frame.switch;
    if (switched?.To && switched.Origin === next.attachment && switched.From === source.thread) {
      const selected = view.machine === source.machine.id && view.session === source.thread;
      this.cancelResume();
      this.#views.delete(source.thread);
      source.thread = switched.To;
      this.#views.set(source.thread, source);
      if (selected) open(source.thread, true);
    }
    let destination: string | undefined;
    this.ensureEditor(next.frame.view.Input.ID);
    this.event = next;
    const menu = next.frame.view.Menu;
    if (menu && !menu.Loading && !menu.Problem) {
      if (menu.Kind === "settings" && ["/model", "/effort", "/reasoning"].includes(menu.Setting)) {
        this.#settings.set(this.settingKey(menu.Setting), menu);
        if (this.#settings.size > 24) this.#settings.delete(this.#settings.keys().next().value!);
      }
      if (this.liveCompletion) {
        const key = this.completionKey();
        this.#completions.delete(key);
        this.#completions.set(key, menu);
        if (this.#completions.size > 24)
          this.#completions.delete(this.#completions.keys().next().value!);
      }
    }
    for (const settle of this.#settle) settle();
    for (const effect of next.frame.effects ?? []) {
      if (effect.ID <= source.effect) continue;
      if (effect.Recovery) {
        source.effect = effect.ID;
        continue;
      }
      const restored = effect.Restore ?? effect.ShellRestore;
      if (
        restored &&
        (restored.Selections.length || restored.AnswerImages.length || restored.Attachments.length)
      ) {
        this.error =
          "This completion needs the native editor. Use Attachments to include files here.";
        source.effect = effect.ID;
        continue;
      }
      const key = effect.Thread || next.frame.view.Main.ID;
      const editor = this.ensureEditor(key);
      if (effect.Target && effect.Target.Revision !== editor.revision) {
        this.error = "The draft changed before the requested insertion completed.";
      } else {
        if (effect.Action) {
          const action = effect.Action;
          const bytes = new TextEncoder().encode(editor.text);
          const decoder = new TextDecoder();
          const caret = bytes.length - editor.cursorBack;
          if (action.Kind === 1)
            editor.text =
              decoder.decode(bytes.slice(0, caret)) +
              action.Text +
              decoder.decode(bytes.slice(caret));
          else if (action.Kind === 2)
            editor.text =
              decoder.decode(bytes.slice(0, action.Start)) +
              decoder.decode(bytes.slice(action.End));
          else if (action.Kind === 3) editor.cursorBack = bytes.length - action.Start;
          else this.error = "This editor action is available in the native terminal.";
          editor.revision++;
        }
        if (restored) {
          // Completed `$skill` text rebinds at submission. Completed files and
          // images become host paths; an image's `[Image N]` placeholder, in
          // text order, is dropped because its attachment adds its own.
          const bytes = new TextEncoder().encode(restored.Text);
          const decoder = new TextDecoder();
          let text = "";
          let at = 0;
          for (const image of restored.Images) {
            text += decoder.decode(bytes.slice(at, image.Start));
            at = image.End;
          }
          text += decoder.decode(bytes.slice(at));
          for (const token of [...restored.Files, ...restored.Images])
            if (!editor.items.some((item) => "path" in item && item.path === token.Path))
              editor.items.push({ id: newIdentity(), path: token.Path });
          editor.text = [text, editor.text].filter(Boolean).join("\n");
          editor.revision++;
        }
        if (effect.Selection) {
          editor.text += `\n${effect.Selection.Text}`;
          editor.revision++;
        }
        if (effect.Recall) {
          editor.text = effect.Recall;
          editor.cursorBack = 0;
          editor.items = [];
          editor.revision++;
        }
      }
      if (effect.Clipboard !== null) this.copied = effect.Clipboard;
      source.effect = effect.ID;
      if (effect.Reattach) destination = effect.Reattach;
    }
    if (source.effect > source.receipt) {
      const effect = (source.receipt = source.effect);
      void this.send(
        {
          receipt: {
            Effect: effect,
            Notice: { Attachment: 0, Claim: "", Feedback: 0, Composer: false, Content: false },
          },
        },
        undefined,
        true,
      ).then((error) => {
        if (source.event?.attachment !== next.attachment) return;
        if (error) {
          source.operationError = error;
          if (this.#source === source) {
            this.error = error;
            this.failResume(error);
          }
          return;
        }
        source.acknowledged = effect;
        if (this.#source === source) this.advanceResume();
        else this.retire(source);
      });
    }
    if (destination && view.machine === source.machine.id && view.session === source.thread)
      this.resume(destination);
    this.advanceResume();
  }

  private resume(thread: string): void {
    const source = this.#source;
    if (!source || thread === source.thread || this.#destination?.thread === thread) return;
    this.cancelResume();
    this.#destination = controlView(thread, source.machine);
    this.resuming = "Opening resumed thread…";
    this.subscribe(this.#destination);
  }

  private failResume(error: string): void {
    if (!this.#destination) return;
    this.cancelResume();
    this.error = `Could not resume: ${error} The current thread is still open. Try resuming again.`;
  }

  private cancelResume(): void {
    this.#destination?.lifetime.abort();
    this.#destination?.stop?.();
    this.#destination = undefined;
    this.resuming = undefined;
  }

  private advanceResume(): void {
    const source = this.#source,
      destination = this.#destination;
    if (!source || !destination?.event || !this.frame) return;
    if (view.machine !== source.machine.id || view.session !== source.thread) {
      this.cancelResume();
      return;
    }
    if (
      source.work ||
      source.inputs ||
      source.acknowledged < source.effect ||
      this.frame.pendingEffects ||
      this.frame.effects?.some((effect) => effect.Recovery)
    ) {
      this.resuming = "Waiting for current input before resuming…";
      return;
    }
    this.#destination = undefined;
    const previous = this.#views.get(destination.thread);
    previous?.lifetime.abort();
    previous?.stop?.();
    this.#views.set(destination.thread, destination);
    this.#source = destination;
    this.event = undefined;
    this.pending = destination.pending;
    this.error = this.connectionError = this.resuming = undefined;
    this.connection = "open";
    open(destination.thread, true);
    this.receive(destination.event);
    this.retireInactive();
  }

  close(): void {
    this.clearMenus();
    this.cancelResume();
    for (const source of this.#views.values()) {
      source?.lifetime.abort();
      source?.stop?.();
    }
    this.#views.clear();
    this.#source = undefined;
    this.event = undefined;
    this.connection = "connecting";
    this.error = undefined;
    this.connectionError = undefined;
    this.resuming = undefined;
    this.editors = {};
    this.copied = undefined;
    this.pending = 0;
  }
  async send(
    operation: RemoteOperation,
    signal?: AbortSignal,
    quiet = false,
    event = this.event,
  ): Promise<string | undefined> {
    const active = this.#source;
    const source =
      active?.event?.attachment === event?.attachment
        ? active
        : [...this.#views.values(), this.#destination].find(
            (candidate) => candidate && candidate.event?.attachment === event?.attachment,
          );
    if (!event || !source || source.connection !== "open") {
      const error =
        event && this.event && event.attachment !== this.event.attachment
          ? "The control view changed. Select the action again."
          : "Wait for this thread's control view to connect.";
      if (!quiet) this.error = error;
      return error;
    }
    const { machine, thread } = source;
    const lifetime = source.lifetime.signal;
    const body: RemoteMutation = {
      attachment: event.attachment,
      version: event.frame.view.Version,
      operation,
    };
    const requestSignal = signal ? AbortSignal.any([signal, lifetime]) : lifetime;
    const run = async () => {
      if (requestSignal.aborted || source.event?.attachment !== body.attachment) {
        const error = "The control view changed. Select the action again.";
        if (!quiet) source.operationError = error;
        if (!quiet && !lifetime.aborted && this.#source === source) this.error = error;
        return error;
      }
      if (!quiet) {
        source.operationError = undefined;
        source.outcome = undefined;
        source.pending++;
        if (this.#source === source) this.pending = source.pending;
        if (this.#source === source) this.error = undefined;
      }
      try {
        const result = await machine.request<NativeResult>(
          "view/intent",
          { thread, body },
          { signal: requestSignal },
        );
        if (!quiet) await this.settled(source, body.attachment, result.version, requestSignal);
        return undefined;
      } catch (failure) {
        // A stale-target refusal asks for the action again on the current view.
        const error =
          failure instanceof TransportError &&
          failure.status === 409 &&
          /refresh/i.test(failure.message)
            ? stale
            : errorText(failure, true);
        if (!lifetime.aborted && !quiet) {
          source.operationError = error;
          source.outcome = failure instanceof TransportError ? failure.kind : "unknown";
          if (this.#source === source) {
            this.error = error;
            this.failResume(error);
          }
        }
        return error;
      } finally {
        if (!quiet) {
          source.pending--;
          if (this.#source === source) this.pending = source.pending;
        }
      }
    };
    source.work++;
    const result = source.queue.then(run, run).finally(() => {
      source.work--;
      if (this.#source === source) this.advanceResume();
      else this.retire(source);
    });
    source.queue = result;
    return result;
  }
  private settled(
    source: ControlView,
    attachment: string,
    version: Version,
    signal: AbortSignal,
  ): Promise<void> {
    return new Promise((resolve, reject) => {
      const done = (error?: Error) => {
        clearTimeout(timer);
        signal.removeEventListener("abort", abort);
        this.#settle.delete(check);
        if (error) reject(error);
        else resolve();
      };
      const check = () => {
        const current = source.event;
        if (current?.attachment !== attachment)
          return done(new Error("The view changed before confirmation."));
        if (
          current.frame.view.Version.Attachment === version.Attachment &&
          current.frame.view.Version.Revision >= version.Revision
        )
          done();
      };
      const abort = () => done(new Error("The view disconnected before confirmation."));
      const timer = setTimeout(
        () => done(new Error("The operation was admitted but its updated view has not arrived.")),
        10000,
      );
      this.#settle.add(check);
      signal.addEventListener("abort", abort, { once: true });
      if (signal.aborted) abort();
      else check();
    });
  }
  async steerWaiting(): Promise<void> {
    const event = this.event;
    if (!event) return;
    await this.send({
      composer: {
        Intent: {
          Thread: event.frame.view.Main.ID,
          Version: event.frame.view.Version,
          Target: this.target(),
          Operation: 10,
          Work: event.frame.view.Interrupt,
        },
        Draft: textDraft(),
        Consumed: false,
      },
    });
  }
  async editWaiting(): Promise<void> {
    const event = this.event;
    if (!event) return;
    const known = new Set(event.frame.effects.map((effect) => effect.ID));
    const error = await this.send({
      composer: {
        Intent: {
          Thread: event.frame.view.Main.ID,
          Version: event.frame.view.Version,
          Target: this.target(),
          Operation: 6,
          Work: event.frame.view.Interrupt,
        },
        Draft: textDraft(),
        Consumed: false,
      },
    });
    if (error || this.event?.attachment !== event.attachment) return;
    for (const effect of this.frame?.effects ?? []) {
      if (effect.Recovery && !known.has(effect.ID))
        await this.send({ recovery: { effect: effect.ID, action: "edit" } });
    }
  }
  // Typing refreshes completions quietly.
  complete(): Promise<string | undefined> {
    return this.send({ completion: { ...this.metadata(), Explicit: false } }, undefined, true);
  }
}
export const semantic = new Semantic();
