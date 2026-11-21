<script lang="ts">
  import type { Snippet } from "svelte";
  import Icon from "../Icon.svelte";
  import * as Trace from "../trace";

  let {
    heading,
    label,
    running = false,
    children,
  }: { heading: string; label?: string; running?: boolean; children?: Snippet } = $props();
</script>

{#snippet line()}
  <Trace.Line status={running ? "running" : undefined} class="min-h-[26px]">
    <Trace.Icon class={["size-3.5", running && "animate-pulse-subtle text-accent"]}
      ><Icon name="thought" size={14} /></Trace.Icon
    >
    <span class={["shrink-0 font-medium", running && "animate-pulse-subtle text-ink"]}
      >{heading}</span
    >
    {#if label}<Trace.Title class="text-ink">{label}</Trace.Title>{/if}
  </Trace.Line>
{/snippet}

<Trace.Root data-ai-element="reasoning" class="gap-0">
  {#if children}
    <Trace.Collapsible>
      <Trace.CollapsibleTrigger
        class="-ml-1.5 min-h-[28px] rounded-control px-1.5 transition-colors hover:bg-active pointer-coarse:min-h-9"
      >
        {@render line()}
      </Trace.CollapsibleTrigger>
      <!-- Keep nested thoughts mounted so closing the group retains each choice. -->
      <Trace.CollapsibleContent class="mt-1 mb-2.5 ml-2 border-l-2 border-rule pl-4 text-ink">
        {@render children()}
      </Trace.CollapsibleContent>
    </Trace.Collapsible>
  {:else}
    {@render line()}
  {/if}
</Trace.Root>
