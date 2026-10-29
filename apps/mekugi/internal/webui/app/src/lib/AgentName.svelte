<script lang="ts">
  import { untrack } from "svelte";
  import { stream } from "./stream.svelte";
  import { show } from "./view.svelte";

  // An agent's display name, in its role's colour when it has one. Another
  // agent on the roster opens its activity, unless plain, as inside a control.
  // A partial roster has not loaded every agent yet. Confirm membership
  // through its targeted read before turning an unresolved name into a link.
  // A quiet name keeps its surroundings' weight and ink beside other text.
  let {
    name,
    current,
    plain = false,
    quiet = false,
  }: { name?: string; current: string; plain?: boolean; quiet?: boolean } = $props();

  const agent = $derived(stream.agent(name));
  const color = $derived(stream.roleColor(agent?.role));
  const target = $derived(!plain && agent && agent.name !== current ? agent.name : undefined);
  $effect(() => {
    const address = name && stream.address(name);
    if (
      !plain &&
      address &&
      address !== current &&
      !agent &&
      stream.roster.next &&
      !stream.unavailableAgents.includes(address)
    )
      untrack(() => void stream.moreRoster(address));
  });
</script>

<!-- The button's hit area is larger than its text without changing the line. -->
{#if target}
  <button
    type="button"
    class={[
      "-my-0.5 py-0.5 underline decoration-control underline-offset-3 hover:decoration-current pointer-coarse:-my-2.5 pointer-coarse:py-2.5",
      !quiet && "font-semibold",
      color ? "text-role" : ["hover:text-accent", !quiet && "text-ink"],
    ]}
    style:--role={color}
    title="Show {stream.label(name)}'s activity"
    onclick={() => show(target)}>{stream.label(name)}</button
  >
{:else}
  <span
    class={[!quiet && "font-semibold", color ? "text-role" : !quiet && "text-ink"]}
    style:--role={color}>{stream.label(name)}</span
  >
{/if}
