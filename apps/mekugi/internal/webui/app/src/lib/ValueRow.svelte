<script lang="ts">
  import Code from "./Code.svelte";
  import { duration } from "./format";
  import { breakable, highlightWithin } from "./highlight";
  import { failed, family } from "./kinds";
  import { toolLabel } from "./markdown";
  import Path from "./Path.svelte";
  import type { Value } from "./protocol";
  import { stream } from "./stream.svelte";
  import Dot from "./ui/Dot.svelte";
  import Icon from "./ui/Icon.svelte";
  import { caret, codeBlock } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";

  // outputLang names the grammar of the retained output, when known.
  let { value, outputLang }: { value: Value; outputLang?: string } = $props();

  const lines = (n: number) => `${n} ${n === 1 ? "line" : "lines"}`;
  const count = (sign: string, n: string) =>
    `<span class="${n === "0" ? "text-muted" : sign === "+" ? "text-positive" : "text-negative"}">${sign}${n}</span>`;

  // A compound command's result under its listed steps: the steps say what
  // ran, so the row holds only the outcome and the combined output.
  const result = $derived(value.batchExit && !value.code);
  const verb = $derived(result ? "" : (value.verb ?? value.kind ?? ""));
  const exited = $derived(value.exitCode !== undefined && value.exitCode !== 0);
  const hookFailed = $derived(!exited && failed(value));
  const changes = $derived((value.changes ?? []).filter((c) => !c.footer));
  const footer = $derived(value.changes?.find((c) => c.footer));
  // Labels join their parts with " · " and state diff counts as "+5 −3".
  // Render segments with space instead of punctuation; diff counts keep their
  // colours, while punctuation within code spans remains authored text.
  const label = $derived(
    parts(value.label ?? "")
      .map((part) =>
        breakable(toolLabel(part, verb))
          .split(/(<code>[\s\S]*?<\/code>)/)
          .map((text, i) =>
            i % 2
              ? text
              : text.replace(
                  /(^|\s)\+(\d+) [−-](\d+)(?=\s|$)/g,
                  (_, lead, added, removed) => `${lead}${count("+", added)} ${count("−", removed)}`,
                ),
          )
          .join(""),
      )
      .map((part, i) => (i === 0 ? part : `<span class="ml-2 inline-block">${part}</span>`))
      .join(" "),
  );

  // Splits Activity's " · " separators, but not inside code spans.
  function parts(text: string): string[] {
    const out: string[] = [];
    // A label with an empty first part, such as a commit without a message,
    // starts with its separator.
    let start = text.startsWith("· ") ? 2 : 0;
    for (let i = start; i < text.length;) {
      if (text[i] === "`") {
        let n = 1;
        while (text[i + n] === "`") n++;
        const end = text.indexOf("`".repeat(n), i + n);
        i = end < 0 ? i + n : end + n;
      } else if (text.startsWith(" · ", i)) {
        out.push(text.slice(start, i));
        i = start = i + 3;
      } else i++;
    }
    out.push(text.slice(start));
    return out.filter(Boolean);
  }

  const hasOutput = $derived(value.tail?.some((line) => line.trim()));
  const code = $derived(value.code?.replace(/\n$/, ""));
  const multiline = $derived(code !== undefined && code.includes("\n"));
  // A command with no label is its own subject: its first line takes the label.
  const headline = $derived(
    !value.label && !value.reads?.length && code ? code.split("\n")[0] : undefined,
  );
  // Tool arguments and results often carry JSON text inside a JSON string;
  // they read as one indented document.
  const json = $derived.by(() => {
    if (value.lang !== "json" || !code) return undefined;
    const decode = (v: unknown): unknown => {
      if (typeof v === "string" && /^\s*[[{]/.test(v)) {
        try {
          return decode(JSON.parse(v));
        } catch {
          return v;
        }
      }
      if (Array.isArray(v)) return v.map(decode);
      if (v && typeof v === "object")
        return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, decode(x)]));
      return v;
    };
    try {
      return JSON.stringify(decode(JSON.parse(code)), null, 2);
    } catch {
      return code;
    }
  });
  const output = $derived.by(() => {
    const shown = value.tail?.length ?? 0;
    return value.tailOmitted
      ? `last ${shown} of ${lines(shown + value.tailOmitted)}`
      : lines(shown);
  });
  // Disclosed text renders, and is highlighted, once it is opened.
  let scriptOpen = $state(false);
  let outputOpen = $state(false);
  // A Run label quotes its command as inline code.
  const commandLabel = (html: string) => (node: HTMLElement) => {
    if (html && verb === "Run") highlightWithin(node, "code", () => "bash", true);
  };
  const verbs: Record<string, string> = {
    run: "text-kind-run",
    read: "text-kind-read",
    write: "text-kind-write",
    fail: "text-negative",
  };
  const changeCodes: Record<string, string> = { A: "text-positive", D: "text-negative" };
  // Details below the label use the card's full width, past the meta column.
  const extra = "col-[2/-1] min-w-0 max-sm:col-span-full";
  const block = `${codeBlock} mt-1 mb-1.5`;
  const scroll = `${block} max-h-80 px-3 py-2.5`;
  const disclosure = `inline-flex min-h-6 items-center gap-1 text-small text-muted hover:text-ink pointer-coarse:min-h-9 ${caret}`;
