import type { HighlighterCore } from "shiki/core";
import type { Value } from "./protocol";
import type { DiffRow } from "./diff";

// Syntax highlighting for code the transcript shows. Shiki runs on its
// JavaScript regex engine; it and each grammar load the first time a block
// needs them, so a page loads only what it shows. Anything unknown, too large
// or failing to load stays plain text.

type Grammar = () => Promise<{ default: Parameters<HighlighterCore["loadLanguage"]>[0] }>;

const grammars: Record<string, Grammar> = {
  shellscript: () => import("shiki/langs/shellscript.mjs"),
  fish: () => import("shiki/langs/fish.mjs"),
  powershell: () => import("shiki/langs/powershell.mjs"),
  json: () => import("shiki/langs/json.mjs"),
  jsonc: () => import("shiki/langs/jsonc.mjs"),
  diff: () => import("shiki/langs/diff.mjs"),
  go: () => import("shiki/langs/go.mjs"),
  typescript: () => import("shiki/langs/typescript.mjs"),
  tsx: () => import("shiki/langs/tsx.mjs"),
  javascript: () => import("shiki/langs/javascript.mjs"),
  jsx: () => import("shiki/langs/jsx.mjs"),
  python: () => import("shiki/langs/python.mjs"),
  rust: () => import("shiki/langs/rust.mjs"),
  markdown: () => import("shiki/langs/markdown.mjs"),
  yaml: () => import("shiki/langs/yaml.mjs"),
  toml: () => import("shiki/langs/toml.mjs"),
  ini: () => import("shiki/langs/ini.mjs"),
  xml: () => import("shiki/langs/xml.mjs"),
  css: () => import("shiki/langs/css.mjs"),
  scss: () => import("shiki/langs/scss.mjs"),
  html: () => import("shiki/langs/html.mjs"),
  svelte: () => import("shiki/langs/svelte.mjs"),
  vue: () => import("shiki/langs/vue.mjs"),
  sql: () => import("shiki/langs/sql.mjs"),
  graphql: () => import("shiki/langs/graphql.mjs"),
  proto: () => import("shiki/langs/proto.mjs"),
  c: () => import("shiki/langs/c.mjs"),
  cpp: () => import("shiki/langs/cpp.mjs"),
  java: () => import("shiki/langs/java.mjs"),
  kotlin: () => import("shiki/langs/kotlin.mjs"),
  swift: () => import("shiki/langs/swift.mjs"),
  ruby: () => import("shiki/langs/ruby.mjs"),
  php: () => import("shiki/langs/php.mjs"),
  lua: () => import("shiki/langs/lua.mjs"),
  zig: () => import("shiki/langs/zig.mjs"),
  nix: () => import("shiki/langs/nix.mjs"),
  docker: () => import("shiki/langs/docker.mjs"),
  make: () => import("shiki/langs/make.mjs"),
};

// Fence names and file extensions that name a grammar above.
const aliases: Record<string, string> = {
  bash: "shellscript",
  sh: "shellscript",
  shell: "shellscript",
  zsh: "shellscript",
  console: "shellscript",
  ps1: "powershell",
  patch: "diff",
  golang: "go",
  ts: "typescript",
  mts: "typescript",
  cts: "typescript",
  js: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  py: "python",
  rs: "rust",
  md: "markdown",
  yml: "yaml",
  htm: "html",
  gql: "graphql",
  h: "c",
  cc: "cpp",
  cxx: "cpp",
  hpp: "cpp",
  "c++": "cpp",
  kt: "kotlin",
  rb: "ruby",
  dockerfile: "docker",
  makefile: "make",
  mk: "make",
};

export function language(name: string | undefined): string | undefined {
  const key = name?.trim().toLowerCase();
  if (!key) return undefined;
  const id = aliases[key] ?? key;
  return id in grammars ? id : undefined;
}

// The grammar for a file path: by extension, or by name for Makefile and Dockerfile.
export function pathLanguage(path: string): string | undefined {
  const base = path.slice(path.lastIndexOf("/") + 1);
  const dot = base.lastIndexOf(".");
  return language(dot > 0 ? base.slice(dot + 1) : base);
}

