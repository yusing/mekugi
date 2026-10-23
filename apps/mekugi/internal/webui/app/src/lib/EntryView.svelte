<script lang="ts">
  import AgentName from "./AgentName.svelte";
  import UserImage from "./UserImage.svelte";
  import Excerpt from "./Excerpt.svelte";
  import { duration } from "./format";
  import { outputLanguage } from "./highlight";
  import { failed, isUser, reasoningBody } from "./kinds";
  import Markdown from "./Markdown.svelte";
  import { inline } from "./markdown";
  import type { Entry, Value } from "./protocol";
  import Icon from "./ui/Icon.svelte";
  import { caret } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";
  import ValueRow from "./ValueRow.svelte";

  // at is the time stamp stamp names, empty when it repeats the previous
  // row's. owner is the agent whose activity the transcript shows.
  let {
    entry,
    at,
    stamp,
    turn,
    owner,
  }: { entry: Entry; at: string; stamp: string; turn?: number; owner: string } = $props();

  const user = $derived(isUser(entry));
  // A compound command's result row has nothing to show until it settles.
  const pending = (value: Value) =>
    value.batchExit &&
    !value.code &&
    !value.tail?.length &&
    value.exitCode === undefined &&
    !value.duration &&
    !value.running;
  const first = $derived(entry.values?.[0]);
  const hookFailed = $derived(first !== undefined && failed(first));

  // Traffic between agents: a task, a message, or a child's answer. Like the
  // terminal, it is headed by who sent it and what happened.
  const traffic = $derived.by(() => {
    if (user) return undefined;
    const kind = first?.kind;
    if (entry.kind === "start" || kind === "start")
      return {
        glyph: "start",
        from: first?.from,
        to: first?.to ?? entry.assignmentTo,
        detail: "started",
      };
    if (kind === "message" || entry.kind === "reply" || entry.assignment)
      return {
        glyph: "message",
        from: first?.from,
        to: first?.to ?? entry.assignmentTo,
        detail: "",
      };
    // A child's answer, as Main's transcript shows it.
    const child = entry.agent !== owner && entry.agent !== "Main";
    if (child && entry.kind === "final")
      return { glyph: "final", from: entry.agent, detail: "finished" };
    if (child && entry.kind === "error")
      return { glyph: "error", from: entry.agent, detail: "failed" };
    return undefined;
  });
  const body = $derived(first?.body || entry.assignmentText || entry.text || "");
  // An agent's answer or message to Main. As in the terminal, Main's
  // transcript shows the agent's own summary of it, or its first lines.
  const reply = $derived(
    owner === "/root" &&
      (traffic?.glyph === "final" || traffic?.glyph === "message") &&
      !entry.assignment &&
      entry.agent !== "Main" &&
      entry.agent !== "/root",
  );
  const thought = (ms?: number) => (ms && ms >= 1000 ? `Thought for ${duration(ms)}` : "Thought");
  const prose = $derived(
    entry.kind === "text" || entry.kind === "final" || entry.kind === "message",
  );
  const edges: Record<string, string> = {
    final: "border-l-positive/55",
    error: "border-l-negative",
    start: "border-l-border",
    message: "border-l-border",
  };
  const glyphs: Record<string, string> = {
    final: "text-positive",
    error: "text-negative",
    start: "text-muted",
    message: "text-muted",
  };
  const summary = "flex items-center gap-2 pointer-coarse:min-h-9";
</script>

<!-- Agent output reads down the left beside its offsets; your turns sit on the right,
     ending where agent output ends. -->
<article
  class={[
    "max-w-[920px]",
    user
      ? "mt-5 flex justify-end pb-3"
      : [
          "grid grid-cols-[60px_minmax(0,1fr)] gap-x-4 max-sm:grid-cols-1",
          prose ? "py-2.5" : "py-1",
        ],
  ]}
  data-kind={entry.kind}
