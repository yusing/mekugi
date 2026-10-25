<script lang="ts">
  import Alert from "./ui/Alert.svelte";
  import RecoveryEditor from "./RecoveryEditor.svelte";
  import Markdown from "./Markdown.svelte";
  import { semantic } from "./semantic.svelte";
  import { stream } from "./stream.svelte";
  import Button from "./ui/Button.svelte";
  import Icon from "./ui/Icon.svelte";

  // What sits between the transcript and the composer: the side answer,
  // unsent submissions, input waiting for a turn, and the server's feedback.
  const view = $derived(semantic.frame?.view);
  const recoveries = $derived(semantic.frame?.effects.filter((effect) => effect.Recovery) ?? []);
  const disabled = $derived(semantic.connection !== "open" || semantic.pending > 0);
  const waiting = $derived(view ? [...view.Pending.Waiting, ...view.Pending.Queued] : []);
  const processes = $derived(semantic.frame?.processes ?? []);
  const feedback = $derived(view?.Status.Feedback);
  const hasDocks = $derived(
    !!(processes.length || view?.BTW || recoveries.length || waiting.length),
  );
  const hasFeedback = $derived(
    !!(
      semantic.resuming ||
      (!view && stream.connection === "open") ||
      view?.Loading ||
      feedback?.Text ||
      semantic.error
    ),
  );
  const card = "rounded-float border px-3.5 pt-2.5 pb-3";
  const heading = "flex min-h-7 items-center justify-between gap-2";
</script>

<div
  class="mx-auto flex max-h-[45dvh] w-full max-w-column flex-col gap-2 px-4 pb-2 empty:hidden max-sm:px-2"