// The grammar of a row's retained output, from what Activity already classified:
// a read's output is its last file, a diff is a diff, and complete output that
// parses as JSON is JSON. A compound command's result row under its steps
// carries its last step's output.
export function outputLanguage(values: Value[], index: number): string | undefined {
  const value = values[index];
  const step = value.batchExit && !value.code ? values[index - 1] : value;
  const read = step?.reads?.at(-1);
  if (read) return pathLanguage(read.path);
  if (step?.verb === "Diff") return "diff";
  const text = value.tail?.join("\n").trim() ?? "";
  if (!value.tailOmitted && /^[[{]/.test(text)) {
    try {
      JSON.parse(text);
      return "json";
    } catch {
      return undefined;
    }
  }
  return undefined;
}

const limit = 64 * 1024;
let core: Promise<HighlighterCore> | undefined;
const loading = new Map<string, Promise<unknown>>();

export interface SyntaxToken {
  text: string;
  style?: string;
}

// Whole source sides retain multiline grammar state. The diff painter owns
// row geometry and changed-word backgrounds, independently of these colors.
export async function highlightLines(
  code: string,
  lang: string | undefined,
): Promise<SyntaxToken[][] | undefined> {
  const id = language(lang);
  if (code.length > limit || !id) return undefined;
  try {
    const h = await grammarHighlighter(id);
    return h
      .codeToTokens(code, {
        lang: id,
        themes: { light: "github-light-default", dark: "github-dark-default" },
        defaultColor: false,
      })
      .tokens.map((line) =>
        line.map((token) => ({
          text: token.content,
          style: Object.entries(token.htmlStyle ?? {})
            .map(([key, value]) => `${key}:${value}`)
            .join(";"),
        })),
      );
  } catch {
    return undefined;
  }
}

function highlighter() {
  core ??= Promise.all([import("shiki/core"), import("shiki/engine/javascript")]).then(
    ([{ createHighlighterCore }, { createJavaScriptRegexEngine }]) =>
      createHighlighterCore({
        themes: [
          import("shiki/themes/github-light-default.mjs"),
          import("shiki/themes/github-dark-default.mjs"),
        ],
        engine: createJavaScriptRegexEngine({ forgiving: true }),
      }),
  );
  core.catch(() => (core = undefined));
  return core;
}

async function grammarHighlighter(id: string) {
  const h = await highlighter();
  let grammar = loading.get(id);
  if (!grammar) {
    grammar = grammars[id]().then((m) => h.loadLanguage(m.default));
    loading.set(id, grammar);
    grammar.catch(() => loading.delete(id));
  }
  await grammar;
  return h;
}

// Change statistics as `git diff --numstat` and `mchanges` print them: an
// optional status, the added and removed line counts, and a path, separated
// by tabs.
const stat = /^(?:([A-Z?!]\d*)\t)?(\d+|-)\t(\d+|-)\t(.+)$/;
const escapeHTML = (text: string) =>
  text.replace(/[&<>"]/g, (c) => `&${{ "&": "amp", "<": "lt", ">": "gt", '"': "quot" }[c]};`);

const count = (sign: string, n: string, pad: number) =>
  `<span class="${n === "0" || n === "-" ? "text-muted" : sign === "+" ? "text-positive" : "text-negative"}">${(n === "-" ? n : sign + n).padStart(pad)}</span>`;

// Text in no known grammar shows its change statistics as a diff stat: aligned
// counts in the diff colours. Other lines stay as they are.
function diffstat(code: string): string | undefined {
  const lines = code.split("\n");
  const rows = lines.map((line) => stat.exec(line));
  if (!rows.some(Boolean)) return undefined;
  const width = (group: number) => Math.max(...rows.map((row) => row?.[group]?.length ?? 0));
  const [status, added, removed] = [width(1), width(2) + 1, width(3) + 1];
  return lines
    .map((line, i) => {
      const row = rows[i];
      if (!row) return escapeHTML(line);
      const lead = status
        ? `<span class="text-muted">${(row[1] ?? "").padEnd(status)}</span>  `
        : "";
      return `${lead}${count("+", row[2], added)} ${count("−", row[3], removed)}  ${escapeHTML(row[4])}`;
    })
    .join("\n");
}

// Highlighted inline HTML for code (token spans and <br> between lines), or
// undefined when it stays plain. Shiki escapes the code's text.
export async function highlight(code: string, lang: string | undefined) {
  if (code.length > limit) return undefined;
  const id = language(lang);
  if (!id) return diffstat(code);
  try {
    const h = await grammarHighlighter(id);
    return h.codeToHtml(code, {
      lang: id,
      themes: { light: "github-light-default", dark: "github-dark-default" },
      defaultColor: false,
      structure: "inline",
    });
  } catch {
    return undefined;
  }
}

// Lets paths in HTML wrap after a slash, in text outside markup.
export const breakable = (html: string) =>
  html
    .split(/(<[^>]*>)/)
    .map((text, i) => (i % 2 ? text : text.replaceAll("/", "/<wbr>")))
    .join("");

// Highlights rendered code elements inside node, such as Markdown fences:
// lang names each element's grammar. An element whose text changed meanwhile
// is left alone, since its owner has re-rendered it.
export function highlightWithin(
  node: HTMLElement,
  selector: string,
  lang: (element: Element) => string | undefined,
  wrap = false,
) {
  // After the current update, so the elements are the ones just rendered.
  queueMicrotask(() => {
    for (const element of node.querySelectorAll(selector)) {
      const text = element.textContent ?? "";
      void highlight(text, lang(element)).then((html) => {
        if (html && element.isConnected && element.textContent === text)
          element.innerHTML = wrap ? breakable(html) : html;
      });
    }
  });
}

// Keep each hunk's before/after grammar state independent. Gaps are not source,
// and a deleted comment/string delimiter must not affect the added side.
export async function diffSyntax(
  rows: DiffRow[],
  beforePath: string,
  afterPath: string,
): Promise<Map<number, SyntaxToken[]>> {
  const syntax = new Map<number, SyntaxToken[]>();
  if (rows.reduce((n, row) => n + row.text.length + 1, 0) > limit) return syntax;
  for (let start = 0; start < rows.length;) {
    let end = start + 1;
    while (end < rows.length && rows[end].kind !== "hunk") end++;
    for (const [side, path] of [beforePath, afterPath].entries()) {
      const indexes: number[] = [];
      for (let i = start; i < end; i++)
        if (rows[i].kind === "context" || rows[i].kind === (side ? "add" : "remove"))
          indexes.push(i);
      if (!indexes.length) continue;
      const source = indexes.map((index) => rows[index].text).join("\n");
      const lines = await highlightLines(source, pathLanguage(path));
      if (lines)
        indexes.forEach((index, i) => {
          // Shiki normalizes CRLF. Never let decoration replace source content.
          if (lines[i]?.map((token) => token.text).join("") === rows[index].text)
            syntax.set(index, lines[i]);
        });
    }
    start = end;
  }
  return syntax;
}
