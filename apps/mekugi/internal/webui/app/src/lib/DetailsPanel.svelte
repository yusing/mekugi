<script lang="ts">
  import { untrack } from "svelte";
  import Alert from "./ui/Alert.svelte";
  import type { NativeDetails } from "../../../../sessionapi/protocol";
  import Code from "./Code.svelte";
  import { pathLanguage } from "./highlight";
  import Markdown from "./Markdown.svelte";
  import ReferenceSelection from "./ReferenceSelection.svelte";
  import { stream } from "./stream.svelte";
  import { errorText } from "./transport";
  import { semantic } from "./semantic.svelte";
  import Button from "./ui/Button.svelte";
  import { codeBlock } from "./ui/styles";

  // One activity's details from the server, in the right panel.
  let {
    details,
    onclose,
    onreference,
  }: { details: NativeDetails; onclose: () => void; onreference: () => void } = $props();
  let hydrated = $state.raw<NativeDetails>();
  let failure = $state<string>();
  let retry = $state(0);
  const display = $derived(hydrated ?? details);
  const contentIdentity = $derived(JSON.stringify(details.content));
  $effect(() => {
    void contentIdentity;
    const reference = untrack(() => details.content);
    void retry;
    hydrated = undefined;
    failure = undefined;
    if (!reference) return;
    const request = new AbortController();
    void stream
      .readContent(reference, request.signal)
      .then((result) => {
        if (request.signal.aborted) return;
        if (!result.details) throw new Error("The complete details are unavailable.");
        hydrated = result.details;
      })
      .catch((error) => {
        if (!request.signal.aborted) failure = errorText(error);
      });
    return () => request.abort();
  });
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
  <header class="flex min-h-12 items-center gap-2 pr-2 pl-4">
    <h2
      id="details-heading"
      class="min-w-0 flex-1 truncate font-semibold focus:outline-none"
      tabindex="-1"
      bind:this={heading}
      title={display.file?.Path}
    >
      {display.file?.Path || "Activity details"}
    </h2>
    <ReferenceSelection
      root={content}
      source={display.file?.Path || ""}
      description="dialog"
      {onreference}
    />
    <Button variant="quiet" icon="close" label="Close details" onclick={onclose} />
  </header>
  <div bind:this={content} class="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pt-3 pb-6">
    {#if display.content}
      {#if failure}<Alert>{failure}</Alert><Button variant="quiet" onclick={() => retry++}
          >Retry complete details</Button
        >
      {:else}<p class="text-small text-muted">Loading complete details…</p>{/if}
    {/if}
    {#if display.file}<pre class={block}><Code
          code={display.file.Source}
          lang={pathLanguage(display.file.Path)}
        /></pre>{/if}
    {#each display.view.Pages as page (page.ID)}
      <section class="not-first:mt-6">
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
        {#if page.Tail.length}<pre class={block}><Code
              code={page.Tail.join("\n")}
              lang={display.file ? pathLanguage(display.file.Path) : undefined}
            /></pre>{/if}
        {#if display.output?.Page === page.ID}
          <pre class={block}><Code
              code={display.output.View.Lines.join("\n")}
              lang={display.file ? pathLanguage(display.file.Path) : undefined}
            /></pre>
          {#if display.output.View.Dropped}<p class="text-small text-muted">
              {display.output.View.Dropped} earlier lines are no longer kept.
            </p>{/if}
        {:else if page.Retained}<Button
            {disabled}
            onclick={() =>
              semantic.send({
                detailOutput: { ID: display.view.ID, Page: page.ID, Version: page.OutputVersion },
              })}>Load full output</Button
          >{/if}
      </section>
    {:else}
      {#if !display.file && !display.content}<p class="text-small text-muted">
          No further details for this activity.
        </p>{/if}
    {/each}
    {#if semantic.error}<Alert>{semantic.error}</Alert>{/if}
  </div>
</section>
