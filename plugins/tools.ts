import type {Plugin} from "../internal/router/toolplugin/plugin.d.ts";
import {createMCatTool} from "./mcat.ts";
import {createMSymbolTool} from "./msymbol.ts";
import {createInspectFileTool} from "./inspect_file.ts";
import {createMReadTool} from "./read_output.ts";
import {createMRunTool} from "./mrun.ts";
import {createMChangesTool} from "./mchanges.ts";

const readerPath = `(?:"(?:\\\\(?:["\\\\/bfnrt]|u[0-9A-Fa-f]{4})|[^\\x00-\\x1F"\\\\]|\\t)*"|[^\\x00-\\x20"]+)`;
const readSpec = `${readerPath}(?: (?:0|[1-9][0-9]*)[:-][1-9][0-9]*)*`;
const mcatRegex = `\\A(?:(?:-n |--max-tokens[ =])[1-9][0-9]* |--tail )*${readSpec}(?: ${readSpec})*(?: (?:-n |--max-tokens[ =])[1-9][0-9]*| --tail)*\\z`;
const inspectFileRegex = `\\A(?:(?:--max-tokens[ =][1-9][0-9]*|--json) )*${readerPath}(?: (?:${readerPath}|--max-tokens[ =][1-9][0-9]*|--json))*\\z`;

const msymbolRegex = `\\A(?:(?:--workspace ${readerPath}|--max-tokens[ =][1-9][0-9]*) )*(?:def|refs) ${readerPath} [1-9][0-9]* [^\\x00-\\x20]+(?: [1-9][0-9]*)?(?: (?:--workspace ${readerPath}|--max-tokens[ =][1-9][0-9]*))*\\z`;

const plugin: Plugin = {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "builtin.frontends",
  tools: [
    createMCatTool(mcatRegex),
    createMSymbolTool(msymbolRegex),
    createInspectFileTool(inspectFileRegex),
    createMReadTool(),
    createMRunTool(),
    createMChangesTool(),
  ],
};

export default plugin;
