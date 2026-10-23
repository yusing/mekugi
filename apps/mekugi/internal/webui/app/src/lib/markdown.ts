import MarkdownIt from "markdown-it";

// Recorded text is untrusted: raw HTML stays escaped, and only web links
// navigate. Workspace file links render as plain paths, with the target as a
// title, because the browser cannot open them.
const md = new MarkdownIt({ html: false, linkify: true, breaks: false });

const web = /^https?:\/\//i;
const defaultLinkOpen =
  md.renderer.rules.link_open ??
  ((tokens, i, options, _env, self) => self.renderToken(tokens, i, options));

md.renderer.rules.link_open = (tokens, i, options, env, self) => {
  const href = String(tokens[i].attrGet("href") ?? "");
  if (!web.test(href)) {
    tokens[i].attrs = [];
    tokens[i].tag = "span";
    tokens[i].attrSet("class", "file-link");
    tokens[i].attrSet("title", href);
    const close = tokens.slice(i + 1).find((t) => t.type === "link_close");
    if (close) close.tag = "span";
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
