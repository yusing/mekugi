<script lang="ts">
  import { doing, since, stateLabel, worked } from "./agents";
  import { cost, duration, tokens } from "./format";
  import type { Agent, Entry, Roster } from "./protocol";
  import Dot, { type Tone } from "./ui/Dot.svelte";
  import { emptyBox, skeleton } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";

  // The agents of the session, as a tree under Main. Selecting one shows its
  // own activity; now is the recorded clock, for timers.
  let {
    roster,
    loading = false,
    selected,
    now,
    activity,
    onselect,
  }: {
    roster: Roster;
    loading?: boolean;
    selected: string;
    now: number;
    activity: (agent: Agent) => Entry | undefined;
    onselect: (name: string) => void;
  } = $props();

  const agents = $derived(roster.agents);
  const lines = (n: number) => `${n} ${n === 1 ? "line" : "lines"}`;
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;
  // How many agents are working, done, or failed, for the heading.
  const tallies = $derived(
    (["working", "done", "error"] as const)
      .map((state) => ({ state, n: agents.filter((a) => a.state === state).length }))
      .filter(({ n }) => n)
      .map(({ state, n }) => ({
        state,
        n,
        text: state === "error" ? plural(n, "error") : `${n} ${state}`,
      })),
  );
  // A tally that covers every agent says the total itself.
  const whole = $derived(tallies.length === 1 && tallies[0].n === agents.length);

  // Session totals; usage that is partly unknown is a lower bound.
  const totals = $derived.by(() => {
    let input = 0,
      output = 0,
      usd = 0,
      tokensKnown = false,
      costKnown = false,
      partial = false;
    for (const a of agents) {
      if (a.inputTokens !== undefined) {
        input += a.inputTokens;
        output += a.outputTokens ?? 0;
        tokensKnown = true;
      }
      if (a.cost !== undefined) {
        usd += a.cost;
        costKnown = true;
      }
      partial ||= !!a.usagePartial || !!a.costPartial;
    }
    const lower = partial ? "≥" : "";
    return {
      input: tokensKnown ? `${lower}${tokens(input)}` : undefined,
      output: tokensKnown ? `${lower}${tokens(output)}` : undefined,
      cost: costKnown ? `${lower}${cost(usd)}` : undefined,
    };
  });

  // Nested agents show their own name under their parent.
  const name = (agent: Agent) =>
    agent.depth ? agent.label.slice(agent.label.lastIndexOf("/") + 1) : agent.label;

  const dots: Record<Agent["state"], Tone> = {
    working: "running",
    done: "done",
    error: "alert",
    idle: "hollow",
  };
  const tallyInk: Record<string, string> = { working: "text-accent", error: "text-negative" };
  const stateInk: Record<string, string> = { working: "text-ink", error: "text-negative" };
  const sub = "pl-4 text-small leading-[18px]";
</script>

