<script lang="ts">
  import { stream } from "./stream.svelte";
  import { show } from "./view.svelte";

  // An agent's display name. Another agent on the roster opens its activity.
  let { name, current }: { name?: string; current: string } = $props();

  const agent = $derived(stream.agent(name));
</script>

<!-- The button's hit area is larger than its text without changing the line. -->
{#if agent && agent.name !== current}
  <button
    type="button"
    class="-my-0.5 py-0.5 font-semibold text-ink underline decoration-control underline-offset-3 hover:text-accent hover:decoration-current pointer-coarse:-my-2.5 pointer-coarse:py-2.5"
    title="Show {agent.label}'s activity"
    onclick={() => show(agent.name)}>{agent.label}</button
  >
{:else}
  <span class="font-semibold text-ink">{stream.label(name)}</span>
{/if}
