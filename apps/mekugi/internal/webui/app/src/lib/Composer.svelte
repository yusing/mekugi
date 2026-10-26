<script lang="ts">
  import Code from "./Code.svelte";
  import Alert from "./ui/Alert.svelte";
  import { onDestroy, tick, untrack } from "svelte";
  import type { ChoiceID } from "../../../../sessionapi/protocol";
  import Attachments from "./Attachments.svelte";
  import { hub } from "./hub.svelte";
  import { prepareAttachments, type InitialInput } from "./attachments";
  import type { Attachment, Input } from "./protocol";
  import { semantic, textDraft } from "./semantic.svelte";
  import { stream, type Stream } from "./stream.svelte";
  import Button from "./ui/Button.svelte";
  import ComposerCard from "./ui/ComposerCard.svelte";
  import Menu from "./ui/Menu.svelte";
  import MenuItem from "./ui/MenuItem.svelte";
  import { composerText, composerTools, floating } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";
  import ThreadSettings from "./ThreadSettings.svelte";

  // The thread's composer, as in the terminal: a leading slash is a command,
  // a leading ! a shell command on the execution machine, and / @ $ open the
  // server's completions. Enter sends or steers; Tab queues for the next turn.
  let {
    input,
    machine,
    unavailable,
    source = stream,
    agentName = "main",
    role,
    initial,
    oninitial,
  }: {
    input?: Input;
    machine: string;
    unavailable?: string;
    source?: Stream;
    agentName?: string;
    role?: string;
    initial?: InitialInput;
    oninitial?: () => void;
  } = $props();
  const editor = $derived(semantic.editor());
  const view = $derived(semantic.frame?.view);
  const previewThread = $derived(view?.Input.ID);
  const previewTarget = $derived(
    hub.selected && previewThread ? { machine: hub.selected, thread: previewThread } : undefined,
  );
  const shell = $derived(editor.text.startsWith("!"));
  const pending = $derived(editor.submission);
  let acquisition: AbortController | undefined;
  let stopping = $state(false);
  let error = $state<string>();
  let progress = $state("");
  let pathing = $state(false);
  let field = $state<HTMLTextAreaElement>();
  $effect(() => {
    if (editor.focus) untrack(() => void tick().then(() => field?.focus()));
  });
  let syntax = $state<HTMLElement>();
  let tray = $state<ReturnType<typeof Attachments>>();
  const touch = matchMedia("(pointer: coarse)").matches;
  const running = $derived(!!input?.stoppable);
  const empty = $derived(!editor.text.trim() && !editor.items.length);
  const refusal = $derived(
    unavailable ??
      (shell
        ? undefined
        : view?.Admission.Reason || (input ? input.reason : "Waiting for the session…")),
  );
  // Navigation may cancel file acquisition, but admitted input belongs to its
  // retained control view until confirmation or explicit cancellation.
  onDestroy(() => acquisition?.abort());

  // Completions belong to the draft revision they were computed for.
  let dismissed = $state<string>();
  const completionIdentity = $derived(
    JSON.stringify([semantic.event?.attachment, view?.Input.ID, editor.revision]),
  );
  const completion = $derived(
    dismissed === completionIdentity ? undefined : semantic.completionMenu,
  );
  let active = $state(0);
  $effect(() => {
    const menu = completion;
    if (!menu) return;
    const preferred = menu.Choices.findIndex(
      (choice) => choice.ID === menu.Preferred.ID && choice.Name === menu.Preferred.Name,
    );
    untrack(() => (active = Math.max(0, preferred)));
  });
  $effect(() => {
    if (completion)
      document.getElementById(`completion-${active}`)?.scrollIntoView({ block: "nearest" });
  });
  function choose(kind: number, choice: ChoiceID = { ID: 0, Name: "", Path: "" }) {
    if (kind === 1) dismissed = completionIdentity;
    const menu = semantic.liveCompletion;
    if (!view || !menu || menu.Loading) return;
    void semantic.send({
      menu: { Version: view.Version, Menu: menu.ID, Kind: kind, Choice: choice, Query: "" },
      editor: semantic.metadata(),
    });
    field?.focus();
  }

  // One quiet completion request at a time; the newest draft is sampled when
  // it finishes. Drafts without a trigger character need none, except to
  // close a picker that is still open.
  let asked = "";
  let asking = false;
  function refresh() {
    const frame = semantic.frame;
    if (asking || !frame || semantic.connection !== "open") return;
    const current = semantic.editor();
    const key = `${semantic.event?.attachment}:${frame.view.Input.ID}:${current.revision}`;
    if (key === asked || !(/[/@$]/.test(current.text) || frame.view.Menu?.Kind === "")) return;
    asked = key;
    asking = true;
    void semantic.complete().finally(() => {
      asking = false;
      refresh();
    });
  }
  $effect(() => {
    void editor.revision;
    void semantic.frame?.view.Input.ID;
    void semantic.event?.attachment;
    untrack(refresh);
  });

  $effect(() => {
    const first = initial;
    if (!first || semantic.connection !== "open" || !semantic.event || refusal) return;
    untrack(() => {
      oninitial?.();
      editor.text = first.text;
      editor.items = first.items;
      editor.cursorBack = 0;
      editor.revision++;
      void send(false, first.attachments);
    });
  });
  function caret() {
    return new TextEncoder().encode(editor.text.slice(field?.selectionEnd ?? editor.text.length))
      .length;
  }
  function edited() {
    editor.revision++;
    editor.cursorBack = caret();
    error = undefined;
  }
  async function rewrite(text: string) {
    editor.text = text;
    editor.cursorBack = 0;
    editor.revision++;
    error = undefined;
    await tick();
    field?.focus();
    field?.setSelectionRange(text.length, text.length);
  }
  async function send(queue = false, initialAttachments?: Attachment[]) {
    if (empty || pending || refusal) return;
    const destination = editor;
    const controller = new AbortController();
    destination.submission = controller;
    const release = semantic.retainInput();
    const text = destination.text,
      items = destination.items,
      back = destination.cursorBack;
    const capturedEvent = semantic.event;
    const captured = capturedEvent?.frame;
    const target = semantic.target();
    // The server clears a command's text once it applies; other input leaves
    // the editor now and returns if it is refused.
    const command = text.trimStart().startsWith("/") && !items.length;
    let detached: number | undefined;
    try {
      if (!captured) {
        error = "Wait for the thread controls to connect.";
        return;
      }
      acquisition = !command && !initialAttachments ? controller : undefined;
      const prepared = command
        ? []
        : (initialAttachments ??
          (await prepareAttachments(
            items,
            controller.signal,
            (read, total) =>
              (progress = total
                ? `Reading files: ${Math.round((read / total) * 100)}%`
                : "Preparing input…"),
          )));
      acquisition = undefined;
      progress = command ? "Running command…" : "Sending…";
      if (!command) {
        destination.text = "";
        destination.items = [];
        destination.cursorBack = 0;
        detached = ++destination.revision;
      }
      error = await semantic.send(
        {
          composer: {
            Intent: {
              Thread: captured.view.Input.ID,
              Version: captured.view.Version,
              Target: target,
              Operation: queue ? 2 : 1,
            },
            // Remote input travels only in its public descriptor.
            Draft: textDraft(),
            Consumed: !command,
            input: { text, attachments: prepared },
          },
        },
        controller.signal,
        false,
        capturedEvent,
      );
    } catch (failure) {
      error = controller.signal.aborted
        ? "Attachment preparation cancelled."
        : failure instanceof Error
          ? failure.message
          : "Could not prepare input.";
    } finally {
      acquisition = undefined;
      if (error && detached !== undefined && destination.revision === detached) {
        destination.text = text;
        destination.items = items;
        destination.cursorBack = back;
        destination.revision++;
      }
      if (destination.submission === controller) destination.submission = undefined;
      progress = "";
      field?.focus();
      release(error);
    }
  }
  async function stop() {
    stopping = true;
    error = await source.stop();
    stopping = false;
    field?.focus();
  }
  function onkeydown(event: KeyboardEvent) {
    if (event.isComposing) return;
    const choices = completion?.Choices ?? [];
    if (completion && choices.length) {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const step = event.key === "ArrowDown" ? 1 : -1;
        active = (active + step + choices.length) % choices.length;
        return;
      }
      if ((event.key === "Enter" && !event.shiftKey) || (event.key === "Tab" && !event.shiftKey)) {
        event.preventDefault();
        const choice = choices[active];
        choose(2, { ID: choice.ID, Name: choice.Name, Path: choice.Path });
        return;
      }
    }
    if (completion && event.key === "Escape") {
      event.preventDefault();
      choose(1);
      return;
    }
    if (event.key === "Enter" && !event.shiftKey && !touch) {
      event.preventDefault();
      void send();
    } else if (event.key === "Tab" && !event.shiftKey && running && !shell && !empty && !refusal) {
      event.preventDefault();
      void send(true);
    }
  }
  function onpaste(event: ClipboardEvent) {
    const files = event.clipboardData?.files;
    if (!files?.length || shell || pending) return;
    if (!event.clipboardData?.types.includes("text/plain")) event.preventDefault();
    tray?.add(files);
  }
  const hint = $derived(
    error ||
      progress ||
      (pending ? "Sending…" : undefined) ||
      refusal ||
      source.notice ||
      (touch
        ? ""
        : shell
          ? `Enter runs this on the machine. Remove the leading ! to message ${agentName}.`
          : running
            ? `Enter steers ${agentName}'s turn · Tab queues for the next turn · Shift+Enter adds a line`
            : "Enter sends · Shift+Enter adds a line · / for commands · ! for shell"),
  );
