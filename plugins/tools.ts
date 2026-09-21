import type {Plugin, Tool} from "../internal/router/toolplugin/plugin.d.ts";
import {createHGrepTool} from "./hgrep.ts";
import {createMCatTool} from "./mcat.ts";
import {createHSymbolTool} from "./hsymbol.ts";
import {createInspectFileTool, inspectFileDescription} from "./inspect_file.ts";
import {shellTool} from "./shell.mjs";

const verifiedRowLimitDescription = "An incomplete token-limited result retains complete rows, writes stderr, and exits nonzero.";
const readerOptionsDescription = "--max-tokens N sets a strict ceiling (1–15500; default 4000); options may surround operands. --preview-bytes N emits explicit JSON prefixes with full-row identities and omitted-byte counts (1–65536).";
const mcatDescription = `Read one or more UTF-8 files or inclusive logical-line ranges as raw rows without line or hash prefixes. Usage: \`mcat [-n N] [--max-tokens N] [--tail] PATH [START:END] [PATH [START:END] ...]\`. --max-tokens N sets a strict total ceiling (1–15500; default 4000). -n N selects complete lines without tokenization unless --max-tokens is also supplied. --tail requires -n or --max-tokens and selects final rows in source order. Multiple files support --max-tokens and provide per-file mread recovery. ${verifiedRowLimitDescription}`;

const readerPath = `(?:"(?:\\\\(?:["\\\\/bfnrt]|u[0-9A-Fa-f]{4})|[^\\x00-\\x1F"\\\\]|\\t)*"|[^\\x00-\\x20"]+)`;
const readSpec = `${readerPath}(?: (?:0|[1-9][0-9]*):[1-9][0-9]*)?`;
const mcatRegex = `\\A(?:(?:-n|--max-tokens) [1-9][0-9]* |--tail )*${readSpec}(?: ${readSpec})*(?: (?:-n|--max-tokens) [1-9][0-9]*| --tail)*\\z`;
const inspectFileRegex = `\\A(?:--max-tokens [1-9][0-9]* )*${readerPath}(?: --max-tokens [1-9][0-9]*)*\\z`;

const hgrepDescription = `Search files with supported ripgrep arguments and emit verified complete rows as \`"PATH":LINE:HASH TEXT\`. ${readerOptionsDescription} ${verifiedRowLimitDescription}`;

const hgrepPart = `(?:'[^'\\r\\n]*'|"(?:\\\\[^\\r\\n]|[^"\\\\\\r\\n])*"|(?:\\\\[^\\r\\n]|[^\\s'"\\\\])+)`;
const hgrepRegex = `\\A[ \\t]*${hgrepPart}+(?:[ \\t]+${hgrepPart}+)*[ \\t]*\\z`;

const hsymbolDescription = `Resolve one current or hash-verified Go, JavaScript, TypeScript, JSON, or Python symbol and emit complete rows as \`"PATH":LINE:HASH TEXT\`. Usage: \`hsymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH (LINE|LINE:HASH) SYMBOL [N]\`. A plain line selects the current snapshot; a hash additionally checks previously read content. ROOT sets resolver scope and relative paths without changing shell state. N selects an exact language-token occurrence. --max-tokens follows the shared 4000-token default and strict 1–15500 ceiling. Stale rows, ambiguous selectors, unavailable language servers, and definitions without an editable workspace location fail without stdout rows. ${verifiedRowLimitDescription}`;
const hsymbolRegex = `\\A(?:(?:--workspace ${readerPath}|--max-tokens [1-9][0-9]*) )*(?:def|refs) ${readerPath} [1-9][0-9]*(?::[0-9a-f]{4})? [^\\x00-\\x20]+(?: [1-9][0-9]*)?(?: (?:--workspace ${readerPath}|--max-tokens [1-9][0-9]*))*\\z`;

type BuiltinPlugin = Omit<Plugin, "tools"> & {
  tools: [Tool<string[]>, Tool<string[]>, Tool<string[]>, Tool<string[]>, typeof shellTool];
};

const plugin: BuiltinPlugin = {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "builtin.shell",
  tools: [
    createMCatTool(mcatDescription, mcatRegex),
    createHGrepTool(hgrepDescription, hgrepRegex),
    createHSymbolTool(hsymbolDescription, hsymbolRegex),
    createInspectFileTool(inspectFileDescription, inspectFileRegex),
    shellTool,
  ],
};

export default plugin;
