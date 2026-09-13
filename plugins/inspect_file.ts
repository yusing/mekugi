import {readFile, stat} from "node:fs/promises";
import path from "node:path";

import type {Tree, Parser, SyntaxNode} from "@lezer/common";
// Load parser tables only for a source kind that actually needs them. Ordinary
// readers and shell translation share this bundle but do not parse source trees.
const goParser = () => (require("@lezer/go") as typeof import("@lezer/go")).parser;
const javascriptParser = (dialect = "") =>
  (require("@lezer/javascript") as typeof import("@lezer/javascript")).parser.configure({dialect});
const jsonParser = () => (require("@lezer/json") as typeof import("@lezer/json")).parser;
const markdownParser = () => (require("@lezer/markdown") as typeof import("@lezer/markdown")).parser;
const pythonParser = () => (require("@lezer/python") as typeof import("@lezer/python")).parser;

import type {Tool} from "../internal/router/toolplugin/plugin.d.ts";
import {
  classifySourcePath,
  decodeGoStringLiteral,
  hashLine,
  lineCount as sharedLineCount,
} from "mekugi:core/v1";
import {
  byteLength,
  decodeUTF8,
  createExecutorTool,
  errorText,
  stripOptionalFinalNewline,
} from "./common.ts";
import {decodeJavaScriptStringLiteral} from "./javascript_string.ts";
import {inspectFileShapeSchemaJSON} from "./inspect_file_schema.ts";

const OUTPUT_BYTES = 64 * 1024;

type ErrorCode =
  | "usage"
  | "not_found"
  | "not_regular"
  | "not_utf8"
  | "read"
  | "parse"
  | "output_limit";

type Language = "go" | "javascript" | "typescript" | "python";
type FileKind = "code" | "markdown" | "json" | "none";
type ValueType = "object" | "array" | "string" | "number" | "boolean" | "null";

type CodeEntry = {
  kind: "import" | "constant" | "variable" | "type" | "class" | "function";
  name: string;
  line: number;
  line_end: number;
};

type MethodEntry = {
  kind: "method";
  name: string;
  receiver: string;
  line: number;
  line_end: number;
};

type HeadingEntry = {
  kind: "heading";
  name: string;
  level: number;
  line: number;
  line_end: number;
};

type FrontmatterEntry = {
  kind: "frontmatter";
  name: string;
  line: number;
  line_end: number;
};

type JSONEntry = {
  kind: "json";
  pointer: string;
  value_type: ValueType;
  line: number;
  line_end: number;
};

type OutlineEntry = CodeEntry | MethodEntry | HeadingEntry | FrontmatterEntry | JSONEntry;
type PublicOutlineEntry = Omit<OutlineEntry, "line" | "line_end"> & {
  line: string;
  line_end: string;
};
type LocatedEntry = {
  entry: OutlineEntry;
  end: number;
  offset: number;
  order: number;
  nameFrom?: number;
  nameTo?: number;
};

export type SourceFormat = {
  kind: Exclude<FileKind, "none">;
  language: Language | null;
  jsx?: true;
};

const goIdentifierNodes = new Set(["DefName", "FieldName", "PackageName", "TypeName", "VariableName"]);
const javascriptIdentifierNodes = new Set([
  "JSXIdentifier",
  "PrivatePropertyName",
  "PropertyDefinition",
  "PropertyName",
  "TypeDefinition",
  "TypeName",
  "VariableDefinition",
  "VariableName",
]);
const pythonIdentifierNodes = new Set(["PropertyName", "VariableName"]);

const goDeclarationProjections: Record<string, {node: string; name: string; kind: CodeEntry["kind"]}> = {
  ConstDecl: {node: "ConstSpec", name: "DefName", kind: "constant"},
  VarDecl: {node: "VarSpec", name: "DefName", kind: "variable"},
  TypeDecl: {node: "TypeSpec", name: "DefName", kind: "type"},
};

