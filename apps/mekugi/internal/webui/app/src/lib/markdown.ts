import MarkdownIt from "markdown-it";

// Recorded text is untrusted. Local links use the machine's shared file
// interaction instead of navigating the browser filesystem.
const md = new MarkdownIt({ html: false, linkify: true, breaks: false });

const web = /^(?:https?:\/\/|mailto:)/i;
const local = (href: string) =>
  !/^[#?]/.test(href) &&
  (!/^[a-z][a-z\d+.-]*:/i.test(href) || href.startsWith("/") || href.startsWith("file:"));
const validateLink = md.validateLink.bind(md);
md.validateLink = (href) => href.startsWith("file:") || validateLink(href);
const defaultLinkOpen =
  md.renderer.rules.link_open ??
  ((tokens, i, options, _env, self) => self.renderToken(tokens, i, options));

md.renderer.rules.link_open = (tokens, i, options, env, self) => {
  const href = String(tokens[i].attrGet("href") ?? "");
  if (!web.test(href)) {
    tokens[i].attrs = [];
    tokens[i].tag = local(href) ? "a" : "span";
    if (local(href)) {
      tokens[i].attrSet("href", "#");
      let path = href;
      if (!href.startsWith("file:")) {
        try {
          path = decodeURIComponent(href);
        } catch {
          /* Preserve a literal malformed escape. */
        }
      }
      tokens[i].attrSet("data-file-link", path);
    }
    tokens[i].attrSet("class", "file-link");
    tokens[i].attrSet("title", href);
    const close = tokens.slice(i + 1).find((t) => t.type === "link_close");
    if (close) close.tag = tokens[i].tag;
    return self.renderToken(tokens, i, options);
  }
  tokens[i].attrSet("target", "_blank");
  tokens[i].attrSet("rel", "noopener noreferrer");
  return defaultLinkOpen(tokens, i, options, env, self);
};

// Images become links, so opening a recording never fetches from the hosts it names.
md.renderer.rules.image = (tokens, i) => {
  const src = String(tokens[i].attrGet("src") ?? "");
  const alt = md.utils.escapeHtml(tokens[i].content || src);
  return web.test(src)
    ? `<a href="${md.utils.escapeHtml(src)}" target="_blank" rel="noopener noreferrer">Image: ${alt}</a>`
    : `<span class="file-link" title="${md.utils.escapeHtml(src)}">Image: ${alt}</span>`;
};

export const block = (text: string) => md.render(text);
export const inline = (text: string) => md.renderInline(text);

// Classified labels already quote operands as Markdown code spans. Keep their
// boundaries rather than splitting punctuation inside commands or search patterns.
export function toolLabel(text: string, verb: string): string {
  const tokens = md.parseInline(text, {});
  for (const token of tokens) {
    const children = token.children ?? [];
    let operand = 0;
    for (let i = 0; i < children.length; i++) {
      const child = children[i];
      if (child.type === "code_inline") {
        child.attrSet("class", verb === "Search" && operand === 0 ? "text-accent" : "tool-operand");
        operand++;
      } else if (
        child.type === "text" &&
        child.content === ", " &&
        children[i - 1]?.type === "code_inline" &&
        children[i + 1]?.type === "code_inline"
      )
        child.content = " ";
    }
  }
  return md.renderer.render(tokens, md.options, {});
}

// Only published byte spans are markers. Identical unbound text stays ordinary.
export function markedBlock(
  text: string,
  spans: NonNullable<import("./protocol").Entry["spans"]>,
): string {
  const bytes = new TextEncoder().encode(text);
  const decoder = new TextDecoder();
  let prefix = "[mekugi-bound:";
  while (text.includes(prefix)) prefix += ":";
  const markers: { key: string; html: string }[] = [];
  const colours = ["text-marker-image", "text-positive", "text-kind-write", "text-accent"];
  let at = 0,
    source = "";
  for (const span of spans) {
    if (
      span.Start < at ||
      span.End <= span.Start ||
      span.End > bytes.length ||
      (bytes[span.Start] & 0xc0) === 0x80 ||
      (span.End < bytes.length && (bytes[span.End] & 0xc0) === 0x80)
    )
      continue;
    const colour = colours[span.Kind];
    if (!colour) continue;
    const key = `${prefix}${markers.length}]`;
    markers.push({
      key,
      html: `<span class="font-semibold whitespace-nowrap ${colour}">${md.utils.escapeHtml(decoder.decode(bytes.slice(span.Start, span.End)))}</span>`,
    });
    source += decoder.decode(bytes.slice(at, span.Start)) + key;
    at = span.End;
  }
  source += decoder.decode(bytes.slice(at));
  return md
    .render(source)
    .split(/(<[^>]*>)/)
    .map((part, i) => {
      if (i % 2) return part;
      for (const marker of markers) part = part.replaceAll(marker.key, marker.html);
      return part;
    })
    .join("");
}
