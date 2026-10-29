<script lang="ts">
  import { tick, untrack } from "svelte";
  import AgentName from "./AgentName.svelte";
  import AutoPage from "./AutoPage.svelte";
  import { workspacePath } from "./diff";
  import DiffView from "./DiffView.svelte";
  import Path from "./Path.svelte";
  import type { Counts, ReviewedFile, SavedReview, PageCursor, PageResult } from "./protocol";
  import { stream } from "./stream.svelte";
  import Alert from "./ui/Alert.svelte";
  import Button from "./ui/Button.svelte";
  import { errorText } from "./transport";
  import Picker, { type Choice } from "./ui/Picker.svelte";
  import { caret, emptyBox, skeleton } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";

  // The saved changes of one agent, or of the whole session, one section per
  // file with its net diff. It follows owner, the agent the transcript
  // shows, until the reader picks another.
  let { review, owner }: { review: SavedReview | null; owner: string } = $props();

  // "" is every agent; "?" is changes recorded without an agent.
  let caller = $derived(owner);
  let metadata = $state.raw<ReviewedFile[]>([]);
  let complete = $state.raw<
    Record<string, { reference: ReviewedFile["content"]; file: ReviewedFile }>
  >({});
  let pending = $state.raw<Record<string, boolean>>({});
  let failures = $state.raw<Record<string, string>>({});
  let next = $state.raw<PageCursor | null>();
  let pageLoading = $state(false);
  let pageError = $state<string>();
  let request = new AbortController();
  let container = $state<HTMLElement>();
  let refreshing = $state(false);
  let acquiredPage = $state.raw<PageCursor>();
  let windowOwner = "";
  let windowRows = 0;
  const pageIdentity = $derived(review?.page ? JSON.stringify({ ...review.page, caller }) : "");
  $effect(() => {
    // Catch-up snapshots replace objects, not the underlying review source.
    // Keep independent HTTP reads alive across control reconnections.
    void pageIdentity;
    const page = untrack(() => review?.page);
    const selected = caller;
    const identity = `${page?.attachment}:${selected}`;
    untrack(() => {
      if (identity !== windowOwner) {
        windowRows = 0;
        metadata = [];
        complete = {};
        acquiredPage = undefined;
      }
      windowOwner = identity;
    });
    request.abort();
    request = new AbortController();
    pending = {};
    failures = {};
    pageLoading = false;
    pageError = undefined;
    next = page ? { ...page, caller: selected } : undefined;
    refreshing = false;
    if (page) untrack(() => void refreshPages());
    return () => request.abort();
  });
  $effect(() => {
    if (stream.connection !== "open") return;
    // Metadata travels over the control channel, unlike file content.
    // Reacquire a failed window after that channel reconnects.
    untrack(() => {
      if (pageError) void refreshPages();
    });
  });
  function captureAnchor() {
    const scroller = container?.closest<HTMLElement>(".overflow-y-auto");
    if (!scroller) return;
    const top = scroller.getBoundingClientRect().top;
    const row = [...container!.querySelectorAll<HTMLElement>("[data-file]")].find(
      (node) => node.getBoundingClientRect().bottom > top,
    );
    return {
      scroller,
      key: row?.dataset.file,
      focus: document.activeElement?.closest<HTMLElement>("[data-file]")?.dataset.file,
      offset: (row?.getBoundingClientRect().top ?? top) - top,
      top: scroller.scrollTop,
    };
  }
  function restoreAnchor(anchor: ReturnType<typeof captureAnchor>) {
    if (!anchor || container?.closest(".overflow-y-auto") !== anchor.scroller) return;
    const { scroller } = anchor;
    const row = [...container.querySelectorAll<HTMLElement>("[data-file]")].find(
      (node) => node.dataset.file === anchor.key,
    );
    scroller.scrollTop = row
      ? scroller.scrollTop +
        row.getBoundingClientRect().top -
        scroller.getBoundingClientRect().top -
        anchor.offset
      : anchor.top;
    if (anchor.focus && document.activeElement === document.body) {
      [...container.querySelectorAll<HTMLElement>("[data-file]")]
        .find((node) => node.dataset.file === anchor.focus)
        ?.querySelector<HTMLElement>("summary")
        ?.focus({ preventScroll: true });
    }
  }
  async function refreshPages() {
    if (refreshing) return;
    const page = review?.page;
    if (!page) return;
    const signal = request.signal;
    // A revision refreshes only the window already acquired by this reader.
    const wanted = windowRows;
    refreshing = true;
    pageError = undefined;
    let cursor: PageCursor | null | undefined = { ...page, caller };
    const updated: ReviewedFile[] = [];
    // Keep the acquired window visible until its replacement is ready.
    try {
      do {
        const result = await stream.page(cursor, signal);
        try {
          if (signal.aborted) return;
          updated.push(...((result.files ?? []) as ReviewedFile[]));
          cursor = result.next;
        } finally {
          result.acknowledge?.();
        }
      } while (cursor && updated.length < wanted);
      const anchor = captureAnchor();
      metadata = updated;
      complete = Object.fromEntries(
        updated.flatMap((file) => {
          const cached = complete[file.key];
          return cached ? [[file.key, cached]] : [];
        }),
      );
      next = cursor;
      acquiredPage = page;
      await tick();
      if (signal.aborted) return;
      restoreAnchor(anchor);
      windowRows = metadata.length;
    } catch (error) {
      if (!signal.aborted) pageError = errorText(error);
    }
    if (signal.aborted) return;
    refreshing = false;
  }
  async function loadPage(demand?: AbortSignal) {
    const cursor = next,
      session = request.signal,
      signal = demand ? AbortSignal.any([session, demand]) : session;
    if (!cursor || pageLoading) return;
    pageLoading = true;
    pageError = undefined;
    let result: PageResult | undefined;
    try {
      result = await stream.page(cursor, signal);
      if (signal.aborted) return;
      metadata = [...metadata, ...((result.files ?? []) as ReviewedFile[])];
      windowRows = Math.max(windowRows, metadata.length);
      next = result.next;
    } catch (error) {
      if (!signal.aborted) pageError = errorText(error);
    } finally {
      result?.acknowledge?.();
      if (!session.aborted) pageLoading = false;
    }
  }
  async function loadFile(file: ReviewedFile) {
    if (!file.content || pending[file.key]) return;
    const signal = request.signal;
    pending = { ...pending, [file.key]: true };
    failures = { ...failures, [file.key]: "" };
    toggled[file.key] = true;
    try {
      const result = await stream.readContent(file.content, signal);
      if (signal.aborted) return;
      if (!result.review) throw new Error("The composed file is unavailable.");
      const anchor = captureAnchor();
      complete = {
        ...complete,
        [file.key]: { reference: file.content, file: result.review as ReviewedFile },
      };
      await tick();
      if (!signal.aborted) restoreAnchor(anchor);
    } catch (error) {
      if (!signal.aborted) failures = { ...failures, [file.key]: errorText(error) };
    } finally {
      if (!signal.aborted) pending = { ...pending, [file.key]: false };
    }
  }
  const loading = $derived(
    !review || review.coverage === "CONNECTING" || (!acquiredPage && refreshing),
  );
  // Coverage reads as its status; an empty one is complete.
  const coverage = $derived(loading ? "" : (review?.coverage ?? ""));
  function hydrated(file: ReviewedFile): boolean {
    const reference = complete[file.key]?.reference;
    return (
      !!reference &&
      reference.attachment === file.content?.attachment &&
      reference.revision === file.content.revision
    );
  }
  const files = $derived(
    metadata.map((file) => {
      const cached = complete[file.key];
      if (!cached) return file;
      return hydrated(file)
        ? { ...cached.file, content: file.content }
        : { ...file, chunks: cached.file.chunks, origins: cached.file.origins };
    }),
  );

  const name = (key: string) =>
    key === "" ? "All agents" : key === "?" ? "Unknown agent" : stream.label(key);
  // The lines each agent edited, as the roster counts them; the session's
  // net outcome for all of them.
  const total = (key: string) =>
    key ? review?.counts.Callers?.[key] : (review?.counts.Net ?? undefined);
  // Roster order, then any agent with changes the roster does not list.
  const choices = $derived.by((): Choice[] => {
    const keys = stream.roster.agents.map((a) => a.name);
    for (const c of review?.callers ?? []) if (!keys.includes(c.caller)) keys.push(c.caller);
    if (!keys.includes(owner)) keys.unshift(owner);
    return ["", ...keys].map((key) => ({ value: key, label: name(key), detail: summary(key) }));
  });
  function summary(key: string): string {
    const n =
      (key ? review?.callers?.find((value) => value.caller === key)?.count : review?.fileCount) ??
      0;
    if (!n) return "No saved changes";
    const c = total(key);
    return [known(c) && `+${c!.Added} −${c!.Removed}`, plural(n, "file")]
      .filter(Boolean)
      .join(" · ");
  }

  // Explicit choices outlive updates; only two files start open.
  let toggled = $state<Record<string, boolean>>({});
  const defaults = $derived(new Set(metadata.slice(0, 2).map((file) => file.key)));
  const open = (file: ReviewedFile) => toggled[file.key] ?? defaults.has(file.key);
  $effect(() => {
    if (
      refreshing ||
      acquiredPage?.attachment !== review?.page?.attachment ||
      acquiredPage?.revision !== review?.page?.revision
    )
      return;
    const visible = metadata.filter((file) => {
      return (
        open(file) && file.content && !pending[file.key] && !failures[file.key] && !hydrated(file)
      );
    });
    untrack(() => {
      for (const file of visible) void loadFile(file);
    });
  });
  // The tool that applied a patch is the editing path, not news.
  const sources = (file: ReviewedFile) => [
    ...new Set((file.origins ?? []).map((o) => o.Source).filter((s) => s && s !== "apply_patch")),
  ];
  // One agent's files need not name it again.
  const callers = (file: ReviewedFile) =>
    caller ? [] : [...new Set((file.origins ?? []).map((o) => o.Caller))];
  // Agents and programs behind every file shown are named once above them;
  // each file names only those that differ.
  const common = (list: (file: ReviewedFile) => string[]) =>
    files.length
      ? files.map(list).reduce((shared, keys) => shared.filter((key) => keys.includes(key)))
      : [];
  const sharedCallers = $derived(common(callers));
  const sharedSources = $derived(common(sources));
  const own = (keys: string[], shared: string[]) => keys.filter((key) => !shared.includes(key));
  const known = (c?: Counts | null) => !!c && c.Added >= 0 && c.Removed >= 0;
  const plural = (n: number, word: string) => `${n} ${n === 1 ? word : `${word}s`}`;
