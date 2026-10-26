<script lang="ts">
  import { stream } from "./stream.svelte";
  import { show } from "./view.svelte";

  // An agent's display name, in its role's colour when it has one. Another
  // agent on the roster opens its activity, unless plain, as inside a control.
  // A quiet name keeps its surroundings' weight and ink beside other text.
  let {
    name,
    current,
    plain = false,
    quiet = false,
  }: { name?: string; current: string; plain?: boolean; quiet?: boolean } = $props();

  const agent = $derived(stream.agent(name));
  const color = $derived(stream.roleColor(agent?.role));
</script>

<!-- The button's hit area is larger than its text without changing the line. -->
{#if agent && agent.name !== current && !plain}
  <button
    type="button"
    class={[
      "-my-0.5 py-0.5 underline decoration-control underline-offset-3 hover:decoration-current pointer-coarse:-my-2.5 pointer-coarse:py-2.5",
      !quiet && "font-semibold",
      color ? "text-role" : ["hover:text-accent", !quiet && "text-ink"],
    ]}
    style:--role={color}
    title="Show {agent.label}'s activity"
    onclick={() => show(agent.name)}>{agent.label}</button
  >
{:else}
  <span
    class={[!quiet && "font-semibold", color ? "text-role" : !quiet && "text-ink"]}
    style:--role={color}>{stream.label(name)}</span
  >
{/if}
