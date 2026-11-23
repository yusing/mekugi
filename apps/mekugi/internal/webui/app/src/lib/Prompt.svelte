<script lang="ts">
  import Alert from "./ui/Alert.svelte";
  import Attachments from "./Attachments.svelte";
  import { prepareAttachments } from "./attachments";
  import { onDestroy, tick, untrack } from "svelte";
  import type { Approval, QuestionCall } from "./protocol";
  import { stream } from "./stream.svelte";
  import { semantic, questionDraft, questionAnswers } from "./semantic.svelte";
  import Tag from "./ui/Tag.svelte";
  import Button from "./ui/Button.svelte";
  import { input } from "./ui/styles";

  let {
    item,
    count = 1,
    position = 0,
    questionIndex = 0,
    onquestion,
    total = 0,
    onprevious,
    onnext,
    unavailable = false,
  }: {
    item: Approval | QuestionCall;
    count?: number;
    position?: number;
    questionIndex?: number;
    onquestion?: (index: number) => void;
    total?: number;
    onprevious?: () => void;
    onnext?: () => void;
    unavailable?: boolean;
  } = $props();

  const call = $derived("questions" in item ? item : undefined);
  const approval = $derived("choices" in item ? item : undefined);
  // Keyed prompts retain their originating control view's input and outcomes.
  const promptState = untrack(() => semantic.prompt(item.id));
  $effect.pre(() => {
    const questions = call?.questions ?? [];
    untrack(() => {
      for (const q of questions) promptState.drafts[q.id] ??= questionDraft(q);
    });
  });
  let card: HTMLElement;
  let submittedWithFocus = false;
  let trays: Record<string, ReturnType<typeof Attachments>> = {};
  const acquisitions = new Set<AbortController>();
  let hovered = $state<Record<string, number | undefined>>({});
  let focused = $state<Record<string, number | undefined>>({});
  const activeDraft = $derived(
    call?.questions[questionIndex]
      ? promptState.drafts[call.questions[questionIndex].id]
      : undefined,
  );
  let progress = $state("");
  const disabled = $derived(
    unavailable || promptState.sending || (!!call?.blocking && promptState.accepted),
  );

  onDestroy(() => {
    for (const controller of acquisitions) controller.abort();
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

  async function submitAnswers(id?: string) {
    if (!call || disabled) return;
    const target = call;
    const pending = promptState;
    let answers;
    try {
      answers = questionAnswers(target, pending.drafts, id);
    } catch (failure) {
      pending.error = failure instanceof Error ? failure.message : "Could not prepare answers.";
      if (failure instanceof Error)
        onquestion?.(target.questions.findIndex((q) => q.id === failure.cause));
      tick().then(() => card.querySelector<HTMLElement>("[role=alert]")?.focus());
      return;
    }
    const drafts = answers.map((answer) => pending.drafts[answer.question]);
    if (drafts.some((draft) => draft.sending || draft.accepted)) return;
    submittedWithFocus = card.contains(document.activeElement);
    const release = semantic.retainInput();
    const captured = semantic.event;
    const controller = new AbortController();
    acquisitions.add(controller);
    if (target.blocking) pending.sending = true;
    for (const draft of drafts) {
      draft.sending = true;
      draft.accepted = false;
    }
    pending.error = undefined;
    pending.accepted = false;
    try {
      for (const answer of answers) {
        const q = target.questions.find((candidate) => candidate.id === answer.question)!;
        const draft = pending.drafts[q.id];
        if (draft.choice === (q.options?.length ?? 0)) {
          answer.attachments = await prepareAttachments(
            draft.items,
            controller.signal,
            (read, size) => {
              progress = size
                ? `Reading images: ${Math.round((read / size) * 100)}%`
                : "Preparing answers…";
            },
          );
        }
      }
      progress = "Sending…";
      const error = await semantic.send(
        { answers: { call: target.id, answers } },
        controller.signal,
        false,
        captured,
      );
      pending.error = error;
      if (target.blocking) pending.accepted = !error;
      for (const draft of drafts) draft.accepted = !error;
    } catch (failure) {
      pending.error = controller.signal.aborted
        ? "Attachment preparation cancelled."
        : failure instanceof Error
          ? failure.message
          : "Could not prepare answers.";
    } finally {
      acquisitions.delete(controller);
      progress = "";
      if (target.blocking) pending.sending = false;
      for (const draft of drafts) draft.sending = false;
      release(pending.error);
    }
  }

  async function decide(choice: number) {
    if (!approval || disabled) return;
    submittedWithFocus = card.contains(document.activeElement);
    const pending = promptState;
    const release = semantic.retainInput();
    pending.sending = true;
    const event = semantic.event;
    pending.error = event
      ? await semantic.send(
          {
            approval: {
              Version: event.frame.view.Version,
              Request: approval.id,
              Choice: choice,
              Reason: choice === pending.deny ? pending.reason : "",
            },
          },
          undefined,
          false,
          event,
        )
      : "Wait for this thread's control view to connect.";
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
  class="rounded-float border border-border bg-raised p-4 shadow-xs wrap-anywhere max-sm:p-3"
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
        class="my-3 rounded-control border border-rule bg-surface p-2.5 text-code whitespace-pre-wrap wrap-anywhere"><code
          >{approval.subject}</code
        ></pre>{/if}
    {#each approval.details as detail}<p class={text}>{detail}</p>{/each}
    {#if promptState.deny !== undefined}
      <form
        onsubmit={(event) => {
          event.preventDefault();
          if (promptState.deny !== undefined) decide(promptState.deny);
        }}
      >
        <label class={written}
          >Reason for denial (optional)
          <textarea
            class={answerBox}
            name="denial-reason"
            autocomplete="off"
            bind:value={promptState.reason}
            rows="2"
            {disabled}></textarea>
        </label>
        <div class="mt-3 flex flex-wrap gap-2">
          <Button type="submit" variant="primary" size="lg" {disabled}>Deny</Button>
          <Button size="lg" onclick={() => (promptState.deny = undefined)} {disabled}>Cancel</Button
          >
        </div>
      </form>
    {:else}
      <div class="mt-3 flex flex-wrap gap-2">
        {#each approval.choices as choice}
          <Button
            size="lg"
            {disabled}
            onclick={() => {
              promptState.error = undefined;
              if (choice.deny) promptState.deny = choice.id;
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
        submitAnswers(call?.questions[questionIndex]?.id);
      }}
    >
      <div class="mb-3 flex items-center gap-3">
        <Button
          variant="quiet"
          aria-label="Previous question"
          disabled={disabled || position === 0}
          onclick={onprevious}>Previous</Button
        >
        <span class="text-small text-muted" aria-live="polite"
          >{position + 1} of {total || call.questions.length}</span
        >
        <Button
          variant="quiet"
          aria-label="Next question"
          disabled={disabled || position + 1 >= (total || call.questions.length)}
          onclick={onnext}>Next</Button
        >
      </div>
      {#each call.questions.slice(questionIndex, questionIndex + 1) as question (question.id)}
        {#if question.secret}
          <p class={text}>Answer this secret prompt in the terminal.</p>
        {:else if promptState.drafts[question.id]}
          {@const draft = promptState.drafts[question.id]}
          {@const questionDisabled = disabled || draft.sending || draft.accepted}
          {@const previewIndex = hovered[question.id] ?? focused[question.id] ?? draft.choice}
          {@const preview =
            typeof previewIndex === "number"
              ? question.options?.[previewIndex]?.preview
              : undefined}
          <fieldset class="mb-4 min-w-0" disabled={questionDisabled}>
            <legend class="font-semibold">{question.header || "Question"}</legend>
            {#if question.text && question.text.trim() !== question.header?.trim()}<p class={text}>
                {question.text}
              </p>{/if}
            <div class="question-layout">
              <div>
                {#each question.options ?? [] as option_, index}
                  <label
                    class={option}
                    onmouseenter={() => (hovered[question.id] = index)}
                    onmouseleave={() => (hovered[question.id] = undefined)}
                  >
                    <input
                      class={radio}
                      type="radio"
                      name={question.id}
                      bind:group={promptState.drafts[question.id].choice}
                      value={index}
                      onfocus={() => {
                        focused[question.id] = index;
                        hovered[question.id] = undefined;
                      }}
                      onblur={() => (focused[question.id] = undefined)}
                    />
                    <span
                      ><span class="flex flex-wrap items-center gap-2"
                        >{option_.label}{#if option_.recommended}<Tag>Recommended</Tag>{/if}</span
                      >{#if option_.description}<small class="block text-small text-muted"
                          >{option_.description}</small
                        >{/if}</span
                    >
                  </label>
                {/each}
                <label
                  class={option}
                  onmouseenter={() => (hovered[question.id] = question.options?.length ?? 0)}
                  onmouseleave={() => (hovered[question.id] = undefined)}
                >
                  <input
                    class={radio}
                    type="radio"
                    name={question.id}
                    bind:group={promptState.drafts[question.id].choice}
                    value={question.options?.length ?? 0}
                    onfocus={() => {
                      focused[question.id] = question.options?.length ?? 0;
                      hovered[question.id] = undefined;
                    }}
                    onblur={() => (focused[question.id] = undefined)}
                  />
                  <span>Other</span>
                </label>
              </div>
              {#if preview}<section class="question-preview min-w-0" aria-label="Option preview">
                  <h3 class="mb-2 text-small text-muted">Preview</h3>
                  <!-- svelte-ignore a11y_no_noninteractive_tabindex (Keyboard scrolling.) -->
                  <pre
                    tabindex="0"
                    role="region"
                    aria-label="Preview text"
                    class="max-h-64 overflow-auto rounded-control border border-rule bg-surface p-3 text-code whitespace-pre-wrap wrap-anywhere">{preview}</pre>
                </section>{/if}
            </div>
            {#if typeof draft.choice === "number" && draft.choice < (question.options?.length ?? 0)}
              <label class={written}
                >Note (optional)<textarea
                  class={answerBox}
                  name={question.id + "-note"}
                  autocomplete="off"
                  bind:value={draft.note}
                  rows="2"></textarea></label
              >
            {/if}
            {#if promptState.drafts[question.id].choice === (question.options?.length ?? 0)}
              <label class={written}
                >Your answer
                <textarea
                  class={answerBox}
                  name={question.id + "-answer"}
                  autocomplete="off"
                  bind:value={promptState.drafts[question.id].text}
                  onpaste={(event) => {
                    if (questionDisabled) return;
                    const files = Array.from(event.clipboardData?.files ?? []).filter(
                      (file) => !file.type || file.type.startsWith("image/"),
                    );
                    if (!files.length) return;
                    if (!event.clipboardData?.types.includes("text/plain")) event.preventDefault();
                    trays[question.id]?.add(files);
                  }}
                  rows="2"></textarea>
              </label>
              <Attachments
                bind:this={trays[question.id]}
                bind:items={promptState.drafts[question.id].items}
                machine=""
                disabled={questionDisabled}
              />
            {/if}
            <label class={option}>
              <input
                class={radio}
                type="radio"
                name={question.id}
                bind:group={promptState.drafts[question.id].choice}
                value="skip"
                onfocus={() => {
                  focused[question.id] = question.options?.length ?? 0;
                  hovered[question.id] = undefined;
                }}
                onblur={() => (focused[question.id] = undefined)}
              />
              <span>Skip this question</span>
            </label>
          </fieldset>
          {#if !call.blocking}<Button
              type="submit"
              variant="primary"
              size="lg"
              disabled={questionDisabled}>Submit answer</Button
            >{/if}
        {/if}
      {/each}
      {#if call.blocking && !call.questions.some((q) => q.secret)}
        <Button type="submit" variant="primary" size="lg" {disabled}>Submit answers</Button>
      {/if}
    </form>
  {/if}
  {#if promptState.error}<Alert class="my-2" tabindex={-1}>{promptState.error}</Alert>
  {:else if unavailable}<p class={note} role="status">Not connected to the thread.</p>
  {:else if promptState.sending || promptState.accepted || activeDraft?.sending || activeDraft?.accepted}<p
      class={note}
      role="status"
    >
      {promptState.accepted || activeDraft?.accepted
        ? "Submitted. Waiting for the session…"
        : progress || "Sending…"}
    </p>{/if}
</article>

<style>
  article {
    container-type: inline-size;
    container-name: question;
  }
  .question-layout {
    display: grid;
    gap: 1rem;
  }
  @container question (min-width: 34rem) {
    .question-layout:has(.question-preview) {
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    }
  }
</style>