</script>

{#snippet counts(c: Counts, title?: string)}
  <span class="font-mono text-small whitespace-nowrap tabular-nums" {title}
    ><span class={c.Added ? "text-positive" : "text-muted"}>+{c.Added}</span>
    <span class={c.Removed ? "text-negative" : "text-muted"}>−{c.Removed}</span></span
  >
{/snippet}

<!-- One shrinkable column, so wide diffs scroll inside their own frame. -->
<div class="grid grid-cols-[minmax(0,1fr)]" bind:this={container}>
  <section aria-labelledby="saved-heading">
    <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
      <h2 id="saved-heading" class="font-semibold">Saved</h2>
      <Picker
        class="max-w-56"
        label="Agent"
        title="Whose changes to show"
        value={caller}
        groups={[{ choices }]}
        onchange={(next) => (caller = next)}
      />
      {#if !loading && files.length}<span class="text-small text-muted"
          >{plural(files.length, "file")}</span
        >
        {#if known(total(caller))}{@render counts(
            total(caller)!,
            caller ? "Lines this agent changed" : "Net lines changed in the workspace",
          )}{/if}{/if}
      {#if coverage}<span class="text-small text-muted">{coverage}</span>{/if}
    </div>
    {#if !loading && (sharedCallers.length || sharedSources.length)}
      <p class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-small">
        <span class="text-muted">{files.length === 1 ? "Changed" : "Every file changed"}</span>
        {#if sharedCallers.length}<span class="text-muted">by</span>
          {#each sharedCallers as name (name)}<AgentName {name} current={owner} />{/each}{/if}
        {#if sharedSources.length}<span class="text-muted">via</span>
          {#each sharedSources as source (source)}<Tag mono>{source}</Tag>{/each}{/if}
      </p>
    {/if}
    {#if review?.failure}<Alert class="mt-2">{review.failure}</Alert>{/if}
    {#if loading}
      <div class="mt-3 grid gap-3" aria-busy="true" aria-label="Loading saved changes">
        {#each [70, 52, 84] as width, i (i)}<div class="{skeleton} h-[18px]" style:width="{width}%">
            &nbsp;
          </div>{/each}
      </div>
    {:else if !files.length}
      <p class="{emptyBox} mt-2.5">
        {caller
          ? `${name(caller)} has no saved changes in this session yet.`
          : "No saved changes in this session yet."}
      </p>
    {:else}
      <ul class="mt-1.5">
        {#each files as file (file.key)}
          {@const isOpen = open(file)}
          {@const fileCallers = own(callers(file), sharedCallers)}
          {@const fileSources = own(sources(file), sharedSources)}
          <li class="py-1" data-file={file.key}>
            <details
              class="group/disclosure"
              open={isOpen}
              ontoggle={(e) => {
                const now = (e.currentTarget as HTMLDetailsElement).open;
                if (now !== open(file)) toggled[file.key] = now;
              }}
            >
              <summary
                class="flex min-h-9 cursor-pointer items-center gap-2 rounded-control px-2 py-1 hover:bg-active {caret}"
              >
                <span class="min-w-0 flex-1 font-mono text-code wrap-break-word"
                  ><Path path={workspacePath(file.path, review!.workspace)} /></span
                >
                {#each fileCallers as name (name)}<span class="flex-none text-small"
                    ><AgentName {name} current={owner} /></span
                  >{/each}
                {#each fileSources as source (source)}<Tag mono class="flex-none">{source}</Tag
                  >{/each}
                {#if file.chunks?.some((c) => c.Highlighted)}<span
                    class="flex-none text-small text-accent">new</span
                  >{/if}
                {#if known(file.counts)}{@render counts(file.counts)}{/if}
              </summary>
              {#if isOpen}
                <div class="pb-2" aria-busy={!!pending[file.key]}>
                  {#if failures[file.key]}<Alert>{failures[file.key]}</Alert><Button
                      variant="quiet"
                      onclick={() => loadFile(file)}>Retry file</Button
                    >
                  {:else if file.content && !complete[file.key]}<p class="text-small text-muted">
                      Loading composed file…
                    </p>{/if}
                  {#each file.chunks ?? [] as chunk, i (chunk.Key || i)}
                    <DiffView
                      file={chunk.Review}
                      workspace={review!.workspace}
                      status={chunk.Status}
                      recent={chunk.Highlighted}
                    />
                  {/each}
                </div>
              {/if}
            </details>
          </li>
        {/each}
      </ul>
    {/if}
    {#if pageError}<Alert
        >{pageError}<Button variant="quiet" onclick={refreshPages}>Retry</Button></Alert
      >{/if}
    {#if next}<AutoPage
        cursor={JSON.stringify(next)}
        ready={!refreshing && !pageLoading && !pageError}
        onpage={loadPage}
      />{/if}
  </section>
</div>
