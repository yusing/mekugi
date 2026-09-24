import type {SyntaxNode, Tree} from "@lezer/common";
import {
  children,
  LineMap,
  nodeText,
  parseSource,
  type LocatedEntry,
  type ValueType,
} from "./inspect_file_support.ts";

const jsonParser = () => (require("@lezer/json") as typeof import("@lezer/json")).parser;
const markdownParser = () => (require("@lezer/markdown") as typeof import("@lezer/markdown")).parser;

export function markdownTree(source: string): Tree {
  return parseSource(markdownParser(), source);
}

export function jsonTree(source: string): Tree {
  return parseSource(jsonParser(), source);
}

function markdownFrontmatter(
  source: string,
  lines: LineMap,
): {endOffset: number | null; entries: LocatedEntry[]; parseComplete: boolean} {
  if (lines.count === 0) {
    return {endOffset: null, entries: [], parseComplete: true};
  }
  const logicalLines = source.split(/\r\n|\r|\n/u);
  if (logicalLines.at(-1) === "" && /(?:\r\n|\r|\n)$/u.test(source)) {
    logicalLines.pop();
  }
  if (logicalLines[0].replace(/^\uFEFF/u, "") !== "---") {
    return {endOffset: null, entries: [], parseComplete: true};
  }
  const closingLine = logicalLines.indexOf("---", 1);
  if (closingLine < 1) {
    return {endOffset: null, entries: [], parseComplete: true};
  }

  const contentStart = lines.starts[1] ?? source.length;
  const contentEnd = lines.starts[closingLine] ?? source.length;
  const {parseDocument, isMap, isNode, isScalar} = require("yaml") as typeof import("yaml");
  const document = parseDocument(source.slice(contentStart, contentEnd));
  const entries: LocatedEntry[] = [];
  if (isMap(document.contents)) {
    for (const pair of document.contents.items) {
      const key = pair.key;
      if (!isScalar(key) || key.range === undefined || key.value === null || typeof key.value === "object") {
        continue;
      }
      const name = String(key.value);
      if (name === "") {
        continue;
      }
      const start = contentStart + key.range[0];
      const end = contentStart + Math.max(key.range[0], key.range[1] - 1);
      entries.push({
        entry: {
          kind: "frontmatter",
          name,
          line: lines.lineAt(start),
          line_end: lines.lineAt(end),
        },
        end: contentStart + (isNode(pair.value) && pair.value.range !== undefined
          ? pair.value.range[1] : key.range[1]),
        offset: start,
        order: entries.length,
      });
    }
  }
  const errorRows = new Set<number>();
  for (const error of document.errors) {
    const offset = Math.min(contentStart + error.pos[0], Math.max(0, source.length - 1));
    const line = lines.lineAt(offset);
    if (errorRows.has(line)) continue;
    errorRows.add(line);
    entries.push({entry: {kind: "parse_error", name: "syntax error", line, line_end: line},
      offset, end: offset, order: entries.length});
  }
  return {
    endOffset: lines.starts[closingLine + 1] ?? source.length,
    entries,
    parseComplete: document.errors.length === 0,
  };
}

export function markdownOutline(
  source: string,
  lines: LineMap,
  tree: Tree,
): {entries: LocatedEntry[]; parseComplete: boolean} {
  const frontmatter = markdownFrontmatter(source, lines);
  const output = [...frontmatter.entries];
  tree.iterate({
    enter(node) {
      if (frontmatter.endOffset !== null && node.from < frontmatter.endOffset) {
        return;
      }
      const match = node.name.match(/^ATXHeading([1-6])$/u);
      if (match === null) {
        return;
      }
      const raw = nodeText(source, node);
      const heading = raw
        .replace(/^ {0,3}#{1,6}(?:[ \t]+|$)/u, "")
        .replace(/\r$/u, "")
        .replace(/[ \t]+#+[ \t]*$/u, "")
        .trim();
      if (heading !== "") {
        output.push({
          entry: {
            kind: "heading",
            name: heading,
            level: Number(match[1]),
            ...lines.range(node),
          },
          end: node.to,
          offset: node.from,
          order: output.length,
        });
      }
    },
  });
  return {entries: output, parseComplete: frontmatter.parseComplete};
}

const jsonValueTypes: Record<string, ValueType> = {
  Object: "object",
  Array: "array",
  String: "string",
  Number: "number",
  True: "boolean",
  False: "boolean",
  Null: "null",
};

function decodePropertyName(source: string, node: SyntaxNode): string | null {
  try {
    const decoded = JSON.parse(nodeText(source, node));
    return typeof decoded === "string" ? decoded : null;
  } catch {
    return null;
  }
}

function containsParseError(node: SyntaxNode): boolean {
  if (node.type.isError) {
    return true;
  }
  return children(node).some(containsParseError);
}

function pointerSegment(value: string): string {
  return value.replaceAll("~", "~0").replaceAll("/", "~1");
}

export function jsonOutline(source: string, lines: LineMap, tree: Tree): LocatedEntry[] {
  const output: LocatedEntry[] = [];
  const addValue = (node: SyntaxNode, pointer: string): void => {
    const valueType = jsonValueTypes[node.name];
    if (valueType === undefined) {
      return;
    }
    output.push({
      entry: {kind: "json", pointer, value_type: valueType, ...lines.range(node)},
      end: node.to,
      offset: node.from,
      order: output.length,
    });

    if (node.name === "Object") {
      for (const property of children(node).filter((child) => child.name === "Property")) {
        const parts = children(property);
        const nameIndex = parts.findIndex((child) => child.name === "PropertyName");
        const colonIndex = parts.findIndex((child) => child.name === ":");
        const valueIndex = parts.findIndex(
          (child, index) => index > colonIndex && jsonValueTypes[child.name] !== undefined,
        );
        if (
          nameIndex < 0
          || colonIndex <= nameIndex
          || valueIndex <= colonIndex
          || parts.slice(nameIndex + 1, valueIndex).some(containsParseError)
        ) {
          continue;
        }
        const name = decodePropertyName(source, parts[nameIndex]);
        if (name !== null) {
          addValue(parts[valueIndex], `${pointer}/${pointerSegment(name)}`);
        }
      }
    } else if (node.name === "Array") {
      let index = 0;
      for (const child of children(node)) {
        if (child.name === ",") {
          index += 1;
        } else if (jsonValueTypes[child.name] !== undefined) {
          addValue(child, `${pointer}/${index}`);
        }
      }
    }
  };

  const root = children(tree.topNode).find((child) => jsonValueTypes[child.name] !== undefined);
  if (root !== undefined) {
    addValue(root, "");
  }
  return output;
}
