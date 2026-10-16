# Session control

The application owns app-server request issuance and response correlation here.
Codex owns request identities, transport, execution, permissions, and process
lifetime. Live presentation supplies intent and consumes correlated results;
it does not issue operations directly through the backend client.

One typed ledger retains each successfully issued request and its consumer
context. Responses consume that identity once. Unknown, duplicate, and retired
responses cannot settle a newer operation. Notifications and answers to host
requests preserve their original method, payload, and identity without becoming
outbound requests. Transport failure is returned to the caller without retry.

Confirmed session replacement retires Main's correlation scope. Rejected
replacement leaves it intact. Independent side-conversation cleanup and title
saves retain their owners across Main's replacement. Forgetting observation
never cancels host work or claims completion.

Submission, steering, shell, settings, and conversation presentation still live
in the router integration. Their extraction is incomplete; the request ledger
does not substitute for their lifecycle records or payload owners.
