<script lang="ts">
  import { onDestroy } from "svelte";
  import Button from "./ui/Button.svelte";
  import Icon from "./ui/Icon.svelte";
  import { revealed, rowAction } from "./ui/styles";

  // Copies a response's text. In a transcript row it sits in the row's
  // gutter, below Details when the row has it, and only a failure takes
  // space under the row; elsewhere it follows the response.
  let {
    text,
    gutter = false,
    below = false,
  }: { text: string; gutter?: boolean; below?: boolean } = $props();
  let feedback = $state<"copied" | "failed">();
  let reset: ReturnType<typeof setTimeout>;
  onDestroy(() => clearTimeout(reset));

  async function copy() {
    clearTimeout(reset);
    try {
      await navigator.clipboard.writeText(text);
      feedback = "copied";
    } catch {
      feedback = "failed";
    }
    reset = setTimeout(() => (feedback = undefined), feedback === "failed" ? 5000 : 1500);
  }
  const icon = $derived(feedback === "copied" ? "check" : feedback === "failed" ? "close" : "copy");
  const label = $derived(
    feedback === "copied" ? "Copied" : feedback === "failed" ? "Copy failed" : "Copy response",
  );
</script>

{#snippet status()}
  <p
    role="status"
    class={feedback === "failed"
      ? ["text-small text-negative", gutter && "ml-19 max-sm:ml-0"]
      : "sr-only"}
  >
    {feedback === "copied"
      ? "Response copied."
      : feedback === "failed"
        ? "Could not copy the response. Select its text and copy it manually, or allow clipboard access and retry."
        : ""}
  </p>
{/snippet}

{#if gutter}
  <button
    type="button"
    class={[
      rowAction,
      "absolute -right-9 max-sm:-right-10",
      below ? "top-9 max-sm:top-11" : "top-1",
      feedback === "failed" ? "text-negative" : "text-muted",
      !feedback && revealed,
    ]}
    aria-label={label}
    title={label}
    onclick={copy}><Icon name={icon} size={14} /></button
  >
  {@render status()}
{:else}
  <div class="mt-1 flex flex-wrap items-center gap-2">
    <Button
      variant={feedback === "failed" ? "quiet-danger" : "quiet"}
      size="sm"
      {icon}
      {label}
      class={feedback
        ? ""
        : "opacity-0 group-hover/entry:opacity-100 focus-visible:opacity-100 touch:opacity-100"}
      onclick={copy}
    />
    {@render status()}
  </div>
{/if}