const javascriptDeclarationProjections: Record<string, {name: string; kind: CodeEntry["kind"]}> = {
  AmbientFunctionDeclaration: {name: "VariableDefinition", kind: "function"},
  FunctionDeclaration: {name: "VariableDefinition", kind: "function"},
  ClassDeclaration: {name: "VariableDefinition", kind: "class"},
  TypeAliasDeclaration: {name: "TypeDefinition", kind: "type"},
  InterfaceDeclaration: {name: "TypeDefinition", kind: "type"},
  EnumDeclaration: {name: "TypeDefinition", kind: "type"},
};

const javascriptMethodNameNodes = new Set([
  "PrivatePropertyDefinition",
  "Number",
  "String",
  "PropertyDefinition",
  "PropertyName",
]);

// Parsers consume BOM-free text, but every syntax-node offset must still address
// the original UTF-16 source. Shift only the root's child positions; descendants
// already use positions relative to those children.
function parseSource(parser: Parser, source: string): Tree {
  if (!source.startsWith("\uFEFF")) {
    return parser.parse(source);
  }
  const tree = parser.parse(source.slice(1));
  const {Tree} = require("@lezer/common") as typeof import("@lezer/common");
  return new Tree(tree.type, tree.children, tree.positions.map((position) => position + 1),
    source.length, tree.propValues);
}

function codeTree(source: string, format: SourceFormat): Tree {
  if (format.language === "go") {
    return parseSource(goParser(), source);
  }
  if (format.language === "typescript") {
    return parseSource(javascriptParser(format.jsx === true ? "ts jsx" : "ts"), source);
  }
  if (format.language === "javascript") {
    return parseSource(javascriptParser(format.jsx === true ? "jsx" : ""), source);
  }
  if (format.language === "python") {
    return parseSource(pythonParser(), source);
  }
  return parseSource(jsonParser(), source);
}

type InspectionData = {
  path: string;
  kind: FileKind;
  language: Language | null;
  size_bytes: number;
  line_count: number | null;
  parse_complete: boolean;
  outline: PublicOutlineEntry[];
};

class InspectFailure extends Error {
  constructor(readonly code: ErrorCode, message: string) {
    super(message);
  }
}

/**
 * LineMap indexes JavaScript UTF-16 offsets for each logical line, using shared-core line-counting semantics.
 */
export class LineMap {
  readonly starts = [0];
  readonly count: number;

  constructor(readonly source: string) {
    this.count = sharedLineCount(source);
    for (let offset = 0; offset < source.length; offset += 1) {
      if (source[offset] === "\r") {
        if (source[offset + 1] === "\n") {
          offset += 1;
        }
        this.starts.push(offset + 1);
      } else if (source[offset] === "\n") {
        this.starts.push(offset + 1);
      }
    }
    if (this.starts.at(-1) === source.length && source.length > 0) {
      this.starts.pop();
    }
  }

  /**
   * lineAt returns the 1-based line number containing the given JavaScript UTF-16 offset.
   */
  lineAt(offset: number): number {
    let low = 0;
    let high = this.starts.length;
    while (low + 1 < high) {
      const middle = Math.floor((low + high) / 2);
      if (this.starts[middle] <= offset) {
        low = middle;
      } else {
        high = middle;
      }
    }
    return low + 1;
  }

  logicalLine(line: number): {from: number; to: number; text: string} | null {
    if (!Number.isSafeInteger(line) || line < 1 || line > this.count) {
      return null;
    }
    const from = this.starts[line - 1];
    let to = line < this.count ? this.starts[line] : this.source.length;
    if (this.source[to - 1] === "\n") {
      to -= 1;
    }
    if (this.source[to - 1] === "\r") {
      to -= 1;
    }
    return {from, to, text: this.source.slice(from, to)};
  }

  range(node: SyntaxNode): {line: number; line_end: number} {
    return {
      line: this.lineAt(node.from),
      line_end: this.lineAt(Math.max(node.from, node.to - 1)),
    };
  }
}

