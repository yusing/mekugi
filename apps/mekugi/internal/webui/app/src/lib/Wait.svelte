<script lang="ts">
  import AgentName from "./AgentName.svelte";

  // A collaboration wait as its text reads in the terminal: what is waiting,
  // the agents it waits for with their status, the optional timeout and why
  // it finished. Names are plain inside another control.
  let {
    text,
    targets,
    timeout,
    reason,
    owner,
    plain = false,
  }: {
    text: string;
    targets: { name: string; status?: string }[];
    timeout?: string;
    reason?: string;
    owner: string;
    plain?: boolean;
  } = $props();
</script>

<span>{text}</span>{#each targets as target, i (i)}{i ? ", " : " "}<span class="whitespace-nowrap"
    ><AgentName name={target.name} current={owner} {plain} />{#if target.status}<span
        class="text-muted">: {target.status}</span
      >{/if}</span
  >{/each}{#if timeout}{" "}<span class="whitespace-nowrap text-muted">(timeout {timeout})</span
  >{/if}{#if reason}{`: ${reason}`}{/if}
