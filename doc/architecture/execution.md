# Stock execution boundary

## CTR-EXECUTION-001 — Host-owned editing and process lifecycle

Codex owns stock `apply_patch`, `exec_command`, Code Mode JavaScript, permissions,
sandboxing, processes, and yielded-session continuation. Mekugi's request
projection only adds journal guidance to the Code Mode tool description. It
does not replace the execution catalog or create a carrier for an unchanged
stock call.
The credential-gated explore filter (`REQ-EXPLORE-FILTER-001`) is the only projection that
rewrites a completed stock result's model-visible text, and it retains the
complete original through a managed read reference.

The response observer reads completed stock arguments once, captures bounded
pre-edit source for named paths, and later reconciles the result with the
workspace outcome. It does not authorize, parse for execution, or replay an
edit. The command classifier derives a write scope from Bash syntax and
literal Code Mode calls only; it never evaluates a program, expands a
variable, or consults the router's environment. The interpreter is the call's
own shell or the session shell named in the request. Destinations that depend
on the filesystem, such as a copy into an existing directory, resolve when the
observer captures the scope, which keeps every reading an earlier statement
could select. Result state is read from the host's own result
header, and a yielded session or cell is matched to its continuation result
by the host-issued session or cell ID. The replay store owns durable change IDs and review evidence. The live
viewer owns provisional display only. Neither can call the host tool again.

The command observer owns stateless post-result sweeps and a process-local writer
window registry. The registry relates concurrent calls, merges response siblings,
and labels background sessions without owning their continuation. Durable captures
survive restart; registry tags do not. Review origin is retained on each review
file, and consumers group tool-managed evidence without changing stored content.
Interpreter providers share syntax-derived path resolution and the shell classifier
for literal subprocesses. Producer queries use a bounded argv-based runner only
after their family validates read-only arguments; no shell executes query text.
VCS and fixer providers use the same registry and deadline. The optional last-seen
LRU is process-local and namespace-isolated. Only persisted observations populate
it; its labeled historical comparisons never replace captured call baselines.
Pre-call filesystem capture runs in a bounded worker pool outside the forwarding
wait. At the 500 ms hold deadline, unfinished capture is discarded and recorded as
unavailable; late reads can never become a pre-call baseline. Stalled filesystem
syscalls may retain a worker until the OS returns, but cannot stall forwarding or
create unbounded workers.
The writer-window registry cancels running previews on terminal observation,
background transition, or eviction. The live broker's lifetime cancels polling
on shutdown. Polling reads only the retained scope and publishes provisional
cards; the result reconciler remains the sole command-evidence owner.

The plugin registry owns one immutable authenticated snapshot and the private
PATH directory. Its frontend workers implement bounded readers and output
retention inside Codex-started processes; they do not own Codex command
sessions or continuation handles. Each configured frontend runs its declared
executor under the same worker authentication.