{#snippet metric(label: string, value: string)}
  <span class="flex min-w-0 items-baseline justify-between gap-2">
    <span class="text-muted">{label}</span><span class="whitespace-nowrap text-ink tabular-nums"
      >{value}</span
    >
  </span>
{/snippet}

<div class="mt-3.5 mb-0.5 flex items-baseline gap-2.5">
  <h2 class="font-semibold">Agents</h2>
  {#if !loading}
    <span class="text-small text-muted tabular-nums">
      {#if whole}<span class={tallyInk[tallies[0].state]}>{tallies[0].text}</span
        >{:else}{agents.length}{#each tallies as tally (tally.state)}<span
            class={tallyInk[tally.state]}>{` · ${tally.text}`}</span
          >{/each}{/if}
    </span>
  {/if}
</div>
{#if roster.added || roster.removed || totals.input || totals.cost}
  <div class="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-small tabular-nums">
    {#if roster.added || roster.removed}<span
        title="Net lines changed in the workspace ({lines(roster.added ?? 0)} added, {lines(
          roster.removed ?? 0,
        )} removed)"
        ><span class="text-positive">+{roster.added ?? 0}</span>
        <span class="text-negative">−{roster.removed ?? 0}</span></span
      >{/if}
    {#if totals.input}{@render metric("Input", totals.input)}{@render metric(
        "Output",
        totals.output!,
      )}{/if}
    {#if totals.cost}{@render metric("Cost", totals.cost)}{/if}
  </div>
{/if}

{#if loading}
  <div class="mt-3.5 grid gap-3.5" aria-busy="true" aria-label="Loading agents">
    {#each [64, 48] as width, i (i)}<div class="{skeleton} h-[18px]" style:width="{width}%">
        &nbsp;
      </div>{/each}
  </div>
{:else if agents.length}
  <ul class="-mx-2.5 mt-2">
    {#each agents as agent (agent.name)}
      {@const time = worked(agent, now)}
      {@const current = agent.state === "working" ? activity(agent) : undefined}
      {@const what = current && doing(current)}
      {@const replied = since(agent.lastResponse, now)}
      <!-- A nested agent hangs from a guide under its parent. -->
      <li
        class={agent.depth ? "border-l border-rule pl-1.5" : undefined}
        style:margin-left={agent.depth ? `${agent.depth * 16 - 6}px` : undefined}
      >
        <button
          type="button"
          class={[
            "grid w-full min-w-0 gap-1.5 rounded-card px-2.5 py-2.5 text-left pointer-coarse:py-[11px]",
            agent.name === selected ? "bg-accent-tint" : "hover:bg-raised",
          ]}
          aria-current={agent.name === selected ? "true" : undefined}
          onclick={() => onselect(agent.name)}
        >
          <span class="flex min-w-0 items-center gap-2">
            <Dot tone={dots[agent.state]} size={8} />
            <span class="min-w-0 truncate font-semibold">{name(agent)}</span>
            {#if agent.role}<Tag>{agent.role}</Tag>{/if}
            {#if time !== undefined}<span
                class="ml-auto pl-2 text-small text-muted tabular-nums"
                title="Time spent working">{duration(time)}</span
              >{/if}
          </span>
          <span class="{sub} truncate {stateInk[agent.state] ?? 'text-muted'}">
            {#if what}
              <span class="sr-only">Working:</span>
              <span class="font-medium text-accent">{what.verb}</span>
              {#if what.html}<span>{@html what.html}</span>{/if}
            {:else}
              {stateLabel[agent.state]}{#if replied && agent.state !== "working"}<span
                  class="text-muted">{` · replied ${replied}`}</span
                >{/if}
            {/if}
          </span>
          <span class="{sub} grid grid-cols-2 gap-x-4 gap-y-1 tabular-nums">
            {#if agent.inputTokens !== undefined}
              {@render metric(
                "Input",
                `${agent.usagePartial ? "≥" : ""}${tokens(agent.inputTokens)}`,
              )}
              {@render metric(
                "Output",
                `${agent.usagePartial ? "≥" : ""}${tokens(agent.outputTokens ?? 0)}`,
              )}
            {/if}
            {#if agent.cost !== undefined}{@render metric(
                "Cost",
                `${agent.costPartial ? "≥" : ""}${cost(agent.cost)}`,
              )}{/if}
            {#if agent.turns}{@render metric("Turns", String(agent.turns))}{/if}
            {#if agent.skills?.length}<span title={agent.skills.join(", ")}
                >{@render metric("Skills", String(agent.skills.length))}</span
              >{/if}
            {#if agent.added || agent.removed}<span class="flex justify-between gap-2 text-muted"
                ><span>Changes</span><span class="whitespace-nowrap"
                  ><span class="text-positive">+{agent.added ?? 0}</span>
                  <span class="text-negative">−{agent.removed ?? 0}</span></span
                ></span
              >{/if}
            {#each agent.items ?? [] as item, i (i)}<span class="col-span-2 text-muted">{item}</span
              >{/each}
          </span>
          {#if agent.context !== undefined && agent.window}
            {@const used = Math.min(100, (agent.context / agent.window) * 100)}
            <span
              class="{sub} mt-0.5 grid grid-cols-[1fr_auto] items-center gap-x-2 gap-y-1 text-muted tabular-nums"
            >
              <span>Context</span><span class="whitespace-nowrap"
                >{tokens(agent.context)} / {tokens(agent.window)}</span
              >
              <span
                class="col-span-2 h-1 w-full overflow-hidden rounded-full bg-active"
                aria-hidden="true"
                ><span class="block h-full bg-accent" style:width="{used}%"></span></span
              >
            </span>
          {/if}
        </button>
      </li>
    {/each}
  </ul>
{:else}
  <p class="{emptyBox} mt-2.5">No agents have started yet.</p>
{/if}