function children(node: SyntaxNode): SyntaxNode[] {
  const result: SyntaxNode[] = [];
  for (let child = node.firstChild; child !== null; child = child.nextSibling) {
    result.push(child);
  }
  return result;
}

function descendants(node: SyntaxNode, name: string): SyntaxNode[] {
  const result: SyntaxNode[] = [];
  const visit = (current: SyntaxNode): void => {
    if (current.name === name) {
      result.push(current);
    }
    for (const child of children(current)) {
      visit(child);
    }
  };
  visit(node);
  return result;
}

function firstDescendant(node: SyntaxNode, names: ReadonlySet<string>): SyntaxNode | null {
  if (names.has(node.name)) {
    return node;
  }
  for (const child of children(node)) {
    const found = firstDescendant(child, names);
    if (found !== null) {
      return found;
    }
  }
  return null;
}

function nodeText(source: string, node: SyntaxNode): string {
  return source.slice(node.from, node.to);
}

function hasParseError(tree: Tree): boolean {
  let found = false;
  tree.iterate({
    enter(node) {
      if (node.type.isError) {
        found = true;
        return false;
      }
    },
  });
  return found;
}

function addNamedEntries(
  output: LocatedEntry[],
  source: string,
  lines: LineMap,
  declaration: SyntaxNode,
  names: SyntaxNode[],
  kind: CodeEntry["kind"],
  rangeNode = declaration,
): void {
  for (const nameNode of names) {
    const name = nodeText(source, nameNode);
    if (name !== "") {
      output.push({
        entry: {kind, name, ...lines.range(rangeNode)},
        end: rangeNode.to,
        offset: rangeNode.from,
        order: output.length,
        nameFrom: nameNode.from,
        nameTo: nameNode.to,
      });
    }
  }
}

/**
 * decodeGoImportPath extracts the import path from a Go string literal syntax node using the shared-core decoder.
 */
function decodeGoImportPath(source: string, node: SyntaxNode): string | null {
  const literal = nodeText(source, node);
  try {
    return decodeGoStringLiteral(literal);
  } catch {
    return null;
  }
}

function goSpecifications(declaration: SyntaxNode, name: string): SyntaxNode[] {
  const container = children(declaration).find((child) => child.name === "SpecList") ?? declaration;
  return children(container).filter((child) => child.name === name);
}

function goOutline(source: string, lines: LineMap, tree: Tree): LocatedEntry[] {
  const output: LocatedEntry[] = [];
  for (const declaration of children(tree.topNode)) {
    if (declaration.name === "ImportDecl") {
      for (const specification of goSpecifications(declaration, "ImportSpec")) {
        const pathNode = firstDescendant(specification, new Set(["String"]));
        if (pathNode === null) {
          continue;
        }
        const name = decodeGoImportPath(source, pathNode);
        if (name !== null && name !== "") {
          output.push({
            entry: {kind: "import", name, ...lines.range(specification)},
            end: specification.to,
            offset: specification.from,
            order: output.length,
          });
        }
      }
      continue;
    }

    const projected = goDeclarationProjections[declaration.name];
    if (projected !== undefined) {
      for (const specification of goSpecifications(declaration, projected.node)) {
        addNamedEntries(
          output,
          source,
          lines,
          declaration,
          children(specification).filter((child) => child.name === projected.name),
          projected.kind,
          specification,
        );
      }
      continue;
    }

    if (declaration.name === "FunctionDecl") {
      const name = firstDescendant(declaration, new Set(["DefName"]));
      if (name !== null) {
        addNamedEntries(output, source, lines, declaration, [name], "function");
      }
      continue;
    }

    if (declaration.name === "MethodDecl") {
      const name = firstDescendant(declaration, new Set(["FieldName"]));
      const receiverParameters = children(declaration).find((child) => child.name === "Parameters");
      const parameter = receiverParameters === undefined
        ? null
        : firstDescendant(receiverParameters, new Set(["Parameter"]));
      const receiverNode = parameter === null
        ? null
        : firstDescendant(parameter, new Set(["PointerType", "ParameterizedType", "TypeName"]));
      if (name !== null && receiverNode !== null) {
        const receiver = nodeText(source, receiverNode).replace(/\s+/gu, "");
        if (receiver !== "") {
          output.push({
            entry: {
              kind: "method",
              name: nodeText(source, name),
              receiver,
              ...lines.range(declaration),
            },
            end: declaration.to,
            offset: declaration.from,
            order: output.length,
            nameFrom: name.from,
            nameTo: name.to,
          });
        }
      }
    }
  }
  return output;
}