>
  {#if hasDocks}
    <div class="grid min-h-0 gap-2 overflow-y-auto overscroll-contain">
      {#if processes.length}
        <section class="rounded-float bg-raised px-3.5 py-2.5" aria-label="Active processes">
          <h2 class="mb-1 text-small font-semibold text-muted">Active processes</h2>
          <ul class="grid gap-1">
            {#each processes as process (JSON.stringify( [process.Thread, process.Turn, process.Item] ))}
              <li class="flex min-w-0 items-center gap-2">
                <div class="grid min-w-0 flex-1 gap-0.5">
                  <code class="truncate text-small" title={process.Command}>{process.Command}</code>
                  {#if !process.ID}<span class="text-small text-muted"
                      >The host does not expose individual Stop.</span
                    >{/if}
                </div>
                {#if process.Agent && process.Agent !== "/root"}<span
                    class="min-w-0 max-w-[30%] truncate text-small text-muted"
                    title={process.Agent}>{process.Agent}</span
                  >{/if}
                <Button
                  variant="quiet"
                  disabled={disabled || !process.ID}
                  title={!process.ID
                    ? "This host command has no individual process control."
                    : undefined}
                  label={`Stop ${process.Command}`}
                  onclick={() => semantic.send({ stopProcess: process })}>Stop</Button
                >
              </li>
            {/each}
          </ul>
        </section>
      {/if}
      {#if view?.BTW}
        {@const btw = view.BTW}
        <section class="{card} bg-raised" aria-label="Side answer">
          <header class={heading}>
            <h2 class="text-small font-semibold text-muted">By the way</h2>
            <Button
              variant="quiet"
              icon="close"
              label="Close answer"
              {disabled}
              onclick={() =>
                semantic.send({ btw: { Version: view.Version, Key: btw.Key, Exchange: btw.ID } })}
            />
          </header>
          <div class="max-h-[24dvh] overflow-y-auto">
            {#each btw.History as exchange (exchange.ID)}
              <div class="text-muted"><Markdown text={exchange.Question} /></div>
              {#each exchange.Answer as answer (answer.ID)}<Markdown text={answer.Text} />{/each}
            {/each}
            <div class="text-muted"><Markdown text={btw.Question} /></div>
            {#each btw.Answer as answer (answer.ID)}<Markdown text={answer.Text} />{/each}
          </div>
          {#if btw.Status}<p
              class={["text-small", btw.Alert ? "text-negative" : "text-muted"]}
              role="status"
            >
              {btw.Status}
            </p>{/if}
        </section>
      {/if}

      {#each recoveries as effect (effect.ID)}
        {@const recovery = effect.Recovery!}
        <section class="{card} border-warning-edge bg-warning-tint" aria-label="Unsent submission">
          <header class={heading}>
            <h2 class="text-small font-semibold text-warning-ink">
              {recovery.shell ? "Unsent shell command" : "Unsent submission"}
            </h2>
          </header>
          {#if recovery.editing}
            <RecoveryEditor {effect} {disabled} />
          {:else}
            <p class={["my-1 whitespace-pre-wrap wrap-anywhere", recovery.shell && "font-mono"]}>
              {recovery.preview}
            </p>
            <p class="text-small text-muted">
              {[
                recovery.attachments && "Original attachments are retained.",
                recovery.queued && "Retry keeps this message queued for the next turn.",
                "Leaving this thread or disconnecting discards it.",
                effect.Thread !== view?.Input.ID &&
                  "Return to the thread it was written in to retry.",
              ]
                .filter(Boolean)
                .join(" ")}
            </p>
            <div class="mt-2 flex gap-2">
              <Button
                {disabled}
                onclick={() => semantic.send({ recovery: { effect: effect.ID, action: "edit" } })}
                >Edit</Button
              >
              <Button
                disabled={disabled || effect.Thread !== view?.Input.ID}
                onclick={() => semantic.send({ recovery: { effect: effect.ID, action: "retry" } })}
                >Retry intact</Button
              >
              <Button
                variant="quiet"
                {disabled}
                onclick={() =>
                  semantic.send({ recovery: { effect: effect.ID, action: "discard" } })}
                >Discard</Button
              >
            </div>
          {/if}
        </section>
      {/each}

      {#if waiting.length}
        <div class="flex items-start gap-2 rounded-float border bg-raised pr-1">
          <details class="min-w-0 flex-1">
            <summary
              class="flex min-h-9 items-center gap-2 px-3.5 text-small text-muted max-sm:min-h-11"
            >
              <Icon name="history" size={14} />
              {waiting.length === 1 ? "1 message waits" : `${waiting.length} messages wait`} for main's
              next turn
            </summary>
            <ol class="list-decimal pr-3.5 pb-2.5 pl-9">
              {#each waiting as item, i (i)}<li class="wrap-anywhere">{item.Text}</li>{/each}
            </ol>
          </details>
          <Button
            variant="quiet"
            class="my-0.5"
            label="Edit queued messages"
            disabled={disabled || view?.Input.ID !== view?.Main.ID}
            onclick={() => semantic.editWaiting()}>Edit</Button
          >
          <Button
            variant="quiet"
            class="my-0.5"
            label="Steer queued messages"
            disabled={disabled || view?.Input.ID !== view?.Main.ID || !view?.Pending.CanSteer}
            onclick={() => semantic.steerWaiting()}>Steer</Button
          >
        </div>
      {/if}
    </div>
  {/if}
  {#if hasFeedback}
    <div
      class="grid max-h-[20dvh] shrink-0 gap-2 overflow-y-auto overscroll-contain"
      aria-label="Thread feedback"
    >
      <!-- A lost machine connection is announced above the transcript. -->
      {#if semantic.resuming}<p class="mx-1 text-small text-muted" role="status">
          {semantic.resuming}
        </p>{/if}
      {#if !view && stream.connection === "open"}
        <p class="mx-1 text-small text-muted" role="status">
          {semantic.connection === "connecting"
            ? "Connecting thread controls…"
            : "Thread controls disconnected."}
          {#if semantic.connectionError}{semantic.connectionError}{/if}
        </p>
      {:else if view?.Loading}<p class="mx-1 text-small text-muted" role="status">
          {view?.Loading}
        </p>{/if}
      {#if feedback?.Text}<p
          class={["mx-1 text-small", feedback.Alert && "text-negative"]}
          role={feedback.Alert ? "alert" : "status"}
        >
          {feedback.Text}
        </p>{/if}
      {#if semantic.error}<Alert class="mx-1">
          {semantic.error}
        </Alert>{/if}
    </div>
  {/if}
</div>
