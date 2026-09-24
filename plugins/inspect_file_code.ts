import type {SyntaxNode, Tree} from "@lezer/common";
import {goOutline as parseGoOutline} from "mekugi:core/v1";
import {byteLength} from "./common.ts";
import {decodeJavaScriptStringLiteral} from "./javascript_string.ts";
import {typescriptOutline} from "./inspect_file_typescript.ts";
import {
  addNamedEntries,
  children,
  descendants,
  firstDescendant,
  LineMap,
  nodeText,
  parseSource,
  parseErrorEntries,
  type CodeEntry,
  type LocatedEntry,
  type SourceFormat,
} from "./inspect_file_support.ts";

// Load parser tables only for a source kind that actually needs them.
const goParser = () => (require("@lezer/go") as typeof import("@lezer/go")).parser;
const javascriptParser = (dialect = "") =>
  (require("@lezer/javascript") as typeof import("@lezer/javascript")).parser.configure({dialect});
const jsonParser = () => (require("@lezer/json") as typeof import("@lezer/json")).parser;
const pythonParser = () => (require("@lezer/python") as typeof import("@lezer/python")).parser;

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
export function codeTree(source: string, format: SourceFormat): Tree {
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
function goOutline(source: string, lines: LineMap, _tree?: Tree): LocatedEntry[] {
  const parsed = parseGoOutline(source);
  const bytes = Buffer.from(source);
  const positions = [...parsed.entries.flatMap(entry => [entry.from, entry.to, entry.name_from, entry.name_to]),
    ...parsed.errors.map(error => error.offset)].filter(value => value >= 0);
  const offsets = new Map<number, number>();
  let previous = 0, characters = 0;
  for (const position of [...new Set(positions)].sort((a, b) => a - b)) {
    characters += bytes.subarray(previous, position).toString("utf8").length;
    offsets.set(position, characters);
    previous = position;
  }
  const offset = (value: number): number => offsets.get(value)!;
  const entries: LocatedEntry[] = parsed.entries.map((entry, order) => {
    const from = offset(entry.from), to = offset(entry.to);
    const span = {line: lines.lineAt(from), line_end: lines.lineAt(Math.max(from, to - 1))};
    return {
      entry: entry.kind === "method"
        ? {kind: "method", name: entry.name, receiver: entry.receiver ?? "", ...span}
        : {kind: entry.kind, name: entry.name, ...span},
      offset: from, end: to, order, complete: entry.complete,
      ...(entry.name_from >= 0 ? {nameFrom: offset(entry.name_from), nameTo: offset(entry.name_to)} : {}),
    };
  });
  const errorRows = new Set<number>();
  for (const error of parsed.errors) {
    const at = Math.min(offset(error.offset), Math.max(0, source.length - 1));
    const line = lines.lineAt(at);
    if (lines.count === 0 || errorRows.has(line)) continue;
    errorRows.add(line);
    entries.push({entry: {kind: "parse_error", name: "syntax error", line, line_end: line}, offset: at, end: at, order: entries.length});
  }
  return entries;
}

export function goDeclarationRange(
  source: string,
  definitionStartByte: number,
  definitionEndByte: number,
): {line: number; line_end: number} | null {
  const lines = new LineMap(source);
  for (const located of goOutline(source, lines)) {
    if (
      located.nameFrom === undefined
      || located.nameTo === undefined
      || located.entry.kind === "import" || located.complete === false
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

export function codeOutline(source: string, lines: LineMap, format: SourceFormat, tree?: Tree): LocatedEntry[] {
  if (format.language === "go") {
    return goOutline(source, lines, tree);
  }
  if (format.language === "typescript") {
    const parsed = typescriptOutline(source, lines, format.jsx === true);
    if (parsed.recoveryFrom === undefined) return parsed.entries;
    const recovered = tree ?? codeTree(source, format);
    const fallback = [...javascriptOutline(source, lines, recovered), ...parseErrorEntries(recovered, lines)]
      .sort((a, b) => a.offset - b.offset);
    // A fatal error cannot discard intact declarations on either side. Lezer
    // supplies recovery boundaries, but Babel verifies every recovered segment.
    // A typeof-import query is not a new top-level import statement.
    const boundaries = [...new Set([0, ...children(recovered.topNode)
      .filter(node => node.name.endsWith("Declaration")
        && !(node.name === "ImportDeclaration" && /^import\s*[.(]/u.test(source.slice(node.from, node.to))))
      .map(node => node.from), source.length])].sort((a, b) => a - b);
    const output: LocatedEntry[] = [];
    let fallbackIndex = 0;
    for (let index = 0; index + 1 < boundaries.length; index++) {
      const from = boundaries[index], to = boundaries[index + 1];
      const firstFallback = fallbackIndex;
      while (fallbackIndex < fallback.length && fallback[fallbackIndex].offset < to) fallbackIndex++;
      const text = source.slice(from, to);
      const segment = typescriptOutline(text, new LineMap(text), format.jsx === true);
      if (segment.recoveryFrom !== undefined) {
        output.push(...fallback.slice(firstFallback, fallbackIndex));
        continue;
      }
      for (const entry of segment.entries) {
        const offset = entry.offset + from, end = entry.end + from;
        output.push({...entry, offset, end, order: output.length,
          entry: {...entry.entry, line: lines.lineAt(offset), line_end: lines.lineAt(Math.max(offset, end - 1))},
          ...(entry.nameFrom !== undefined ? {nameFrom: entry.nameFrom + from, nameTo: entry.nameTo! + from} : {}),
        });
      }
    }
    return output;
  }
  if (format.language === "javascript" || format.language === "typescript") {
    return javascriptOutline(source, lines, tree ?? codeTree(source, format));
  }
  if (format.language === "python") {
    return pythonOutline(source, lines, tree ?? codeTree(source, format));
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
  if (format.language === "go") {
    return goDeclarationRange(source, byteLength(source.slice(0, definitionFrom)), byteLength(source.slice(0, definitionTo)));
  }
  const tree = format.language === "typescript" ? undefined : codeTree(source, format);
  for (const located of codeOutline(source, lines, format, tree)) {
    if (
      located.nameFrom === definitionFrom
      && located.nameTo === definitionTo
      && located.entry.kind !== "import"
      && located.complete !== false
    ) {
      let invalid = false;
      if (located.complete === undefined) (tree ?? codeTree(source, format)).iterate({from: located.offset, to: located.end, enter(node) {
        if (node.type.isError && node.from >= located.offset && node.from <= located.end) invalid = true;
      }});
      if (invalid) return null;
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