>
  {#if user}
    <div
      class="min-w-0 max-w-[min(680px,85%)] rounded-[14px_14px_4px_14px] bg-accent-tint px-4 pt-2.5 pb-3 max-sm:max-w-[90%]"
    >
      <div class="mb-1 flex items-baseline gap-2.5 text-small font-semibold text-accent">
        <span>You</span>{#if turn}<span class="font-normal text-muted tabular-nums"
            >Turn {turn}</span
          >{/if}
        {#if at}<time
            class="ml-auto pl-3 font-mono font-normal text-muted tabular-nums"
            title={stamp}>{at}</time
          >{/if}
      </div>
      {#if entry.images?.length}<div class="mb-3 flex flex-wrap gap-2">
          {#each entry.images as image, index (`${image}:${index}`)}<UserImage
              {entry}
              {image}
              {index}
            />{/each}
        </div>{/if}
      <Markdown text={entry.text ?? ""} spans={entry.spans} />
    </div>
  {:else}
    <time
      class="pt-px text-right font-mono text-small leading-[22px] text-muted tabular-nums max-sm:hidden"
      title={stamp}>{at}</time
    >
    <div class="min-w-0">
      {#if traffic}
        <div class="border-l-2 py-0.5 pl-3.5 {edges[traffic.glyph]}">
          <div class="mb-1 flex flex-wrap items-baseline gap-x-1.5 text-small">
            <span class="w-3.5 {glyphs[traffic.glyph]}" aria-hidden="true"
              >{{ start: "▶", message: "✉", final: "✓", error: "✗" }[traffic.glyph]}</span
            >
            <AgentName name={traffic.from} current={owner} />
            {#if traffic.to}<span class="text-muted">→</span>
              <AgentName name={traffic.to} current={owner} />{/if}
            {#if traffic.detail}<span class="text-muted">{traffic.detail}</span>{/if}
          </div>
          {#if body}
            <!-- A task is long and its agent's own view leads with it. -->
            {#if traffic.glyph === "start" && traffic.to !== owner}
              <details class="group/disclosure">
                <summary class="{summary} {caret} min-h-6 text-small text-muted">Task</summary>
                <Markdown text={body} />
              </details>
            {:else if reply && first?.summary}
              <Markdown text={first.summary} />
              <details class="group/disclosure">
                <summary class="{summary} {caret} min-h-6 text-small text-muted">Full reply</summary
                >
                <Markdown text={body} />
              </details>
            {:else if reply}
              <Excerpt text={body} />
            {:else}
              <Markdown text={body} />
            {/if}
          {/if}
        </div>
      {:else if prose}
        <Markdown text={entry.text ?? ""} />
      {:else if entry.kind === "reasoning"}
        {#each entry.values ?? [] as value, i (i)}
          {@const content = reasoningBody(value.body, value.label)}
          {#snippet thoughtHeading()}
            <Icon name="thought" size={14} />
            <span>{value.live ? "Thinking" : thought(value.thought)}</span>
            {#if value.label}<span class="min-w-0 truncate text-ink">{value.label}</span>{/if}
          {/snippet}
          {#if content}
            <details>
              <summary class="{summary} min-h-[26px] text-muted hover:text-ink"
                >{@render thoughtHeading()}</summary
              >
              <div class="mt-1 mb-2 ml-1.5 border-l-2 border-rule pl-[15px]">
                <Markdown text={content} quiet />
              </div>
            </details>
          {:else}
            <div class="{summary} min-h-[26px] text-muted">{@render thoughtHeading()}</div>
          {/if}
        {/each}
      {:else if entry.kind === "hook"}
        <div class="flex min-h-[26px] items-center gap-2 text-small text-muted">
          <span class={["flex flex-none", hookFailed && "text-negative"]}
            ><Icon name="hook" size={13} /></span
          >
          <span class="min-w-0 truncate" title={first?.label ?? entry.text}
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
      {:else if entry.values?.length}
        <div
          class={[
            "grid grid-cols-[max-content_minmax(0,1fr)_auto] gap-x-3 divide-y divide-rule rounded-card border px-3.5 py-1 max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:px-3",
            entry.running && "border-[color-mix(in_srgb,var(--accent)_45%,var(--border))]",
          ]}
        >
          {#each entry.values as value, i (i)}
            {#if value.kind === "progress"}
              <p class="col-span-full py-[5px] text-muted">{value.body}</p>
            {:else if !pending(value)}
              <ValueRow {value} outputLang={outputLanguage(entry.values, i)} />
            {/if}
          {/each}
        </div>
      {:else if entry.text}
        <Markdown text={entry.text} />
      {/if}
    </div>
  {/if}
</article>
