<script lang="ts">
  import AgentName from "./AgentName.svelte";
  import UserImage from "./UserImage.svelte";
  import Excerpt from "./Excerpt.svelte";
  import { basename, duration, progressText } from "./format";
  import { outputLanguage } from "./highlight";
  import { failed, isUser, reasoningBody } from "./kinds";
  import Markdown from "./Markdown.svelte";
  import { inline, referenceContext } from "./markdown";
  import { observeOverflow } from "./overflow";
  import type { Entry, Value } from "./protocol";
  import { stream, type Stream } from "./stream.svelte";
  import Icon from "./ui/Icon.svelte";
  import Reasoning from "./ui/ai/Reasoning.svelte";
  import * as Message from "./ui/message";
  import * as Trace from "./ui/trace";
  import * as Card from "./ui/card";
  import { caret } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";
  import ValueRow from "./ValueRow.svelte";
  import Wait from "./Wait.svelte";

  // at is the time stamp stamp names, empty when it repeats the previous
  // row's. owner is the agent whose activity the transcript shows. views are
  // the images an agent viewed in a row, starting with entry; thoughts are its
  // consecutive reasoning entries, likewise.
  let {
    entry,
    at,
    stamp,
    turn,
    owner,
    views,
    thoughts,
    source = stream,
    onload,
    onclip,
  }: {
    entry: Entry;
    at: string;
    stamp: string;
    turn?: number;
    owner: string;
    views?: Entry[];
    thoughts?: Entry[];
    source?: Stream;
    onload?: () => unknown;
    onclip?: (clipped?: boolean) => void;
  } = $props();

  const user = $derived(isUser(entry));
  // A compound command's result row has nothing to show until it settles.
  const pending = (value: Value) =>
    value.batchExit &&
    !value.code &&
    !value.codeLines &&
    !value.tailLines &&
    !value.tail?.length &&
    value.exitCode === undefined &&
    !value.duration &&
    !value.running;
  const first = $derived(entry.values?.[0]);
  // A run of file edits, all a command's effects or all captured on their
  // own, names the programs behind all of them once, above the run; each edit
  // keeps only the programs the others did not share.
  const sourced = (value: Value) =>
    value.kind === "op" &&
    ["Create", "Edit", "Delete", "Move"].includes(value.verb ?? "") &&
    !!value.sources?.length;
  const edits = $derived.by(() => {
    const all = entry.values ?? [];
    const values = [...all];
    const via = new Map<number, { sources: string[]; files: number; effect?: boolean }>();
    for (let i = 0, end = 1; i < all.length; i = end++) {
      while (
        end < all.length &&
        sourced(all[i]) &&
        sourced(all[end]) &&
        !all[end].effect === !all[i].effect
      )
        end++;
      if (end - i < 2) continue;
      const shared = all
        .slice(i, end)
        .map((value) => value.sources!)
        .reduce((common, sources) => common.filter((program) => sources.includes(program)));
      if (!shared.length) continue;
      via.set(i, {
        sources: shared,
        files: all
          .slice(i, end)
          .reduce((n, v) => n + (entry.content && v.hiddenEdits ? v.hiddenEdits : 1), 0),
        effect: all[i].effect,
      });
      for (let j = i; j < end; j++)
        values[j] = {
          ...all[j],
          sources: all[j].sources!.filter((program) => !shared.includes(program)),
        };
    }
    return { values, via };
  });
  const values = $derived(edits.values);
  let openFiles = $state<Record<number, boolean>>({});
  const rows = $derived.by(() => {
    const out: { value: Value; index: number; hidden?: Value[] }[] = [];
    for (let i = 0; i < values.length;) {
      const value = values[i];
      const count = value.hiddenEdits ?? 0;
      out.push({ value, index: i, hidden: count ? values.slice(i, i + count) : undefined });
      i += entry.content ? 1 : Math.max(1, count);
    }
    return out;
  });
  $effect(() => {
    if (entry.content && rows.some(({ value, index }) => value.hiddenEdits && openFiles[index]))
      onload?.();
  });
  const hookFailed = $derived(first !== undefined && failed(first));
  const measureHook = (node: HTMLElement) => {
    const notify = onclip;
    if (!notify) return;
    void first?.label;
    const stop = observeOverflow(node, notify);
    return () => {
      stop();
      notify();
    };
  };
  // Main is local to an independently hosted runtime; canonical paths are global.
  const endpoint = (name?: string) => (name === "Main" ? source.session?.caller || "/root" : name);

  // Traffic between agents: a task, a message, or a child's answer. Like the
  // terminal, it is headed by who sent it and what happened.
  const traffic = $derived.by(() => {
    if (user) return undefined;
    const kind = first?.kind;
    if (entry.kind === "start" || kind === "start")
      return {
        glyph: "start",
        from: endpoint(first?.from),
        to: endpoint(first?.to ?? entry.assignmentTo),
        detail: "started",
      };
    if (kind === "message" || entry.kind === "reply" || entry.assignment)
      return {
        glyph: "message",
        from: endpoint(first?.from),
        to: endpoint(first?.to ?? entry.assignmentTo),
        detail: "",
      };
    // A child's answer, as Main's transcript shows it.
    const child = entry.agent !== owner && entry.agent !== "Main";
    if (child && entry.kind === "final")
      return {
        glyph:
          entry.outcome === "interrupted"
            ? "interrupted"
            : entry.outcome === "failed"
              ? "error"
              : "final",
        from: entry.agent,
        detail:
          entry.outcome === "interrupted"
            ? "interrupted"
            : entry.outcome === "failed"
              ? "failed"
              : "finished",
      };
    if (child && entry.kind === "error")
      return { glyph: "error", from: entry.agent, detail: "failed" };
    return undefined;
  });
  const body = $derived(first?.body || entry.assignmentText || entry.text || "");
  const replyContext = $derived(first?.summary ? referenceContext(body) : undefined);
  // Incoming replies use their summary or excerpt in the recipient's feed;
  // the displayed agent's own replies remain complete.
  const reply = $derived(
    (entry.kind === "final" || traffic?.glyph === "message") &&
      !entry.assignment &&
      entry.agent !== "Main" &&
      entry.agent !== owner,
  );
  const thought = (ms?: number) => (ms && ms >= 1000 ? `Thought for ${duration(ms)}` : "Thought");
  // Consecutive thoughts fold into one line headed by the latest.
  const reasoning = $derived((thoughts ?? [entry]).flatMap((e) => e.values ?? []));
  const latestThought = $derived(reasoning.at(-1));
  const thinking = $derived.by(() => {
    const ms = reasoning.reduce((sum, value) => sum + (value.thought ?? 0), 0);
    return `${reasoning.length} thoughts${ms >= 1000 ? ` for ${duration(ms)}` : ""}`;
  });
  const prose = $derived(
    entry.kind === "text" || entry.kind === "final" || entry.kind === "message",
  );
  const edges: Record<string, string> = {
    final: "border-l-positive/55",
    interrupted: "border-l-border",
    error: "border-l-negative",
    start: "border-l-border",
    message: "border-l-border",
  };
  const glyphs: Record<string, string> = {
    final: "text-positive",
    interrupted: "text-muted",
    error: "text-negative",
    start: "text-muted",
    message: "text-muted",
  };
  const summary = "flex items-center gap-2 pointer-coarse:min-h-9";
  const waits = $derived(
    !!entry.values?.length &&
      entry.values.every((value) => value.kind === "progress" && value.waits?.length),
  );