export function goDeclarationRange(
  source: string,
  definitionStartByte: number,
  definitionEndByte: number,
): {line: number; line_end: number} | null {
  const lines = new LineMap(source);
  const tree = parseSource(goParser(), source);
  if (hasParseError(tree)) {
    return null;
  }
  for (const located of goOutline(source, lines, tree)) {
    if (
      located.nameFrom === undefined
      || located.nameTo === undefined
      || located.entry.kind === "import"
    ) {
      continue;
    }
    const nameStartByte = byteLength(source.slice(0, located.nameFrom));
    const nameEndByte = nameStartByte + byteLength(source.slice(located.nameFrom, located.nameTo));
    if (nameStartByte === definitionStartByte && nameEndByte === definitionEndByte) {
      return {line: located.entry.line, line_end: located.entry.line_end};
    }
  }
  return null;
}

function javascriptDeclaration(node: SyntaxNode): SyntaxNode {
  let declaration = node;
  while (declaration.name === "ExportDeclaration" || declaration.name === "AmbientDeclaration") {
    const nested = children(declaration).find((child) => /Declaration$/u.test(child.name));
    if (nested === undefined) {
      break;
    }
    declaration = nested;
  }
  return declaration;
}

function bindingNamesBeforeInitializers(declaration: SyntaxNode): SyntaxNode[] {
  const names: SyntaxNode[] = [];
  let binding = true;
  for (const child of children(declaration)) {
    if (child.name === "Equals") {
      binding = false;
      continue;
    }
    if (child.name === ",") {
      binding = true;
      continue;
    }
    if (binding) {
      names.push(...descendants(child, "VariableDefinition"));
    }
  }
  return names;
}

function javascriptMethodName(source: string, method: SyntaxNode): {name: string; from: number; to: number} | null {
  const parts = children(method);
  const parametersIndex = parts.findIndex((child) => child.name === "ParamList");
  if (parametersIndex < 0) {
    return null;
  }
  const beforeParameters = parts.slice(0, parametersIndex);
  for (let index = beforeParameters.length - 1; index >= 0; index -= 1) {
    if (beforeParameters[index].name !== "]") {
      continue;
    }
    for (let open = index - 1; open >= 0; open -= 1) {
      if (beforeParameters[open].name === "[") {
        return {
          name: source.slice(beforeParameters[open].from, beforeParameters[index].to),
          from: beforeParameters[open].from,
          to: beforeParameters[index].to,
        };
      }
    }
  }
  const nameNode = beforeParameters.findLast((child) => javascriptMethodNameNodes.has(child.name));
  return nameNode === undefined
    ? null
    : {name: nodeText(source, nameNode), from: nameNode.from, to: nameNode.to};
}

