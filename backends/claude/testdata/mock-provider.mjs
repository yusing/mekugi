import { createServer } from "node:http";
import { randomUUID } from "node:crypto";
import { createInterface } from "node:readline";
import { pathToFileURL } from "node:url";

// The native SDK acceptance and the application harness share the same local
// Anthropic response framing. Every request stays on the loopback listener.
export function createMockProvider(onMessage) {
  const requests = [];
  return createServer(async (request, response) => {
    response.setHeader("content-type", "application/json");
    if (request.url === "/_fixture/requests") { response.end(JSON.stringify(requests)); return; }
    let data = "";
    for await (const chunk of request) data += chunk;
    const body = data ? JSON.parse(data) : {};
    if (request.url?.includes("count_tokens")) { response.end(JSON.stringify({ input_tokens: 20 })); return; }
    if (!request.url?.startsWith("/v1/messages")) { response.end("{}"); return; }
    requests.push(body);
    const scripted = await onMessage(body);
    if (scripted.status) {
      response.statusCode = scripted.status;
      response.setHeader("x-should-retry", "false");
      response.end(JSON.stringify({ type: "error", error: scripted.error }));
      return;
    }
    const content = scripted.content;
    const message = { id: `msg_${randomUUID()}`, type: "message", role: "assistant", model: body.model, content,
      stop_reason: scripted.stop_reason ?? (content.some((block) => block.type === "tool_use") ? "tool_use" : "end_turn"),
      stop_sequence: null, usage: { input_tokens: 20, output_tokens: 5 } };
    if (!body.stream) { response.end(JSON.stringify(message)); return; }
    response.setHeader("content-type", "text/event-stream");
    const event = (type, payload) => response.write(`event: ${type}\ndata: ${JSON.stringify({ type, ...payload })}\n\n`);
    event("message_start", { message: { ...message, content: [], stop_reason: null } });
    for (const [index, block] of content.entries()) {
      if (scripted.beforeBlock) await scripted.beforeBlock(index);
      event("content_block_start", { index, content_block: block.type === "tool_use" ? { ...block, input: {} } : { type: "text", text: "" } });
      event("content_block_delta", { index, delta: block.type === "tool_use" ? { type: "input_json_delta", partial_json: JSON.stringify(block.input) } : { type: "text_delta", text: block.text } });
      event("content_block_stop", { index });
    }
    event("message_delta", { delta: { stop_reason: message.stop_reason, stop_sequence: null }, usage: { output_tokens: 5 } });
    event("message_stop", {});
    response.end();
  });
}

// CLI: first stdin line is {responses:[{content:[...]}, ...]}. The single stdout
// line is {port}. Keep stdin open until the application fixture has shut down.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const lines = createInterface({ input: process.stdin });
  let provider;
  lines.once("line", (line) => {
    const { responses, compaction } = JSON.parse(line);
    provider = createMockProvider((body) => {
      if (compaction && JSON.stringify(body.messages.at(-1)).includes("CRITICAL: Respond with TEXT ONLY.")) return compaction;
      return responses.shift() ?? {
      status: 400, error: { type: "invalid_request_error", message: "Local fixture response plan exhausted" },
    }; });
    provider.listen(0, "127.0.0.1", () => process.stdout.write(`${JSON.stringify({ port: provider.address().port })}\n`));
  });
  const stop = () => { provider?.closeAllConnections(); provider?.close(); lines.close(); process.stdin.destroy(); };
  lines.on("close", () => { provider?.closeAllConnections(); provider?.close(); });
  process.once("SIGTERM", stop);
}
