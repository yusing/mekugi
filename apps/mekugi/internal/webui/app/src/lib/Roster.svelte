<script lang="ts">
  import { doing, since, stateLabel, worked } from "./agents";
  import { atLeast, cost, duration, progressText, tokens } from "./format";
  import type { Agent, Counts, Entry, Roster } from "./protocol";
  import Dot, { type Tone } from "./ui/Dot.svelte";
  import { emptyBox, skeleton } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";

  // The agents of the session, as a tree under Main. Selecting one shows its
  // own activity; now is the recorded clock, for timers. The saved review's
  // edit lines, by agent and net, are newer than the roster's when present.
  let {
    roster,
    lines: edited,
    net,
    loading = false,
    selected,
    now,
    activity,
    onselect,
  }: {
    roster: Roster;
    lines?: Record<string, Counts>;
    net?: Counts | null;
    loading?: boolean;
    selected: string;
    now: number;
    activity: (agent: Agent) => Entry | undefined;
    onselect: (name: string) => void;
  } = $props();

  const agents = $derived(roster.agents);
  const labels = $derived(new Map(agents.map((agent) => [agent.name, agent.label])));
  const known = (c?: Counts | null): c is Counts => !!c && c.Added >= 0 && c.Removed >= 0;
  const changes = (agent: Agent) =>
    edited?.[agent.name] ?? { Added: agent.added ?? 0, Removed: agent.removed ?? 0 };
  const whole = $derived(
    known(net) ? net : { Added: roster.added ?? 0, Removed: roster.removed ?? 0 },
  );
  const lines = (n: number) => `${n} ${n === 1 ? "line" : "lines"}`;
  const unit = (n: number, word: string) => (n === 1 ? word : `${word}s`);
  const plural = (n: number, word: string) => `${n} ${unit(n, word)}`;
  // Count active waits separately from model work, completed work and failures.
  const tallies = $derived(
    (["working", "waiting", "done", "error"] as const)
      .map((state) => ({ state, n: agents.filter((a) => a.state === state).length }))
      .filter(({ n }) => n)
      .map(({ state, n }) => ({
        state,
        n,
        text: state === "error" ? plural(n, "error") : `${n} ${state}`,
      })),
  );
  // A tally that covers every agent says the total itself.
  const single = $derived(tallies.length === 1 && tallies[0].n === agents.length);

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
    return {
      input: tokensKnown ? atLeast(tokens(input), partial) : undefined,
      output: tokensKnown ? atLeast(tokens(output), partial) : undefined,
      cost: costKnown ? atLeast(cost(usd), partial) : undefined,
    };
  });

  // Nested agents show their own name under their parent.
  const name = (agent: Agent) =>
    agent.depth ? agent.label.slice(agent.label.lastIndexOf("/") + 1) : agent.label;

  const dots: Record<Agent["state"], Tone> = {
    working: "running",
    waiting: "hollow",
    done: "done",
    error: "alert",
    idle: "hollow",
  };
  const tallyInk: Record<string, string> = { working: "text-accent", error: "text-negative" };
  const stateInk: Record<string, string> = { working: "text-ink", error: "text-negative" };
  const sub = "pl-4 text-small leading-[18px]";
</script>

