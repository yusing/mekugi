<script lang="ts">
  import type { Publication } from "../../../../sessionapi/protocol";
  import Code from "./Code.svelte";
  import ResponseCopy from "./ResponseCopy.svelte";
  import { responseText } from "./kinds";
  import Markdown from "./Markdown.svelte";
  import { codeBlock, emptyBox } from "./ui/styles";

  // A side conversation as the server publishes it.
  let { publication, label }: { publication: Publication | null; label: string } = $props();
  const block = `${codeBlock} my-2 px-3 py-2.5`;
</script>

<section class="h-full overflow-y-auto overscroll-contain" aria-label={label}>
  <div class="mx-auto max-w-column px-4 pt-4 pb-10">
    {#each publication?.Entries ?? [] as entry (entry.Seq)}
      <article class="group/entry border-rule py-2.5 not-first:border-t">
        {#if entry.Text}<Markdown text={entry.Text} />{/if}
        {#each entry.Values as value, i (i)}
          {#if value.Label}<p class="mb-1 text-small text-muted">{value.Label}</p>{/if}
          {#if value.Body}<Markdown text={value.Body} />{/if}
          {#if value.Code}<pre class={block}><Code code={value.Code} lang={value.Lang} /></pre>{/if}
          {#if value.Tail.length}<pre class={block}>{value.Tail.join("\n")}</pre>{/if}
        {/each}
        {#if !entry.Assignment && entry.Agent !== "You" && !entry.Agent.endsWith(" · user")}
          {@const response = responseText(entry.Kind, entry.Text, entry.Values[0]?.Body)}
          {#if response}<ResponseCopy text={response} />{/if}
        {/if}
      </article>
    {:else}<p class="{emptyBox} mt-6">
        Nothing in this side conversation yet. Messages you send now go here.
      </p>{/each}
  </div>
</section>