function javascriptOutline(source: string, lines: LineMap, tree: Tree): LocatedEntry[] {
  const output: LocatedEntry[] = [];
  for (const topLevel of children(tree.topNode)) {
    const declaration = javascriptDeclaration(topLevel);
    if (declaration.name === "ImportDeclaration") {
      const bindings = descendants(declaration, "VariableDefinition");
      if (bindings.length > 0) {
        addNamedEntries(output, source, lines, declaration, bindings, "import");
      } else {
        const moduleNode = firstDescendant(declaration, new Set(["String"]));
        if (moduleNode !== null) {
          const name = decodeJavaScriptStringLiteral(nodeText(source, moduleNode));
          if (name !== null) {
            output.push({
              entry: {kind: "import", name, ...lines.range(declaration)},
              end: declaration.to,
              offset: declaration.from,
              order: output.length,
            });
          }
        }
      }
      continue;
    }

    if (declaration.name === "VariableDeclaration") {
      const declarationKind = children(declaration)[0]?.name === "const" ? "constant" : "variable";
      addNamedEntries(
        output,
        source,
        lines,
        declaration,
        bindingNamesBeforeInitializers(declaration),
        declarationKind,
        topLevel,
      );
      continue;
    }

    const projected = javascriptDeclarationProjections[declaration.name];
    if (projected === undefined) {
      continue;
    }
    const nameNode = firstDescendant(declaration, new Set([projected.name]));
    if (nameNode === null) {
      continue;
    }
    addNamedEntries(output, source, lines, declaration, [nameNode], projected.kind, topLevel);
    if (projected.kind !== "class") {
      continue;
    }
    const className = nodeText(source, nameNode);
    const body = children(declaration).find((child) => child.name === "ClassBody");
    if (body === undefined) {
      continue;
    }
    for (const method of children(body).filter((child) => child.name === "MethodDeclaration")) {
      const methodName = javascriptMethodName(source, method);
      if (methodName !== null && methodName.name !== "") {
        output.push({
          entry: {
            kind: "method",
            name: methodName.name,
            receiver: className,
            ...lines.range(method),
          },
          end: method.to,
          offset: method.from,
          order: output.length,
          nameFrom: methodName.from,
          nameTo: methodName.to,
        });
      }
    }
  }
  return output;
}

function pythonImportNames(source: string, declaration: SyntaxNode): string[] {
  const parts = children(declaration);
  const importIndex = parts.findLastIndex((child) => child.name === "import");
  if (importIndex < 0) {
    return [];
  }

  const names: string[] = [];
  let segment: SyntaxNode[] = [];
  const addSegment = (): void => {
    const aliasIndex = segment.findIndex((child) => child.name === "as");
    const candidates = aliasIndex < 0 ? segment : segment.slice(aliasIndex + 1);
    const binding = candidates.find((child) => child.name === "VariableName");
    if (binding !== undefined) {
      names.push(nodeText(source, binding));
    }
    segment = [];
  };

  for (const part of parts.slice(importIndex + 1)) {
    if (part.name === ",") {
      addSegment();
    } else {
      segment.push(part);
    }
  }
  addSegment();
  return names;
}

function pythonBindingNames(node: SyntaxNode): SyntaxNode[] {
  if (node.name === "VariableName") {
    return [node];
  }
  if (node.name === "MemberExpression" || node.name === "TypeDef") {
    return [];
  }
  return children(node).flatMap(pythonBindingNames);
}

function pythonAssignmentNames(declaration: SyntaxNode): SyntaxNode[] {
  const names: SyntaxNode[] = [];
  let segment: SyntaxNode[] = [];
  let assigned = false;
  for (const child of children(declaration)) {
    if (child.name === "AssignOp") {
      names.push(...segment.flatMap(pythonBindingNames));
      segment = [];
      assigned = true;
    } else {
      segment.push(child);
    }
  }
  if (!assigned) {
    const annotation = segment.findIndex((child) => child.name === "TypeDef");
    if (annotation >= 0) {
      names.push(...segment.slice(0, annotation).flatMap(pythonBindingNames));
    }
  }
  return names;
}

function pythonDeclaration(node: SyntaxNode): SyntaxNode {
  if (node.name !== "DecoratedStatement") {
    return node;
  }
  return children(node).find((child) =>
    child.name === "FunctionDefinition" || child.name === "ClassDefinition",
  ) ?? node;
}

