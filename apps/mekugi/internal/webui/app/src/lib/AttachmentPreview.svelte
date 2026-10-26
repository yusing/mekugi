<script module lang="ts">
  import type { Machine } from "./hub.svelte";
  export type PreviewTarget = { machine: Machine; thread?: string; workspace?: string };
</script>

<script lang="ts">
  let { file, path, target }: { file?: File; path?: string; target?: PreviewTarget } = $props();
  // Any path may turn out to be an image, but only one named as an image
  // waits in, or fails into, a preview box; other paths keep their plain chip.
  const imageName = $derived(/\.(png|jpe?g|gif|webp)$/i.test(path ?? ""));
  let url = $state<string>();
  let failed = $state(false);
  let unsupported = $state(false);
  $effect(() => {
    const controller = new AbortController();
    let preview: string | undefined;
    url = undefined;
    failed = false;
    unsupported = false;
    if (file) {
      url = preview = URL.createObjectURL(file);
    } else if (path && target) {
      void target.machine
        .request<{ mime: string; data: string }>(
          "session/attachment-image",
          { thread: target.thread, body: { path, workspace: target.workspace } },
          { signal: controller.signal },
        )
        .then((result) => {
          if (controller.signal.aborted) return;
          const bytes = Uint8Array.from(atob(result.data), (c) => c.charCodeAt(0));
          url = preview = URL.createObjectURL(new Blob([bytes], { type: result.mime }));
        })
        .catch(() => {
          if (controller.signal.aborted) return;
          unsupported = !imageName;
          failed = imageName;
        });
    }
    return () => {
      controller.abort();
      if (preview) URL.revokeObjectURL(preview);
    };
  });
</script>

{#if failed}
  <span class="my-1 flex size-16 shrink-0 items-center text-center text-small" role="status"
    >Preview unavailable</span
  >
{:else if url && !unsupported}
  <img
    src={url}
    alt={`Preview of ${file?.name ?? path}`}
    width="64"
    height="64"
    class="my-1 size-16 shrink-0 rounded-chip object-contain"
    onerror={() => {
      unsupported = !!file && !file.type;
      failed = !unsupported;
    }}
  />
{:else if !unsupported && (file || (target && imageName))}
  <span class="my-1 flex size-16 shrink-0 items-center text-center text-small" role="status"
    >Loading preview…</span
  >
{/if}
