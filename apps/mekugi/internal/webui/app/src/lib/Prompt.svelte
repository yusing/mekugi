<script lang="ts">
  import Alert from "./ui/Alert.svelte";
  import { onDestroy, tick, untrack } from "svelte";
  import type { Approval, QuestionCall } from "./protocol";
  import { stream } from "./stream.svelte";
  import Button from "./ui/Button.svelte";
  import { input } from "./ui/styles";

  let {
    item,
    count = 1,
    unavailable = false,
  }: {
    item: Approval | QuestionCall;
    count?: number;
    unavailable?: boolean;
  } = $props();

  const call = $derived("questions" in item ? item : undefined);
  const approval = $derived("choices" in item ? item : undefined);
  // The parent keys each card by request ID, keeping drafts across stream patches.
  type Draft = { choice: number | "skip" | undefined; text: string };
  let drafts = $state<Record<string, Draft>>({});
  $effect.pre(() => {
    const questions = call?.questions ?? [];
    untrack(() => {
      drafts = Object.fromEntries(
        questions.map((q) => [
          q.id,
          drafts[q.id] ?? { choice: q.options?.length ? undefined : 0, text: "" },
        ]),
      );
    });
  });
  let deny = $state<number>();
  let reason = $state("");
  let sending = $state(false);
  let accepted = $state(false);
  let error = $state<string>();
  let card: HTMLElement;
  let submittedWithFocus = false;
  const disabled = $derived(unavailable || sending);

  onDestroy(() => {
    const active = document.activeElement;
    if (!card?.contains(active) && !(submittedWithFocus && active === document.body)) return;
    tick().then(() =>
      document
        .querySelector<HTMLElement>(
          "[data-prompt] input:not(:disabled), [data-prompt] button:not(:disabled), #composer-text",
        )
        ?.focus({ preventScroll: true }),
    );
  });

  async function answer() {
    if (!call || disabled) return;
    if (
      call.questions.some((q) => {
        const d = drafts[q.id];
        return (
          !d || d.choice === undefined || (d.choice === (q.options?.length ?? 0) && !d.text.trim())
        );
      })
    ) {
      error = "Answer every question or skip it.";
      tick().then(() => card.querySelector<HTMLElement>("[role=alert]")?.focus());
      return;
    }
    submittedWithFocus = card.contains(document.activeElement);
    sending = true;
    error = await stream.answer({
      call: call.id,
      answers: call.questions.map((q) => {
        const draft = drafts[q.id];
        return {
          question: q.id,
          choice: typeof draft.choice === "number" ? draft.choice : undefined,
          text: draft.text,
          skip: draft.choice === "skip",
        };
      }),
    });
    accepted = !error;
    sending = false;
  }

  async function decide(choice: number) {
    if (!approval || disabled) return;
    submittedWithFocus = card.contains(document.activeElement);
    sending = true;
    error = await stream.decide({
      approval: approval.id,
      choice,
      reason: choice === deny ? reason : undefined,
    });
    accepted = !error;
    sending = false;
  }

  const text = "my-2 whitespace-pre-wrap";
  const note = "my-2 text-small text-muted";
  const option = "flex min-h-11 cursor-pointer items-start gap-2.5 py-2.5";
  const radio = "mt-[3px] flex-none accent-accent";
  const written = "my-2 mb-3 grid gap-2";
  const answerBox = `${input} resize-y`;
</script>

<article
  class="rounded-card border bg-raised p-4 wrap-anywhere max-sm:p-3"
  data-prompt
  bind:this={card}
>
  {#if item.agent}<p class="mb-2 text-small text-muted">{stream.label(item.agent)}</p>{/if}
  {#if approval}
    <div class="flex items-baseline gap-3">
      <h2 class="font-semibold">{approval.title}</h2>
      {#if count > 1}<span class="ml-auto whitespace-nowrap text-muted">1 of {count}</span>{/if}
    </div>
    {#if approval.subject}<pre
        class="my-3 rounded-control bg-surface p-2.5 text-code whitespace-pre-wrap wrap-anywhere"><code
          >{approval.subject}</code
        ></pre>{/if}
    {#each approval.details as detail}<p class={text}>{detail}</p>{/each}
    {#if deny !== undefined}
      <form
        onsubmit={(event) => {
          event.preventDefault();
          if (deny !== undefined) decide(deny);
        }}
      >
        <label class={written}
          >Reason for denial (optional)
          <textarea
            class={answerBox}
            name="denial-reason"
            autocomplete="off"
            bind:value={reason}
            rows="2"
            {disabled}></textarea>
        </label>
        <div class="mt-3 flex flex-wrap gap-2">
          <Button type="submit" variant="primary" size="lg" {disabled}>Deny</Button>
          <Button size="lg" onclick={() => (deny = undefined)} {disabled}>Cancel</Button>
        </div>
      </form>
    {:else}
      <div class="mt-3 flex flex-wrap gap-2">
        {#each approval.choices as choice}
          <Button
            size="lg"
            {disabled}
            onclick={() => {
              error = undefined;
              if (choice.deny) deny = choice.id;
              else decide(choice.id);
            }}>{choice.label}</Button
          >
        {/each}
      </div>
    {/if}
  {:else if call}
    <form
      onsubmit={(event) => {
        event.preventDefault();
        answer();
      }}
    >
      {#each call.questions as question (question.id)}
        {#if question.secret}
          <p class={text}>Answer this secret prompt in the terminal.</p>
        {:else if drafts[question.id]}
          <fieldset class="mb-4 min-w-0" {disabled}>
            <legend class="font-semibold">{question.header || "Question"}</legend>
            <p class={text}>{question.text}</p>
            {#each question.options ?? [] as option_, index}
              <label class={option}>
                <input
                  class={radio}
                  type="radio"
                  name={question.id}
                  bind:group={drafts[question.id].choice}
                  value={index}
                />
                <span
                  >{option_.label}{#if option_.description}<small
                      class="block text-small text-muted">{option_.description}</small
                    >{/if}</span
                >
              </label>
            {/each}
            <label class={option}>
              <input
                class={radio}
                type="radio"
                name={question.id}
                bind:group={drafts[question.id].choice}
                value={question.options?.length ?? 0}
              />
              <span>Other</span>
            </label>
            {#if drafts[question.id].choice === (question.options?.length ?? 0)}
              <label class={written}
                >Your answer
                <textarea
                  class={answerBox}
                  name={question.id + "-answer"}
                  autocomplete="off"
                  bind:value={drafts[question.id].text}
                  rows="2"></textarea>
              </label>
            {/if}
            <label class={option}>
              <input
                class={radio}
                type="radio"
                name={question.id}
                bind:group={drafts[question.id].choice}
                value="skip"
              />
              <span>Skip this question</span>
            </label>
          </fieldset>
        {/if}
      {/each}
      {#if !call.questions.some((q) => q.secret)}
        <Button type="submit" variant="primary" size="lg" {disabled}>Submit answers</Button>
      {/if}
    </form>
  {/if}
  {#if error}<Alert class="my-2" tabindex={-1}>{error}</Alert>
  {:else if unavailable}<p class={note} role="status">Not connected to the thread.</p>
  {:else if sending || accepted}<p class={note} role="status">
      {accepted ? "Submitted. Waiting for the session…" : "Sending…"}
    </p>{/if}
</article>