<!-- A value with its unit in muted ink: 33.72M in, 7 turns. -->
{#snippet metric(value: string, unit: string, title?: string)}
  <span class="whitespace-nowrap" {title}
    ><span class="text-ink">{value}</span> <span class="text-muted">{unit}</span></span
  >
{/snippet}
{#snippet changed(added = 0, removed = 0, title?: string)}
  <span class="whitespace-nowrap" {title}
    ><span class="text-positive">+{added}</span>
    <span class="text-negative">−{removed}</span></span
  >
{/snippet}

<div class="mt-3.5 mb-0.5 flex items-baseline gap-2.5">
  <h2 class="font-semibold">Agents</h2>
  {#if !loading}
    <span class="text-small text-muted tabular-nums">
      {#if single}<span class={tallyInk[tallies[0].state]}>{tallies[0].text}</span
        >{:else}{agents.length}{#each tallies as tally (tally.state)}<span
            class="ml-2 {tallyInk[tally.state] ?? ''}">{tally.text}</span
          >{/each}{/if}
    </span>
  {/if}
</div>
{#if whole.Added || whole.Removed || totals.input || totals.cost}
  <div class="mt-1.5 flex flex-wrap gap-x-3 gap-y-0.5 text-small tabular-nums">
    {#if whole.Added || whole.Removed}{@render changed(
        whole.Added,
        whole.Removed,
        `Net lines changed in the workspace (${lines(whole.Added)} added, ${lines(whole.Removed)} removed)`,
      )}{/if}
    {#if totals.input}{@render metric(totals.input, "in", "Input tokens")}{@render metric(
        totals.output!,
        "out",
        "Output tokens",
      )}{/if}
    {#if totals.cost}<span class="text-ink" title="Cost">{totals.cost}</span>{/if}
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
      {@const waiting = agent.wait && progressText(agent.wait, (name) => labels.get(name) ?? name)}
      {@const replied = since(agent.lastResponse, now)}
      {@const edits = changes(agent)}
      <!-- A nested agent hangs from a guide under its parent. -->
      <li
        class={agent.depth ? "border-l border-rule pl-1.5" : undefined}
        style:margin-left={agent.depth ? `${agent.depth * 16 - 6}px` : undefined}
      >
        <button
          type="button"
          class={[
            "relative grid w-full min-w-0 gap-1 rounded-card px-2.5 pt-2 pb-3 text-left pointer-coarse:pt-[11px] pointer-coarse:pb-[13px]",
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
          <span class="{sub} truncate {stateInk[agent.state] ?? 'text-muted'}" title={waiting}>
            {#if agent.state === "waiting" && waiting}
              {waiting}
            {:else if what}
              <span class="sr-only">Working:</span>
              <span class="font-medium text-accent">{what.verb}</span>
              {#if what.html}<span>{@html what.html}</span>{/if}
            {:else}
              {stateLabel[agent.state]}{#if replied && agent.state !== "working"}<span
                  class="ml-2 text-muted">replied {replied}</span
                >{/if}
            {/if}
          </span>
          <span class="{sub} flex flex-wrap gap-x-3 gap-y-0.5 tabular-nums empty:hidden">
            {#if edits.Added || edits.Removed}{@render changed(
                edits.Added,
                edits.Removed,
                "Lines this agent changed",
              )}{/if}
            {#if agent.inputTokens !== undefined}{@render metric(
                atLeast(tokens(agent.inputTokens), agent.usagePartial),
                "in",
                "Input tokens",
              )}{@render metric(
                atLeast(tokens(agent.outputTokens ?? 0), agent.usagePartial),
                "out",
                "Output tokens",
              )}{/if}
            {#if agent.cost !== undefined}<span class="text-ink" title="Cost"
                >{atLeast(cost(agent.cost), agent.costPartial)}</span
              >{/if}
            {#if agent.turns}{@render metric(String(agent.turns), unit(agent.turns, "turn"))}{/if}
            {#if agent.roundtrips}{@render metric(
                String(agent.roundtrips),
                unit(agent.roundtrips, "round trip"),
              )}{/if}
            {#if agent.skills?.length}{@render metric(
                String(agent.skills.length),
                unit(agent.skills.length, "skill"),
                agent.skills.join(", "),
              )}{/if}
            {#each agent.items ?? [] as item, i (i)}<span class="text-muted">{item}</span>{/each}
            {#if agent.context !== undefined}<span class="whitespace-nowrap" title="Context used"
                ><span class="text-ink">{tokens(agent.context)}</span>
                <span class="text-muted"
                  >{agent.window ? `of ${tokens(agent.window)} context` : "context"}</span
                ></span
              >{/if}
          </span>
          <!-- The context bar runs along the card's foot rather than taking a row. -->
          {#if agent.context !== undefined && agent.window}
            <span
              class="absolute inset-x-2.5 bottom-1 h-[3px] overflow-hidden rounded-full bg-active"
              aria-hidden="true"
              ><span
                class="block h-full bg-accent"
                style:width="{Math.min(100, (agent.context / agent.window) * 100)}%"
              ></span></span
            >
          {/if}
        </button>
      </li>
    {/each}
  </ul>
{:else}
  <p class="{emptyBox} mt-2.5">No agents have started yet.</p>
{/if}
