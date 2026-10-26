<script lang="ts">
  import AgentName from "./AgentName.svelte";
  import { workspacePath } from "./diff";
  import DiffView from "./DiffView.svelte";
  import Path from "./Path.svelte";
  import type { Counts, ReviewedFile, SavedReview } from "./protocol";
  import { stream } from "./stream.svelte";
  import Alert from "./ui/Alert.svelte";
  import Picker, { type Choice } from "./ui/Picker.svelte";
  import { caret, emptyBox, skeleton } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";

  // The saved changes of one agent, or of the whole session, one section per
  // file with its net diff. It follows owner, the agent the transcript
  // shows, until the reader picks another.
  let { review, owner }: { review: SavedReview | null; owner: string } = $props();

  // "" is every agent; "?" is changes recorded without an agent.
  let caller = $derived(owner);
  const loading = $derived(!review || review.coverage === "CONNECTING");
  // Coverage reads as its status; an empty one is complete.
  const coverage = $derived(loading ? "" : (review?.coverage ?? ""));
  const scoped = (key: string) =>
    key ? (review?.callers?.find((c) => c.caller === key)?.files ?? []) : (review?.files ?? []);
  const files = $derived(scoped(caller));

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
    const n = scoped(key).length;
    if (!n) return "No saved changes";
    const c = total(key);
    return [known(c) && `+${c!.Added} −${c!.Removed}`, plural(n, "file")]
      .filter(Boolean)
      .join(" · ");
  }

  // Bound initial painting across the review, not separately for every file.
  // All file controls remain available; explicit choices outlive updates.
  let toggled = $state<Record<string, boolean>>({});
  const defaults = $derived.by(() => {
    const opened = new Set<string>();
    let lines = 300;
    let characters = 64 * 1024;
    for (const file of files) {
      let size = 0;
      let count = 0;
      for (const chunk of file.chunks) {
        const text = chunk.Review.Diff;
        size += text.length;
        if (size > characters) break;
        for (let at = text.indexOf("\n"); at !== -1; at = text.indexOf("\n", at + 1)) {
          if (++count > lines) break;
        }
        if (count > lines) break;
      }
      if (size > characters || count > lines) continue;
      opened.add(file.key);
      characters -= size;
      lines -= count;
    }
    return opened;
  });
  const open = (file: ReviewedFile) => toggled[file.key] ?? defaults.has(file.key);
  // The tool that applied a patch is the editing path, not news.
  const sources = (file: ReviewedFile) => [
    ...new Set(file.origins.map((o) => o.Source).filter((s) => s && s !== "apply_patch")),
  ];
  // One agent's files need not name it again.
  const callers = (file: ReviewedFile) =>
    caller ? [] : [...new Set(file.origins.map((o) => o.Caller))];
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
<div class="grid grid-cols-[minmax(0,1fr)]">
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
          <li class="border-rule py-1 not-first:border-t">
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
                {#if file.chunks.some((c) => c.Highlighted)}<span
                    class="flex-none text-small text-accent">new</span
                  >{/if}
                {#if known(file.counts)}{@render counts(file.counts)}{/if}
              </summary>
              {#if isOpen}
                <div class="pb-2">
                  {#each file.chunks as chunk, i (chunk.Key || i)}
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
  </section>
</div>
