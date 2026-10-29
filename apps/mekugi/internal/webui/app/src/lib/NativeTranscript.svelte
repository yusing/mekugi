<script lang="ts">
  import type { Publication } from "../../../../sessionapi/protocol";
  import Code from "./Code.svelte";
  import ResponseCopy from "./ResponseCopy.svelte";
  import { responseText } from "./kinds";
  import Markdown from "./Markdown.svelte";
  import { codeBlock, emptyBox } from "./ui/styles";
  import Wait from "./Wait.svelte";
  import * as Message from "./ui/message";
  import * as Trace from "./ui/trace";

  // A side conversation as the server publishes it. A wait shows its targets
  // by name instead of the entry's flattened text.
  let { publication, label }: { publication: Publication | null; label: string } = $props();
  const block = `${codeBlock} my-2 px-3 py-2.5`;
</script>

<section class="h-full overflow-y-auto overscroll-contain" aria-label={label}>
  <div class="mx-auto max-w-column px-4 pt-4 pb-10">
    {#each publication?.Entries ?? [] as entry (entry.Seq)}
      {@const waits = entry.Values.some((value) => value.WaitTargets?.length)}
      <article class="group/entry py-2.5">
        <Message.Root>
          <Message.Content class="gap-0">
            {#if entry.Text && !waits}<Markdown text={entry.Text} />{/if}
            {#each entry.Values as value, i (i)}
              {#if value.Label}<Trace.Line class="mb-1 text-small"
                  ><Trace.Title class="overflow-visible whitespace-normal"
                    >{value.Label}</Trace.Title
                  ></Trace.Line
                >{/if}
              {#if value.WaitTargets?.length}<p class="text-muted">
                  <Wait
                    text={value.Body}
                    targets={value.WaitTargets.map((target) => ({
                      name: target.Name,
                      status: target.Status,
                    }))}
                    timeout={value.WaitTimeout}
                    reason={value.WaitReason}
                    owner=""
                  />
                </p>
              {:else if value.Body}<Markdown text={value.Body} />{/if}
              {#if value.Code}<pre class={block}><Code
                    code={value.Code}
                    lang={value.Lang}
                  /></pre>{/if}
              {#if value.Tail.length}<pre class={block}>{value.Tail.join("\n")}</pre>{/if}
            {/each}
            {#if !entry.Assignment && entry.Agent !== "You" && !entry.Agent.endsWith(" · user")}
              {@const response = responseText(entry.Kind, entry.Text, entry.Values[0]?.Body)}
              {#if response}<ResponseCopy text={response} />{/if}
            {/if}
          </Message.Content>
        </Message.Root>
      </article>
    {:else}<p class="{emptyBox} mt-6">
        Nothing in this side conversation yet. Messages you send now go here.
      </p>{/each}
  </div>
</section>
