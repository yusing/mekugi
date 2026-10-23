<script lang="ts">
  import Alert from "./ui/Alert.svelte";
  import { tick } from "svelte";
  import { uploadLimit, type PendingAttachment } from "./attachments";
  import { bytes } from "./format";
  import Button from "./ui/Button.svelte";
  import Icon from "./ui/Icon.svelte";
  import { input } from "./ui/styles";
  import { newIdentity } from "./transport";

  // The attachments a composer will send, in order: browser files and paths
  // on the execution machine. The composer opens the file picker and the path
  // field, and hands over dropped or pasted files.
  let {
    items = $bindable([]),
    pathing = $bindable(false),
    disabled = false,
    machine,
  }: {
    items?: PendingAttachment[];
    pathing?: boolean;
    disabled?: boolean;
    machine: string;
  } = $props();
  let path = $state("");
  let picker: HTMLInputElement;
  let field = $state<HTMLInputElement>();
  const total = $derived(
    items.reduce((sum, item) => sum + ("file" in item ? item.file.size : 0), 0),
  );

  export function pick() {
    picker.click();
  }
  export function add(files: Iterable<File>) {
    items = [...items, ...Array.from(files, (file) => ({ id: newIdentity(), file }))];
  }
  $effect(() => {
    if (pathing) tick().then(() => field?.focus());
  });
  function addPath() {
    if (!path.trim()) return;
    items = [...items, { id: newIdentity(), path: path.trim() }];
    path = "";
    pathing = false;
  }
  const name = (item: PendingAttachment) => ("file" in item ? item.file.name : item.path);
</script>

<input
  class="sr-only"
  aria-label="Upload files"
  type="file"
  multiple
  tabindex="-1"
  bind:this={picker}
  {disabled}
  onchange={() => {
    add(picker.files ?? []);
    picker.value = "";
  }}
/>
{#if items.length || pathing}
  <div class="grid gap-2 px-2.5 pt-2.5">
    {#if items.length}
      <ul class="flex flex-wrap gap-1.5" aria-label="Attachments">
        {#each items as item (item.id)}
          <li
            class="inline-flex h-[30px] max-w-full items-center gap-1.5 rounded-control border bg-raised pr-0.5 pl-2 text-small text-muted max-sm:h-9"
          >
            <Icon name={"file" in item ? "attach" : "folder"} size={14} />
            <span
              class={["min-w-0 truncate text-ink", "path" in item && "font-mono"]}
              title={name(item)}>{name(item)}</span
            >
            {#if "file" in item}<span class="tabular-nums">{bytes(item.file.size)}</span>{/if}
            <button
              type="button"
              class="inline-flex size-6 items-center justify-center rounded-chip text-muted hover:enabled:bg-active hover:enabled:text-ink max-sm:size-8"
              {disabled}
              aria-label={`Remove ${name(item)}`}
              onclick={() => (items = items.filter((other) => other.id !== item.id))}
              ><Icon name="close" size={14} /></button
            >
          </li>
        {/each}
      </ul>
      {#if total > uploadLimit}<Alert>
          Uploads total {bytes(total)}; one message carries at most 32 MiB.
        </Alert>{/if}
    {/if}
    {#if pathing}
      <div class="flex gap-1.5">
        <label class="sr-only" for="attach-path">Path on {machine}</label>
        <input
          id="attach-path"
          class="{input} font-mono"
          bind:this={field}
          bind:value={path}
          {disabled}
          placeholder="Path on {machine}"
          autocomplete="off"
          spellcheck="false"
          onkeydown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              addPath();
            } else if (event.key === "Escape") {
              event.preventDefault();
              pathing = false;
            }
          }}
        />
        <Button disabled={disabled || !path.trim()} onclick={addPath}>Attach path</Button>
        <Button
          variant="quiet"
          icon="close"
          label="Cancel path"
          onclick={() => (pathing = false)}
        />
      </div>
    {/if}
  </div>
{/if}
