<script lang="ts">
  import { atLeast, cost, date, duration, tokens } from "./format";
  import Path from "./Path.svelte";
  import type { Agent, Playback, Session } from "./protocol";
  import Tag from "./ui/Tag.svelte";

  let {
    session,
    playback,
    root,
    status,
    alert = false,
  }: {
    session: Session;
    playback?: Playback;
    root?: Agent;
    status?: string;
    alert?: boolean;
  } = $props();

  // Facts a replay does not record are left out rather than shown as dashes.
  const rows = $derived(
    (
      [
        ["Started", session.started ? date(session.started) : undefined],
        ["Recorded", playback ? duration(playback.until) : undefined],
        ["Turns", root?.turns !== undefined ? String(root.turns) : undefined],
        ["Round trips", root?.roundtrips !== undefined ? String(root.roundtrips) : undefined],
        [
          "Tokens in / out",
          root?.inputTokens !== undefined
            ? `${atLeast(tokens(root.inputTokens), root.usagePartial)} / ${atLeast(tokens(root.outputTokens ?? 0), root.usagePartial)}`
            : undefined,
        ],
        [
          "Context",
          root?.context !== undefined
            ? `${tokens(root.context)}${root.window ? ` of ${tokens(root.window)}` : ""}`
            : undefined,
        ],
        ["Cost", root?.cost !== undefined ? atLeast(cost(root.cost), root.costPartial) : undefined],
      ] as [string, string | undefined][]
    ).filter(([, value]) => value !== undefined),
  );
  const id = "grid gap-0.5 text-small leading-[18px]";
  const idValue = "font-mono wrap-break-word select-all";
</script>

<div class="px-[18px] pt-4 pb-[18px]">
  <h2 class="mb-3 font-semibold">Session details</h2>
  <div class="grid gap-2.5 border-b border-rule pb-3.5">
    {#if session.workspace}
      <div class={id}>
        <span class="text-muted">Workspace</span><span class={idValue}
          ><Path path={session.workspace} /></span
        >
      </div>
    {/if}
    <div class={id}>
      <span class="text-muted">Thread</span><span class={idValue}>{session.thread}</span>
    </div>
    <!-- A status can be a long failure; it reads as prose, not a value column. -->
    {#if status}
      <div class={id}>
        <span class="text-muted">Status</span><span
          class={["leading-5 wrap-anywhere", alert && "text-negative"]}>{status}</span
        >
      </div>
    {/if}
  </div>
  <dl class="mt-2.5 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 leading-7">
    {#each rows as [label, value] (label)}
      <dt class="text-muted">{label}</dt>
      <dd class="text-right tabular-nums">{value}</dd>
    {/each}
  </dl>
  {#if root?.skills?.length}
    <div class="mt-2.5 border-t border-rule pt-3 text-small">
      <span class="text-muted">Skills</span>
      <ul class="mt-1.5 flex flex-wrap gap-1.5">
        {#each root.skills as skill (skill)}<li>
            <Tag mono>{skill}</Tag>
          </li>{/each}
      </ul>
    </div>
  {/if}
</div>
