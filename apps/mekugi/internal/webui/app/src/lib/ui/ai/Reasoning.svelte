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
    <Trace.Icon class="size-3.5"><Icon name="thought" size={14} /></Trace.Icon>
    <span class="shrink-0">{heading}</span>
    {#if label}<Trace.Title class="text-ink">{label}</Trace.Title>{/if}
  </Trace.Line>
{/snippet}

<Trace.Root data-ai-element="reasoning" class="gap-0">
  {#if children}
    <Trace.Collapsible>
      <Trace.CollapsibleTrigger
        class="min-h-[26px] px-1 -ml-1 hover:bg-active pointer-coarse:min-h-9"
      >
        {@render line()}
      </Trace.CollapsibleTrigger>
      <!-- Keep nested thoughts mounted so closing the group retains each choice. -->
      <Trace.CollapsibleContent class="mt-1 mb-2 ml-1.5 border-l-2 border-rule pl-[15px] text-ink">
        {@render children()}
      </Trace.CollapsibleContent>
    </Trace.Collapsible>
  {:else}
    {@render line()}
  {/if}
</Trace.Root>
