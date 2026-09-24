import type {Identifier, Node} from "@babel/types";
import {LineMap, type LocatedEntry, type CodeEntry} from "./inspect_file_support.ts";

function bindingNames(node: Node): Identifier[] {
  switch (node.type) {
    case "Identifier": return [node];
    case "RestElement": return bindingNames(node.argument);
    case "AssignmentPattern": return bindingNames(node.left);
    case "ArrayPattern": return node.elements.flatMap(item => item ? bindingNames(item) : []);
    case "ObjectPattern": return node.properties.flatMap(item => bindingNames(item.type === "RestElement" ? item.argument : item.value));
    default: return [];
  }
}

// Lezer's recovery cannot represent valid typeof-import type queries. Use a
// TypeScript grammar for complete files, retaining Lezer's local recovery only
// when this parser explicitly cannot recover malformed source.
export function typescriptOutline(source: string, lines: LineMap, jsx: boolean): {entries: LocatedEntry[]; recoveryFrom?: number} {
  const {parse} = require("@babel/parser") as typeof import("@babel/parser");
  let file;
  try {
    file = parse(source, {sourceType: "unambiguous", plugins: jsx ? ["typescript", "jsx"] : ["typescript"], errorRecovery: true});
  } catch (error) {
    const position = error instanceof Error && "pos" in error && typeof error.pos === "number" ? error.pos : 0;
    return {entries: [], recoveryFrom: position};
  }
  const errors = file.errors ?? [];
  const output: LocatedEntry[] = [];
  const add = (kind: CodeEntry["kind"] | "method", name: string, node: Node, nameNode?: Node, receiver = ""): void => {
    const from = node.start!, to = node.end!;
    const range = {line: lines.lineAt(from), line_end: lines.lineAt(Math.max(from, to - 1))};
    output.push({
      entry: kind === "method" ? {kind, name, receiver, ...range} : {kind, name, ...range},
      offset: from, end: to, order: output.length,
      complete: !errors.some(error => error.pos >= from && error.pos <= to),
      ...(nameNode ? {nameFrom: nameNode.start!, nameTo: nameNode.start! + name.length} : {}),
    });
  };
  for (const statement of file.program.body) {
    const declaration = statement.type === "ExportNamedDeclaration" || statement.type === "ExportDefaultDeclaration"
      ? statement.declaration : statement;
    if (!declaration) continue;
    switch (declaration.type) {
      case "ImportDeclaration":
        if (declaration.specifiers.length === 0) add("import", declaration.source.value, statement);
        for (const item of declaration.specifiers) add("import", item.local.name, statement);
        break;
      case "VariableDeclaration":
        for (const variable of declaration.declarations) {
          for (const name of bindingNames(variable.id)) add(declaration.kind === "const" ? "constant" : "variable", name.name, statement, name);
        }
        break;
      case "FunctionDeclaration":
      case "TSDeclareFunction":
        if (declaration.id) add("function", declaration.id.name, statement, declaration.id);
        break;
      case "TSTypeAliasDeclaration":
      case "TSInterfaceDeclaration":
      case "TSEnumDeclaration":
        add("type", declaration.id.name, statement, declaration.id);
        break;
      case "ClassDeclaration":
        if (!declaration.id) break;
        add("class", declaration.id.name, statement, declaration.id);
        for (const method of declaration.body.body) {
          if (method.type !== "ClassMethod" && method.type !== "ClassPrivateMethod" && method.type !== "TSDeclareMethod") continue;
          const key = source.slice(method.key.start!, method.key.end!);
          const name = "computed" in method && method.computed ? `[${key}]` : key;
          add("method", name, method, method.key, declaration.id.name);
        }
        break;
    }
  }
  const errorRows = new Set<number>();
  for (const error of errors) {
    const offset = Math.min(error.pos, Math.max(0, source.length - 1));
    const line = lines.lineAt(offset);
    if (lines.count === 0 || errorRows.has(line)) continue;
    errorRows.add(line);
    output.push({entry: {kind: "parse_error", name: "syntax error", line, line_end: line},
      offset, end: offset, order: output.length});
  }
  return {entries: output};
}