function pythonOutline(source: string, lines: LineMap, tree: Tree): LocatedEntry[] {
  const output: LocatedEntry[] = [];
  for (const topLevel of children(tree.topNode)) {
    const declaration = pythonDeclaration(topLevel);
    if (declaration.name === "ImportStatement") {
      for (const name of pythonImportNames(source, declaration)) {
        output.push({
          entry: {kind: "import", name, ...lines.range(declaration)},
          end: declaration.to,
          offset: declaration.from,
          order: output.length,
        });
      }
      continue;
    }
    if (declaration.name === "AssignStatement") {
      addNamedEntries(
        output,
        source,
        lines,
        declaration,
        pythonAssignmentNames(declaration),
        "variable",
      );
      continue;
    }
    if (declaration.name !== "FunctionDefinition" && declaration.name !== "ClassDefinition") {
      continue;
    }
    const nameNode = firstDescendant(declaration, new Set(["VariableName"]));
    if (nameNode === null) {
      continue;
    }
    const kind = declaration.name === "ClassDefinition" ? "class" : "function";
    addNamedEntries(output, source, lines, declaration, [nameNode], kind, topLevel);
    if (kind !== "class") {
      continue;
    }
    const className = nodeText(source, nameNode);
    const body = children(declaration).find((child) => child.name === "Body");
    if (body === undefined) {
      continue;
    }
    for (const candidate of children(body)) {
      const method = pythonDeclaration(candidate);
      if (method.name !== "FunctionDefinition") {
        continue;
      }
      const methodName = firstDescendant(method, new Set(["VariableName"]));
      if (methodName !== null) {
        output.push({
          entry: {
            kind: "method",
            name: nodeText(source, methodName),
            receiver: className,
            ...lines.range(candidate),
          },
          end: candidate.to,
          offset: candidate.from,
          order: output.length,
          nameFrom: methodName.from,
          nameTo: methodName.to,
        });
      }
    }
  }
  return output;
}

function codeOutline(source: string, lines: LineMap, format: SourceFormat, tree: Tree): LocatedEntry[] {
  if (format.language === "go") {
    return goOutline(source, lines, tree);
  }
  if (format.language === "javascript" || format.language === "typescript") {
    return javascriptOutline(source, lines, tree);
  }
  if (format.language === "python") {
    return pythonOutline(source, lines, tree);
  }
  return [];
}

export function declarationRange(
  source: string,
  lines: LineMap,
  format: SourceFormat,
  definitionFrom: number,
  definitionTo: number,
): {line: number; line_end: number} | null {
  if (format.kind !== "code") {
    return null;
  }
  const tree = codeTree(source, format);
  if (hasParseError(tree)) {
    return null;
  }
  for (const located of codeOutline(source, lines, format, tree)) {
    if (
      located.nameFrom === definitionFrom
      && located.nameTo === definitionTo
      && located.entry.kind !== "import"
    ) {
      return {line: located.entry.line, line_end: located.entry.line_end};
    }
  }
  return null;
}

export function symbolOffsets(
  source: string,
  lines: LineMap,
  format: SourceFormat,
  line: number,
  symbol: string,
): number[] {
  const logicalLine = lines.logicalLine(line);
  if (logicalLine === null) {
    return [];
  }
  const tree = format.kind === "json" ? parseSource(jsonParser(), source) : codeTree(source, format);
  const offsets: number[] = [];
  const visit = (node: SyntaxNode): void => {
    if (node.to <= logicalLine.from || node.from >= logicalLine.to) {
      return;
    }
    if (node.firstChild !== null) {
      for (let child = node.firstChild; child !== null; child = child.nextSibling) {
        visit(child);
      }
      return;
    }
    if (node.type.isError || node.from < logicalLine.from || node.to > logicalLine.to) {
      return;
    }
    const text = source.slice(node.from, node.to);
    if (format.kind === "json") {
      if (node.name !== "PropertyName" && node.name !== "String") {
        return;
      }
      try {
        if (JSON.parse(text) === symbol) {
          offsets.push(node.from + 1);
        }
      } catch {
        // A recovered invalid JSON string is not a selectable symbol.
      }
      return;
    }
    const allowed = format.language === "go"
      ? goIdentifierNodes
      : format.language === "python"
        ? pythonIdentifierNodes
        : javascriptIdentifierNodes;
    if (allowed.has(node.name) && text === symbol) {
      offsets.push(node.from);
    }
  };
  visit(tree.topNode);
  return offsets;
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
  return {
    endOffset: lines.starts[closingLine + 1] ?? source.length,
    entries,
    parseComplete: document.errors.length === 0,
  };
}