</script>

{#snippet thoughtRow(value: Value)}
  {@const content = reasoningBody(value.body, value.label)}
  {#if content}
    <Reasoning
      heading={value.live ? "Thinking" : thought(value.thought)}
      label={value.label}
      running={value.live}
    >
      <Markdown text={content} quiet />
    </Reasoning>
  {:else}
    <Reasoning
      heading={value.live ? "Thinking" : thought(value.thought)}
      label={value.label}
      running={value.live}
    />
  {/if}
{/snippet}
{#snippet wait(value: Value)}
  <Wait
    text={value.label || value.body || ""}
    targets={value.waits ?? []}
    timeout={value.waitTimeout}
    reason={value.waitReason}
    {owner}
  />
{/snippet}
<!-- The programs behind a run of edits, in the edits' own column. -->
{#snippet via(index: number)}
  {@const group = edits.via.get(index)!}
  <div
    class={[
      "col-span-full grid grid-cols-subgrid items-baseline py-[5px]",
      group.effect && "ml-3 border-l border-rule pl-3",
    ]}
  >
    <span class="text-small font-medium text-muted max-sm:col-span-full">Via</span>
    <span class="col-[2/-1] flex flex-wrap items-baseline gap-1.5 max-sm:col-span-full">
      {#each group.sources as source (source)}<Tag mono>{source}</Tag>{/each}
      <span class="ml-1 text-small text-muted">for all {group.files} files</span>
    </span>
  </div>
{/snippet}

<!-- Agent output reads down the left beside its offsets; your turns sit on the right,
     ending where agent output ends. -->
<article
  class={[
    "max-w-[920px]",
    user
      ? "mt-5 flex justify-end pb-3"
      : [
          "group/entry grid grid-cols-[60px_minmax(0,1fr)] gap-x-4 max-sm:grid-cols-1",
          prose ? "py-2.5" : "py-1",
        ],
  ]}
  data-kind={entry.kind}
>
  {#if user}
    <Message.Root align="end">
      <Message.Content
        class="w-auto max-w-[min(680px,85%)] gap-0 rounded-[14px_14px_4px_14px] bg-accent-tint px-4 pt-2.5 pb-3 max-sm:max-w-[90%]"
      >
        <Message.Header
          class="mb-1 items-baseline gap-2.5 px-0 text-small font-semibold text-accent"
        >
          <span>You</span>{#if turn}<span class="font-normal text-muted tabular-nums"
              >Turn {turn}</span
            >{/if}
          {#if at}<time
              class="ml-auto pl-3 font-mono font-normal text-muted tabular-nums"
              title={stamp}>{at}</time
            >{/if}
        </Message.Header>
        {#if entry.images?.length}<div class="mb-3 flex flex-wrap gap-2">
            {#each entry.images as image, index (`${image}:${index}`)}<UserImage
                {entry}
                {image}
                {index}
                {source}
              />{/each}
          </div>{/if}
        <Markdown text={entry.text ?? ""} spans={entry.spans} />
      </Message.Content>
    </Message.Root>
  {:else}
    <time
      class="pt-px text-right font-mono text-small leading-[22px] text-muted tabular-nums max-sm:hidden"
      title={stamp}>{at}</time
    >
    <div class="min-w-0">
      {#if traffic}
        <Message.Root
          data-ai-element="subagent"
          class="border-l-2 py-0.5 pl-3.5 {edges[traffic.glyph]}"
        >
          <Message.Content class="gap-0">
            <Message.Header class="mb-1 px-0 font-normal">
              <Trace.Line
                status={traffic.glyph === "error" ? "error" : undefined}
                class="flex-wrap items-baseline gap-x-1.5 text-small"
              >
                <Trace.Icon class="size-3.5 {glyphs[traffic.glyph]}"
                  >{{ start: "▶", message: "✉", final: "✓", error: "✗", interrupted: "■" }[
                    traffic.glyph
                  ]}</Trace.Icon
                >
                <AgentName name={traffic.from} current={owner} />
                {#if traffic.to}<span class="text-muted">→</span>
                  <AgentName name={traffic.to} current={owner} />{/if}
                {#if traffic.detail}<Trace.Description class="text-muted"
                    >{traffic.detail}</Trace.Description
                  >{/if}
              </Trace.Line>
            </Message.Header>
            {#if body}
              <!-- A task is long and its agent's own view leads with it; elsewhere
                 it opens with its first lines. -->
              {#if traffic.glyph === "start" && traffic.to !== owner}
                <Excerpt text={body} noun="task" />
              {:else if reply && first?.summary}
                <Markdown text={first.summary} context={replyContext} />
                <details class="group/disclosure">
                  <summary class="{summary} {caret} min-h-6 text-small text-muted">Details</summary>
                  <Markdown text={first.details ?? body} context={replyContext} />
                </details>
              {:else if reply}
                <Excerpt text={body} />
              {:else}
                <Markdown text={body} />
              {/if}
            {/if}
          </Message.Content>
        </Message.Root>
      {:else if prose}
        <Message.Root>
          <Message.Content><Markdown text={entry.text ?? ""} /></Message.Content>
        </Message.Root>
      {:else if entry.kind === "reasoning" && reasoning.length > 1}
        <Reasoning
          heading={latestThought?.live ? "Thinking" : thinking}
          label={latestThought?.label}
          running={latestThought?.live}
        >
          {#each reasoning as value, i (i)}{@render thoughtRow(value)}{/each}
        </Reasoning>
      {:else if entry.kind === "reasoning"}
        {#each reasoning as value, i (i)}{@render thoughtRow(value)}{/each}
      {:else if entry.kind === "hook"}
        <div class="flex min-h-[26px] items-center gap-2 text-small text-muted">
          <span class={["flex flex-none", hookFailed && "text-negative"]}
            ><Icon name="hook" size={13} /></span
          >
          <span class="min-w-0 truncate" title={first?.label ?? entry.text} {@attach measureHook}
            >{@html inline(first?.label ?? entry.text ?? "")}</span
          >
          {#if hookFailed}<Tag tone="negative">{first?.hook?.status ?? "failed"}</Tag>{/if}
          {#if first?.duration}<span class="font-mono tabular-nums">{duration(first.duration)}</span
            >{/if}
        </div>
        <!-- A failed hook says why. -->
        {#if hookFailed && first?.tail?.length}
          <pre
            class="mt-0.5 mb-1.5 ml-[21px] rounded-card bg-negative-tint px-2.5 py-1.5 text-small leading-[18px] whitespace-pre-wrap wrap-break-word text-negative-ink">{first.tail.join(
              "\n",
            )}</pre>
        {/if}
      {:else if views}
        {@const paths = views.map((view) => view.values?.[0]?.reads?.[0]?.path ?? "")}
        <Card.Root size="sm">
          <Card.Content
            class="grid grid-cols-[max-content_minmax(0,1fr)_auto] gap-x-3 max-sm:grid-cols-[minmax(0,1fr)_auto]"
          >
            {#if views.length === 1}
              <ValueRow value={entry.values![0]} />
            {:else}
              <div class="col-span-full grid grid-cols-subgrid items-baseline py-[5px]">
                <span class="text-small font-medium text-kind-read max-sm:col-span-full"
                  >{first?.verb}</span
                >
                <span class="text-muted">{views.length} images</span>
              </div>
            {/if}
            <div class="col-[2/-1] flex min-w-0 flex-wrap gap-3 pt-1 pb-2.5 max-sm:col-span-full">
              {#each views as view, i (view.seq)}
                {#each view.images ?? [] as image, index (`${image}:${index}`)}
                  <div class="min-w-0">
                    <UserImage
                      entry={view}
                      {image}
                      {index}
                      {source}
                      label="{view.values?.[0]?.verb === 'Generated'
                        ? 'Generated'
                        : 'Viewed'} image {paths[i]}"
                      small={views.length > 1}
                    />
                    {#if views.length > 1}<p
                        class="mt-1 w-44 max-w-full truncate font-mono text-tiny text-muted"
                        title={paths[i]}
                      >
                        {basename(paths[i])}
                      </p>{/if}
                  </div>
                {/each}
              {/each}
            </div>
          </Card.Content>
        </Card.Root>
      {:else if waits}
        <!-- A wait is a compact line like a hook's, its marker breathing while
             it lasts. -->
        {#each entry.values ?? [] as value, i (i)}
          <p class="flex min-h-[26px] items-start gap-2 py-[3px] text-small text-muted">
            <span class={["flex h-5 flex-none items-center", value.live && "animate-blink"]}
              ><Icon name="agents" size={13} /></span
            >
            <span class="min-w-0">{@render wait(value)}</span>
          </p>
        {/each}
      {:else if entry.values?.length}
        <Card.Root size="sm" class={entry.running ? "ring-ring/45" : undefined}>
          <Card.Content>
            <Trace.Root
              class="grid grid-cols-[max-content_minmax(0,1fr)_auto] gap-x-3 gap-y-0 divide-y divide-rule max-sm:grid-cols-[minmax(0,1fr)_auto]"
            >
              {#each rows as { value, index, hidden } (index)}
                {#if edits.via.has(index)}{@render via(index)}{/if}
                {#if value.kind === "progress" && value.waits?.length}
                  <p class="col-span-full py-[5px] text-muted">{@render wait(value)}</p>
                {:else if value.kind === "progress"}
                  <p class="col-span-full py-[5px] text-muted">
                    {progressText(value, (name) => source.label(name))}
                  </p>
                {:else if hidden}
                  <details
                    class="group/disclosure col-span-full py-1"
                    ontoggle={(event) => {
                      openFiles[index] = event.currentTarget.open;
                    }}
                  >
                    <summary
                      class="{summary} {caret} min-h-8 text-small text-muted pointer-coarse:min-h-11"
                    >
                      {value.hiddenEdits} more {value.hiddenEdits === 1 ? "file" : "files"}
                    </summary>
                    <div
                      class="grid grid-cols-[max-content_minmax(0,1fr)_auto] gap-x-3 divide-y divide-rule max-sm:grid-cols-[minmax(0,1fr)_auto]"
                    >
                      {#each entry.content ? [] : hidden as file, i (i)}{#if i && edits.via.has(index + i)}{@render via(
                            index + i,
                          )}{/if}<ValueRow value={file} {onload} />{/each}
                    </div>
                  </details>
                {:else if !pending(value)}
                  <ValueRow {value} {onload} outputLang={outputLanguage(entry.values, index)} />
                {/if}
              {/each}
            </Trace.Root>
          </Card.Content>
        </Card.Root>
      {:else if entry.text}
        <Markdown text={entry.text} />
      {/if}
    </div>
  {/if}
</article>