</script>

<div class="relative mx-auto w-full max-w-column px-4 pb-2.5 max-sm:px-2 max-sm:pb-safe-1.5">
  {#if completion}
    <div
      class="menu-enter absolute inset-x-4 bottom-[calc(100%-4px)] z-5 max-h-[min(320px,45dvh)] overflow-y-auto p-1 {floating} max-sm:inset-x-2"
    >
      {#if completion.Problem}<Alert class="px-2.5 py-2">
          {completion.Problem}
        </Alert>
      {:else if completion.Loading && !completion.Choices.length}<p
          class="px-2.5 py-2 text-small text-muted"
          role="status"
        >
          Loading suggestions…
        </p>
      {:else if !completion.Choices.length}<p
          class="px-2.5 py-2 text-small text-muted"
          role="status"
        >
          No matches.
        </p>{/if}
      {#if completion.Choices.length}
        <ul id="completions" role="listbox" aria-label="Suggestions">
          {#each completion.Choices as choice, i (JSON.stringify( [choice.ID, choice.Name, choice.Path] ))}
            <li
              id="completion-{i}"
              role="option"
              aria-selected={i === active}
              aria-disabled={!semantic.liveCompletion || completion.Loading}
              class="flex min-h-[34px] cursor-pointer items-baseline gap-3 rounded-control px-2.5 py-[7px] aria-selected:bg-active max-sm:min-h-11 max-sm:items-center"
              onpointerdown={(event) => event.preventDefault()}
              onclick={() => choose(2, { ID: choice.ID, Name: choice.Name, Path: choice.Path })}
              onkeydown={() => {}}
            >
              <span class="max-w-1/2 flex-none truncate font-mono"
                >{choice.Display || choice.Name}</span
              >
              {#if choice.Description}<span
                  class="min-w-0 truncate text-small text-muted max-sm:hidden"
                  >{choice.Description}</span
                >{/if}
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
  <ComposerCard
    accepts={!shell && !pending}
    onfiles={(files) => tray?.add(files)}
    onsubmit={() => void send()}
    invalid={!!error}
  >
    <Attachments
      bind:this={tray}
      bind:items={editor.items}
      bind:pathing
      {machine}
      {previewTarget}
      disabled={!!pending || shell}
    />
    <label class="sr-only" for="composer-text">{shell ? "Shell command" : "Message"}</label>
    <div class="relative">
      {#if shell}<div
          class="pointer-events-none absolute inset-0 overflow-hidden"
          aria-hidden="true"
          bind:this={syntax}
        >
          <pre
            class="{composerText} min-h-12 pt-3 pb-1 font-mono whitespace-pre-wrap wrap-break-word"><Code
              code={editor.text + (editor.text.endsWith("\n") ? " " : "")}
              lang="bash"
            /></pre>
        </div>{/if}
      <textarea
        id="composer-text"
        class={[composerText, "min-h-12 pt-3 pb-1", shell && "font-mono"]}
        role="combobox"
        aria-expanded={!!completion?.Choices.length}
        aria-controls={completion?.Choices.length ? "completions" : undefined}
        aria-activedescendant={completion?.Choices.length ? `completion-${active}` : undefined}
        aria-autocomplete="list"
        bind:this={field}
        bind:value={editor.text}
        style:color={shell ? "transparent" : undefined}
        style:caret-color="var(--text)"
        onscroll={() => {
          if (syntax && field) {
            syntax.scrollTop = field.scrollTop;
            syntax.scrollLeft = field.scrollLeft;
          }
        }}
        {onkeydown}
        {onpaste}
        oninput={edited}
        onselect={() => {
          const at = caret();
          if (at !== editor.cursorBack) {
            editor.cursorBack = at;
            editor.revision++;
          }
        }}
        readonly={!!pending || !!initial}
        rows="1"
        placeholder={shell
          ? `Shell command on ${machine}`
          : running
            ? `Steer ${agentName}'s turn`
            : `Message ${agentName}`}
        aria-describedby="composer-hint"
        aria-invalid={error ? "true" : undefined}></textarea>
    </div>
    <!-- A query container: where one row cannot hold every control, the thread
      settings take their own row (ThreadSettings) and Send keeps the corner. -->
    <div class="{composerTools} @container flex-wrap gap-x-0.5 gap-y-1">
      <Menu label="Add files or paths" icon="plus" disabled={!!pending || shell}>
        {#snippet children(close)}
          <MenuItem
            icon="attach"
            onclick={() => {
              close();
              tray?.pick();
            }}>Attach files</MenuItem
          >
          <MenuItem
            icon="folder"
            onclick={() => {
              close();
              pathing = true;
            }}>Attach path on {machine}</MenuItem
          >
        {/snippet}
      </Menu>
      <Button
        variant="quiet"
        icon="slash"
        label="Start a command"
        title="Start a command (/)"
        disabled={!!pending || !!editor.text || !!initial}
        onclick={() => rewrite("/")}
      />
      <Button
        variant="quiet"
        icon="terminal"
        label="Shell command"
        title="Run a shell command on {machine} (!)"
        aria-pressed={shell}
        disabled={!!pending || editor.items.length > 0 || !!initial}
        onclick={() => rewrite(shell ? editor.text.slice(1) : `!${editor.text}`)}
      />
      {#if shell}<Tag mono class="ml-1.5">Shell</Tag>{/if}
      <!-- On phones the breadcrumb above already names the role. -->
      {#if role}<Tag class="ml-1.5 max-sm:hidden">{role}</Tag>{/if}
      {#if view && view.Displayed.ID === view.Main.ID}
        <Button
          variant="quiet"
          icon="presets"
          label="Presets"
          disabled={!!pending || !!unavailable || !!initial}
          onclick={() => semantic.send({ command: "/presets", editor: semantic.metadata() })}
        />
      {/if}
      {#if view && view.Displayed.ID === view.Main.ID}<ThreadSettings
          caption={view.Caption}
          disabled={!!pending || !!unavailable}
        />
      {:else if !shell && view?.Caption.Model}<span
          class="ml-1.5 min-w-0 truncate text-small text-muted max-sm:hidden"
          title="Model and reasoning effort"
          >{view.Caption.Model}{#if view.Caption.Effort}<span class="ml-2"
              >{view.Caption.Effort}</span
            >{/if}</span
        >{/if}
      <span class="flex-1"></span>
      {#if pending}<Button variant="quiet" onclick={() => pending?.abort()}>Cancel</Button>
      {/if}
      <div class="ml-auto flex flex-none items-center gap-2">
        {#if input?.stoppable}<Button
            shape="round"
            icon="stop"
            label="Stop"
            title={`Stop ${agentName}'s turn`}
            onclick={stop}
            disabled={stopping || !!unavailable}
          />{/if}
        <Button
          type="submit"
          variant="primary"
          shape="round"
          icon="send"
          label="Send"
          title={running ? "Steer (Enter)" : "Send (Enter)"}
          disabled={!!pending || !!refusal || empty}
        />
      </div>
    </div>
  </ComposerCard>
  <p
    id="composer-hint"
    class={["mx-1 mt-1.5 min-h-4 text-small", error ? "text-negative" : "text-muted"]}
    aria-live="polite"
  >
    {hint}
  </p>
</div>
