<script lang="ts">
  import Alert from "./ui/Alert.svelte";
  import type { NativeDetails } from "../../../../sessionapi/protocol";
  import Code from "./Code.svelte";
  import Markdown from "./Markdown.svelte";
  import ReferenceSelection from "./ReferenceSelection.svelte";
  import { semantic } from "./semantic.svelte";
  import Button from "./ui/Button.svelte";
  import { codeBlock } from "./ui/styles";

  // One activity's details from the server, in the right panel.
  let {
    details,
    onclose,
    onreference,
  }: { details: NativeDetails; onclose: () => void; onreference: () => void } = $props();
  const disabled = $derived(semantic.connection !== "open" || semantic.pending > 0);
  let heading = $state<HTMLElement>();
  let content = $state<HTMLElement>();
  $effect(() => {
    void details.view.ID.Selection;
    heading?.focus({ preventScroll: true });
  });
  const block = `${codeBlock} my-2 px-3 py-2.5`;
</script>

<section class="flex h-full min-h-0 flex-col" aria-labelledby="details-heading">
  <header class="flex min-h-12 items-center gap-2 border-b border-rule pr-2 pl-4">
    <h2
      id="details-heading"
      class="min-w-0 flex-1 truncate font-semibold focus:outline-none"
      tabindex="-1"
      bind:this={heading}
      title={details.file?.Path}
    >
      {details.file?.Path || "Activity details"}
    </h2>
    <ReferenceSelection root={content} source={details.file?.Path || ""} {onreference} />
    <Button variant="quiet" icon="close" label="Close details" onclick={onclose} />
  </header>
  <div bind:this={content} class="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pt-3 pb-6">
    {#if details.file}<pre class={block}><Code code={details.file.Source} /></pre>{/if}
    {#each details.view.Pages as page (page.ID)}
      <section class="not-first:mt-4 not-first:border-t not-first:border-rule not-first:pt-4">
        {#if page.Label || page.Path || page.Verb}<h3
            class={[
              "mb-2 text-small font-semibold wrap-anywhere",
              !page.Label && !!page.Path && "font-mono",
            ]}
          >
            {page.Label || page.Path || page.Verb}
          </h3>{/if}
        {#if page.Body}<Markdown text={page.Body} />{/if}
        {#if page.Code}<pre class={block}><Code code={page.Code} lang={page.Lang} /></pre>{/if}
        {#if page.Tail.length}<pre class={block}>{page.Tail.join("\n")}</pre>{/if}
        {#if details.output?.Page === page.ID}
          <pre class={block}>{details.output.View.Lines.join("\n")}</pre>
          {#if details.output.View.Dropped}<p class="text-small text-muted">
              {details.output.View.Dropped} earlier lines are no longer kept.
            </p>{/if}
        {:else if page.Retained}<Button
            {disabled}
            onclick={() =>
              semantic.send({
                detailOutput: { ID: details.view.ID, Page: page.ID, Version: page.OutputVersion },
              })}>Load full output</Button
          >{/if}
      </section>
    {:else}
      {#if !details.file}<p class="text-small text-muted">
          No further details for this activity.
        </p>{/if}
    {/each}
    {#if semantic.error}<Alert>{semantic.error}</Alert>{/if}
  </div>
</section>
