# Capture-owned metrics

## CTR-METRICS-001 — Capture-owned metrics

The in-process capturer is the sole owner of request correlation, transport measurement,
provider usage, cache attribution, representation differences, tool-shape accounting,
delivery accounting, capture health, durable sanitized records, and metrics snapshots.
HTTP and WebSocket observers share request-scoped correlation without sending private
headers or changing byte streams, retries, cancellation, or response ownership.

Raw bodies exist only while a boundary is measured. Durable capture retains bounded sizes,
token estimates, status, timing, provider usage, tool identities, and allowlisted outcomes,
but not credentials, prompts, instructions, arguments, command output, response text,
patches, or reports. Missing, truncated, evicted, malformed, or old evidence is marked
unavailable or incomplete, never inferred as zero or success. Provider usage remains
authoritative over local token estimates.

Production feature owners supply only validated observation facts at existing seams. They
do not keep synthetic baselines, metric callbacks, parallel histories, or dashboard-owned
calculations. Terminal parsing provides usage;
private reader execution provides opt-in AX events. Capturer calculations are reused by the
dashboard, offline aggregation, and benchmark validation.

Debug artifacts are a separate router-owned, explicitly requested surface. They may combine
sanitized capture exports with instruction snapshots, lifecycle outcomes, and feature events,
but cannot affect runtime success. Failure to initialize a requested artifact fails startup;
later observation failures remain auxiliary. AX journals and private replay corpora also
remain outside sanitized transport capture.

WebSocket capture observes actual sent and received messages at transport ownership
boundaries. The session and connection owners retain incremental history, steering,
fallback, leases, and cleanup. Capturer state never determines connection reuse, provider
retry, response completion, or delivery.