</script>

<div
  class={[
    "col-span-full grid grid-cols-subgrid items-baseline py-[5px]",
    value.subsumed && "opacity-70",
    value.effect && "ml-3 border-l border-rule pl-3",
  ]}
>
  <span
    class="text-small font-medium whitespace-nowrap max-sm:col-span-full {verbs[
      family(verb) ?? ''
    ] ?? 'text-muted'}">{verb}</span
  >
  <!-- Code in a tool label is the subject itself, not an aside: mono, no chip. -->
  <div class="min-w-0 wrap-break-word [&_code]:text-code" {@attach commandLabel(label)}>
    {#if value.reads?.length}
      {#each value.reads as read, i (i)}
        <span class="block font-mono text-code"
          ><Path path={read.path} />{#if read.ranges?.length}<span
              class="whitespace-nowrap text-muted">:{read.ranges.join(",")}</span
            >{/if}</span
        >
      {/each}
    {:else if value.path}
      <span class="font-mono text-code"><Path path={value.path} /></span>
    {:else if value.label}
      {@html label}
    {:else if headline !== undefined}
      <code
        ><Code code={headline} lang={value.lang} wrap />{#if multiline}<span class="text-muted"
            >{" …"}</span
          >{/if}</code
      >
    {/if}
    {#if value.autoReload}<span class="ml-2 inline-block whitespace-nowrap text-small text-muted"
        >(auto reloaded)</span
      >{/if}
    {#if value.workdir}<span class="text-muted"
        >{` in `}<span class="font-mono"><Path path={value.workdir} /></span></span
      >{/if}
  </div>
  <span
    class="flex items-center justify-end gap-2 font-mono text-small whitespace-nowrap tabular-nums"
  >
    {#if hasOutput}<button
        type="button"
        aria-expanded={outputOpen}
        aria-label={`Output: ${output}`}
        title={`Output: ${output}`}
        class="inline-flex items-center gap-1 font-sans text-small text-muted hover:text-ink max-sm:min-h-9"
        onclick={() => (outputOpen = !outputOpen)}
      >
        <span class={outputOpen ? "rotate-0" : "-rotate-90"}><Icon name="chevron" size={12} /></span
        >
        <span class="max-sm:hidden">Output <span class="text-tiny">{output}</span></span><span
          class="hidden max-sm:inline">{lines(value.tail?.length ?? 0)}</span
        >
      </button>{/if}
    {#if value.running}
      <span class="inline-flex items-center gap-1.5 text-accent"
        ><Dot tone="running" size={6} />running</span
      >
    {:else if value.pending}
      <Tag>pending</Tag>
    {:else if exited}
      <Tag tone="negative">exit {value.exitCode}</Tag>
    {:else if hookFailed}
      <Tag tone="negative">{value.hook?.status ?? "failed"}</Tag>
    {/if}
    {#if value.editOutcome}<Tag>{value.editOutcome}</Tag>{/if}
    {#if value.duration}<span class="text-muted">{duration(value.duration)}</span>{/if}
  </span>

  {#if code && (multiline || (value.fenced && headline === undefined))}
    <div class={extra}>
      {#if json !== undefined}
        <details class="group/disclosure mt-0.5" bind:open={scriptOpen}>
          <summary class={disclosure}
            >Arguments and result <span class="ml-2">{lines(json.split("\n").length)}</span
            ></summary
          >
          {#if scriptOpen}<pre class="{scroll} whitespace-pre-wrap wrap-anywhere"><Code
                code={json}
                lang="json"
              /></pre>{/if}
        </details>
      {:else if multiline}
        <details class="group/disclosure mt-0.5" bind:open={scriptOpen}>
          <summary class={disclosure}
            >Script <span class="ml-2">{lines(code.split("\n").length)}</span></summary
          >
          {#if scriptOpen}<pre class="{scroll} whitespace-pre"><Code
                {code}
                lang={value.lang}
              /></pre>{/if}
        </details>
      {:else}
        <pre class="{block} px-2.5 py-1.5 whitespace-pre"><Code {code} lang={value.lang} /></pre>
      {/if}
    </div>
  {/if}

  {#if changes.length || footer}
    <div class={extra}>
      {#if changes.length}
        <ul class="mt-1 text-code leading-5">
          {#each changes as change, i (i)}
            <li class="grid grid-cols-[14px_minmax(0,1fr)_auto] gap-2">
              <span class="font-mono {changeCodes[change.code ?? ''] ?? 'text-kind-write'}"
                >{change.code ?? change.verb ?? ""}</span
              >
              <span class="truncate font-mono">{change.label}</span>
              {#if change.added || change.removed}
                <span class="font-mono tabular-nums"
                  >{@html `${count("+", String(change.added ?? 0))} ${count("−", String(change.removed ?? 0))}`}</span
                >
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
      {#if footer}
        <div class="mt-0.5 flex flex-wrap gap-x-2 text-small text-muted">
          <span class="font-mono">{footer.label}</span>
          {#if footer.added || footer.removed}
            <span class="font-mono tabular-nums"
              >{@html `${count("+", String(footer.added ?? 0))} ${count("−", String(footer.removed ?? 0))}`}</span
            >
          {/if}
          {#if footer.note}<span>{footer.note}</span>{/if}
        </div>
      {/if}
    </div>
  {/if}

  {#if value.questions?.length || value.waits?.length}
    <div class={extra}>
      {#if value.questions?.length}
        <ul class="mt-1 text-code leading-5">
          {#each value.questions as q, i (i)}
            <li>
              {q.text}{#if q.answer}<span class="ml-2 text-muted">{q.answer}</span>{/if}
            </li>
          {/each}
        </ul>
      {/if}
      {#if value.waits?.length}
        <div class="flex flex-wrap items-center gap-2 text-muted">
          <span>Waiting on</span>
          {#each value.waits as wait}<Tag
              >{stream.label(wait.name)}{#if wait.status}<span class="ml-1">({wait.status})</span
                >{/if}</Tag
            >{/each}
        </div>
      {/if}
    </div>
  {/if}

  {#if hasOutput && outputOpen}
    <div class={extra}>
      <pre class="{scroll} whitespace-pre"><Code
          code={value.tail?.join("\n") ?? ""}
          lang={outputLang}
        /></pre>
    </div>
  {/if}
</div>
