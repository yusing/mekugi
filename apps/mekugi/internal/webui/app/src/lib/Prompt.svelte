<script lang="ts">
  import Alert from "./ui/Alert.svelte";
  import { onDestroy, tick, untrack } from "svelte";
  import type { Approval, QuestionCall } from "./protocol";
  import { stream, type Stream } from "./stream.svelte";
  import { semantic } from "./semantic.svelte";
  import Button from "./ui/Button.svelte";
  import { input } from "./ui/styles";

  let {
    item,
    count = 1,
    unavailable = false,
    source = stream,
  }: {
    item: Approval | QuestionCall;
    count?: number;
    unavailable?: boolean;
    source?: Stream;
  } = $props();

  const call = $derived("questions" in item ? item : undefined);
  const approval = $derived("choices" in item ? item : undefined);
  // Keyed prompts retain their originating control view's input and outcomes.
  const state = untrack(() => semantic.prompt(item.id));
  $effect.pre(() => {
    const questions = call?.questions ?? [];
    untrack(() => {
      state.drafts = Object.fromEntries(
        questions.map((q) => [
          q.id,
          state.drafts[q.id] ?? { choice: q.options?.length ? undefined : 0, text: "" },
        ]),
      );
    });
  });
  let card: HTMLElement;
  let submittedWithFocus = false;
  const disabled = $derived(unavailable || state.sending);

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
        const d = state.drafts[q.id];
        return (
          !d || d.choice === undefined || (d.choice === (q.options?.length ?? 0) && !d.text.trim())
        );
      })
    ) {
      state.error = "Answer every question or skip it.";
      tick().then(() => card.querySelector<HTMLElement>("[role=alert]")?.focus());
      return;
    }
    submittedWithFocus = card.contains(document.activeElement);
    const pending = state;
    const release = semantic.retainInput();
    pending.sending = true;
    pending.error = await source.answer({
      call: call.id,
      answers: call.questions.map((q) => {
        const draft = pending.drafts[q.id];
        return {
          question: q.id,
          choice: typeof draft.choice === "number" ? draft.choice : undefined,
          text: draft.text,
          skip: draft.choice === "skip",
        };
      }),
    });
    pending.accepted = !pending.error;
    pending.sending = false;
    release();
  }

  async function decide(choice: number) {
    if (!approval || disabled) return;
    submittedWithFocus = card.contains(document.activeElement);
    const pending = state;
    const release = semantic.retainInput();
    pending.sending = true;
    pending.error = await source.decide({
      approval: approval.id,
      choice,
      reason: choice === pending.deny ? pending.reason : undefined,
    });
    pending.accepted = !pending.error;
    pending.sending = false;
    release();
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
  {#if item.agent}<p class="mb-2 text-small text-muted">{source.label(item.agent)}</p>{/if}
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
    {#if state.deny !== undefined}
      <form
        onsubmit={(event) => {
          event.preventDefault();
          if (state.deny !== undefined) decide(state.deny);
        }}
      >
        <label class={written}
          >Reason for denial (optional)
          <textarea
            class={answerBox}
            name="denial-reason"
            autocomplete="off"
            bind:value={state.reason}
            rows="2"
            {disabled}></textarea>
        </label>
        <div class="mt-3 flex flex-wrap gap-2">
          <Button type="submit" variant="primary" size="lg" {disabled}>Deny</Button>
          <Button size="lg" onclick={() => (state.deny = undefined)} {disabled}>Cancel</Button>
        </div>
      </form>
    {:else}
      <div class="mt-3 flex flex-wrap gap-2">
        {#each approval.choices as choice}
          <Button
            size="lg"
            {disabled}
            onclick={() => {
              state.error = undefined;
              if (choice.deny) state.deny = choice.id;
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
        {:else if state.drafts[question.id]}
          <fieldset class="mb-4 min-w-0" {disabled}>
            <legend class="font-semibold">{question.header || "Question"}</legend>
            {#if question.text && question.text.trim() !== question.header?.trim()}<p class={text}>
                {question.text}
              </p>{/if}
            {#each question.options ?? [] as option_, index}
              <label class={option}>
                <input
                  class={radio}
                  type="radio"
                  name={question.id}
                  bind:group={state.drafts[question.id].choice}
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
                bind:group={state.drafts[question.id].choice}
                value={question.options?.length ?? 0}
              />
              <span>Other</span>
            </label>
            {#if state.drafts[question.id].choice === (question.options?.length ?? 0)}
              <label class={written}
                >Your answer
                <textarea
                  class={answerBox}
                  name={question.id + "-answer"}
                  autocomplete="off"
                  bind:value={state.drafts[question.id].text}
                  rows="2"></textarea>
              </label>
            {/if}
            <label class={option}>
              <input
                class={radio}
                type="radio"
                name={question.id}
                bind:group={state.drafts[question.id].choice}
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
  {#if state.error}<Alert class="my-2" tabindex={-1}>{state.error}</Alert>
  {:else if unavailable}<p class={note} role="status">Not connected to the thread.</p>
  {:else if state.sending || state.accepted}<p class={note} role="status">
      {state.accepted ? "Submitted. Waiting for the session…" : "Sending…"}
    </p>{/if}
</article>