function markdownOutline(
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

function jsonOutline(source: string, lines: LineMap, tree: Tree): LocatedEntry[] {
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

function ordered(entries: LocatedEntry[]): LocatedEntry[] {
  return entries.sort((left, right) => left.offset - right.offset || left.order - right.order);
}

/**
 * sourceFormat classifies a file path using the shared-core classifier and returns the outline-capable source format.
 */
export function sourceFormat(filePath: string): SourceFormat | null {
  const capabilities = classifySourcePath(filePath);
  if (capabilities === null || capabilities.outline !== true) {
    return null;
  }
  return {
    kind: capabilities.kind,
    language: capabilities.language ?? null,
    ...(capabilities.jsx === true ? {jsx: true} : {}),
  };
}

function hashOutline(lines: LineMap, outline: LocatedEntry[]): PublicOutlineEntry[] {
  // All entries refer to this immutable source snapshot, including repeated endpoints.
  const identities = new Map<number, string>();
  function rowIdentity(line: number): string {
    const cached = identities.get(line);
    if (cached !== undefined) {
      return cached;
    }
    const logical = lines.logicalLine(line);
    if (logical === null) {
      throw new InspectFailure("parse", `missing line ${line}`);
    }
    const identity = `${line}:${hashLine(logical.text)}`;
    identities.set(line, identity);
    return identity;
  }
  return outline.map(({entry}) => ({
    ...entry,
    line: rowIdentity(entry.line),
    line_end: rowIdentity(entry.line_end),
  }));
}

function parseContent(
  source: string,
  format: SourceFormat,
): {parseComplete: boolean; outline: PublicOutlineEntry[]; lineCount: number} {
  const lines = new LineMap(source);
  try {
    if (format.kind === "code") {
      const tree = codeTree(source, format);
      return {
        parseComplete: !hasParseError(tree),
        outline: hashOutline(lines, ordered(codeOutline(source, lines, format, tree))),
        lineCount: lines.count,
      };
    }
    if (format.kind === "markdown") {
      const tree = parseSource(markdownParser(), source);
      const outline = markdownOutline(source, lines, tree);
      return {
        parseComplete: !hasParseError(tree) && outline.parseComplete,
        outline: hashOutline(lines, ordered(outline.entries)),
        lineCount: lines.count,
      };
    }
    const tree = parseSource(jsonParser(), source);
    return {
      parseComplete: !hasParseError(tree),
      outline: hashOutline(lines, ordered(jsonOutline(source, lines, tree))),
      lineCount: lines.count,
    };
  } catch (error) {
    throw new InspectFailure("parse", `parser failed: ${errorText(error)}`);
  }
}

function failure(pathValue: string | null, code: ErrorCode, message: string): string {
  return `${JSON.stringify({ok: false, path: pathValue, error: {code, message}})}\n`;
}

function success(data: InspectionData): string {
  const complete = `${JSON.stringify({ok: true, data, truncated: false, truncation: null})}\n`;
  if (byteLength(complete) <= OUTPUT_BYTES) {
    return complete;
  }
  if (data.outline.length === 0) {
    throw new InspectFailure("output_limit", "minimum success result exceeds the output byte limit");
  }

  const render = (count: number): string => `${JSON.stringify({
    ok: true,
    data: {...data, outline: data.outline.slice(0, count)},
    truncated: true,
    truncation: {reason: "output_bytes", after_entries: count},
  })}\n`;

  let low = 0;
  let high = data.outline.length - 1;
  let selected = -1;
  while (low <= high) {
    const middle = Math.floor((low + high) / 2);
    if (byteLength(render(middle)) <= OUTPUT_BYTES) {
      selected = middle;
      low = middle + 1;
    } else {
      high = middle - 1;
    }
  }
  if (selected < 0) {
    throw new InspectFailure("output_limit", "minimum success result exceeds the output byte limit");
  }
  return render(selected);
}

function normalizeInputPath(input: string): string {
  if (input === "" || input.includes("\0")) {
    throw new InspectFailure("usage", "inspect_file expects exactly one usable path");
  }
  const normalized = path.normalize(input);
  if (
    normalized === "@shell"
    || normalized.startsWith(`@shell${path.sep}`)
  ) {
    throw new InspectFailure("usage", "@shell references are read through hcat");
  }
  return normalized;
}

function filesystemFailure(error: unknown): InspectFailure {
  if (error instanceof InspectFailure) {
    return error;
  }
  if (error instanceof Error && "code" in error && error.code === "ENOENT") {
    return new InspectFailure("not_found", "path does not exist");
  }
  return new InspectFailure("read", `cannot inspect path: ${errorText(error)}`);
}

async function inspect(input: string): Promise<InspectionData> {
  const normalized = normalizeInputPath(input);
  const target = path.resolve(normalized);

  let info;
  try {
    info = await stat(target);
  } catch (error) {
    throw filesystemFailure(error);
  }
  if (!info.isFile()) {
    throw new InspectFailure("not_regular", "path is not a regular file");
  }

  const resultPath = normalized.split(path.sep).join("/");
  const format = sourceFormat(normalized);
  if (format === null) {
    return {
      path: resultPath,
      kind: "none",
      language: null,
      size_bytes: info.size,
      line_count: null,
      parse_complete: true,
      outline: [],
    };
  }

  let bytes: Uint8Array;
  try {
    bytes = await readFile(target);
  } catch (error) {
    throw filesystemFailure(error);
  }
  let source: string;
  try {
    source = decodeUTF8(bytes, "source file");
  } catch {
    throw new InspectFailure("not_utf8", "supported file is not valid UTF-8");
  }
  const parsed = parseContent(source, format);
  return {
    path: resultPath,
    kind: format.kind,
    language: format.language,
    size_bytes: bytes.byteLength,
    line_count: parsed.lineCount,
    parse_complete: parsed.parseComplete,
    outline: parsed.outline,
  };
}

function inspectFileInput(input: string): string[] {
  const value = stripOptionalFinalNewline(input);
  if (value === "") {
    return [];
  }
  if (value.startsWith("\"")) {
    try {
      const parsed = JSON.parse(value);
      return typeof parsed === "string" ? [parsed] : [];
    } catch {
      return [];
    }
  }
  return [value];
}

export const inspectFileDescription = `Inspect one host-readable regular file and return bounded JSON metadata and a structural outline. Outline line and line_end are copyable LINE:HASH identities, not source text.

Result shape schema:
${inspectFileShapeSchemaJSON}`;

export function createInspectFileTool(description: string, grammar: string): Tool<string[]> {
  return createExecutorTool({
    name: "inspect_file",
    description,
    grammar,
    argv: inspectFileInput,
    async execute(argv) {
      let suppliedPath: string | null = null;
      try {
        if (argv.length !== 1) {
          throw new InspectFailure("usage", "inspect_file expects PATH");
        }
        suppliedPath = argv[0];
        const stdout = success(await inspect(suppliedPath));
        return {stdout, exitCode: 0};
      } catch (error) {
        const cause = error instanceof InspectFailure
          ? error
          : new InspectFailure("read", `cannot inspect file: ${errorText(error)}`);
        return {stdout: failure(suppliedPath, cause.code, cause.message), exitCode: 1,
          failureClass: cause.code === "usage" ? "invalid_arguments" : cause.code === "not_found" ? "not_found"
            : cause.code === "output_limit" ? "output_limit" : "reader_error"};
      }
    },
  });
}
