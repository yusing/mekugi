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
};

type ControlView = {
  machine: Machine;
  thread: string;
  lifetime: AbortController;
  stop?: () => void;
  event?: NativeEvent;
};

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
  #source?: ControlView;
  #destination?: ControlView;
  #work = 0;
  #inputs = 0;
  #queue: Promise<unknown> = Promise.resolve();
  #effect = 0;
  #acknowledged = 0;
  #receipt = 0;
  #settle = new Set<() => void>();

  get frame() {
    return this.event?.frame;
  }
  // Main's model and effort menus open from the composer rather than as dialogs.
  get settingsMenu() {
    const shown = this.frame?.view;
    const menu = shown?.Menu;
    if (!shown || !menu || menu.Kind !== "settings" || shown.Displayed.ID !== shown.Main.ID)
      return undefined;
    return ["/model", "/effort", "/reasoning"].includes(menu.Setting) ? menu : undefined;
  }
  editor(): Editor {
    const id = this.frame?.view.Input.ID || this.#source?.thread || "";
    return this.editors[id] ?? this.emptyEditor;
  }
  target(): EditorTarget {
    return { Call: "", Question: "", Request: "", Revision: this.editor().revision };
  }
  metadata() {
    const editor = this.editor();
    return { Target: this.target(), Draft: textDraft(editor.text, editor.cursorBack) };
  }

  connect(thread: string, machine: Machine): void {
    if (this.#source?.thread === thread && this.#source.machine === machine) return;
    this.close();
    this.#source = { thread, machine, lifetime: new AbortController() };
    this.subscribe(this.#source);
  }

  // Component cleanup releases only the view it borrowed, not an adopted successor.
  release(thread: string, machine: Machine): void {
    if (this.#source?.thread === thread && this.#source.machine === machine) this.close();
  }

  retainInput(): (error?: string) => void {
    const source = this.#source;
    if (!source) return () => {};
    this.#inputs++;
    return (error) => {
      if (this.#source !== source) return;
      this.#inputs--;
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
        const next = event.data as NativeEvent;
        if (
          source === this.#destination &&
          (!next.attachment ||
            next.frame?.view?.Main?.ID !== source.thread ||
            !next.frame.view.Version)
        ) {
          this.failResume("The destination did not provide a valid thread view.");
          return;
        }
        source.event = next;
        if (source === this.#source) {
          this.connection = "open";
          this.receive(next);
        } else if (source === this.#destination) this.advanceResume();
      },
      (connection, failure) => {
        if (source.lifetime.signal.aborted) return;
        if (connection !== "open") source.event = undefined;
        if (source === this.#source) {
          this.connection = connection;
          if (connection !== "open") this.event = undefined;
          this.connectionError = failure?.message;
        } else if (source === this.#destination && failure) this.failResume(failure.message);
      },
    );
  }

  private receive(next: NativeEvent): void {
    const source = this.#source;
    if (!source) return;
    if (next.attachment !== this.event?.attachment)
      this.#effect = this.#receipt = this.#acknowledged = 0;
    const switched = next.frame.switch;
    if (switched?.To && switched.Origin === next.attachment && switched.From === source.thread) {
      const selected = view.machine === source.machine.id && view.session === source.thread;
      this.cancelResume();
      source.thread = switched.To;
      if (selected) open(source.thread, true);
    }
    let destination: string | undefined;
    this.editors[next.frame.view.Input.ID] ??= {
      text: "",
      cursorBack: 0,
      revision: 0,
      items: [],
    };
    this.event = next;
    for (const settle of this.#settle) settle();
    for (const effect of next.frame.effects ?? []) {
      if (effect.ID <= this.#effect) continue;
      if (effect.Recovery) {
        this.#effect = effect.ID;
        continue;
      }
      const restored = effect.Restore ?? effect.ShellRestore;
      if (
        restored &&
        (restored.Selections.length || restored.AnswerImages.length || restored.Attachments.length)
      ) {
        this.error =
          "This completion needs the native editor. Use Attachments to include files here.";
        this.#effect = effect.ID;
        continue;
      }
      const key = effect.Thread || next.frame.view.Main.ID;
      const editor = (this.editors[key] ??= {
        text: "",
        cursorBack: 0,
        revision: 0,
        items: [],
      });
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
      }
      if (effect.Clipboard !== null) this.copied = effect.Clipboard;
      this.#effect = effect.ID;
      if (effect.Reattach) destination = effect.Reattach;
    }
    if (this.#effect > this.#receipt) {
      const effect = (this.#receipt = this.#effect);
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
        if (this.#source !== source || this.event?.attachment !== next.attachment) return;
        if (error) {
          this.failResume(error);
          return;
        }
        this.#acknowledged = effect;
        this.advanceResume();
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
    this.#destination = { thread, machine: source.machine, lifetime: new AbortController() };
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
      this.#work ||
      this.#inputs ||
      this.#acknowledged < this.#effect ||
      this.frame.pendingEffects ||
      this.frame.effects?.some((effect) => effect.Recovery)
    ) {
      this.resuming = "Waiting for current input before resuming…";
      return;
    }
    this.#destination = undefined;
    this.#source = destination;
    source.lifetime.abort();
    source.stop?.();
    this.event = undefined;
    this.editors = {};
    this.error = this.connectionError = this.resuming = undefined;
    this.connection = "open";
    open(destination.thread, true);
    this.receive(destination.event);
  }

  close(): void {
    this.cancelResume();
    this.#source?.lifetime.abort();
    this.#source?.stop?.();
    this.#source = undefined;
    this.event = undefined;
    this.connection = "connecting";
    this.error = undefined;
    this.connectionError = undefined;
    this.resuming = undefined;
    this.editors = {};
    this.copied = undefined;
    this.#effect = this.#receipt = this.#acknowledged = 0;
    this.pending = this.#work = this.#inputs = 0;
    this.#queue = Promise.resolve();
  }
  async send(
    operation: RemoteOperation,
    signal?: AbortSignal,
    quiet = false,
    event = this.event,
  ): Promise<string | undefined> {
    const source = this.#source;
    if (!event || !source || this.connection !== "open") {
      const error = "Wait for this thread's control view to connect.";
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
      if (requestSignal.aborted || this.event?.attachment !== body.attachment) {
        const error = "The control view changed. Select the action again.";
        if (!quiet && !lifetime.aborted) this.error = error;
        return error;
      }
      if (!quiet) {
        this.pending++;
        this.error = undefined;
      }
      try {
        const result = await machine.request<NativeResult>(
          "view/intent",
          { thread, body },
          { signal: requestSignal },
        );
        if (!quiet) await this.settled(body.attachment, result.version, requestSignal);
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
          this.error = error;
          this.failResume(error);
        }
        return error;
      } finally {
        if (!quiet && this.#source === source) this.pending--;
      }
    };
    this.#work++;
    const result = this.#queue.then(run, run).finally(() => {
      if (this.#source !== source) return;
      this.#work--;
      this.advanceResume();
    });
    this.#queue = result;
    return result;
  }
  private settled(attachment: string, version: Version, signal: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
      const done = (error?: Error) => {
        clearTimeout(timer);
        signal.removeEventListener("abort", abort);
        this.#settle.delete(check);
        if (error) reject(error);
        else resolve();
      };
      const check = () => {
        const current = this.event;
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
  // Typing refreshes completions quietly.
  complete(): Promise<string | undefined> {
    return this.send({ completion: { ...this.metadata(), Explicit: false } }, undefined, true);
  }
}
export const semantic = new Semantic();
