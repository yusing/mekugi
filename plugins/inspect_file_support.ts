import type {Parser, SyntaxNode, Tree} from "@lezer/common";
import {lineCount as sharedLineCount} from "mekugi:core/v1";

export type Language = "go" | "javascript" | "typescript" | "python";
export type FileKind = "code" | "markdown" | "json" | "none";
export type ValueType = "object" | "array" | "string" | "number" | "boolean" | "null";

export type CodeEntry = {
  kind: "import" | "constant" | "variable" | "type" | "class" | "function";
  name: string;
  line: number;
  line_end: number;
};

export type MethodEntry = {
  kind: "method";
  name: string;
  receiver: string;
  line: number;
  line_end: number;
};

export type HeadingEntry = {
  kind: "heading";
  name: string;
  level: number;
  line: number;
  line_end: number;
};

export type FrontmatterEntry = {
  kind: "frontmatter";
  name: string;
  line: number;
  line_end: number;
};

export type JSONEntry = {
  kind: "json";
  pointer: string;
  value_type: ValueType;
  line: number;
  line_end: number;
};

export type ParseErrorEntry = {kind: "parse_error"; name: "syntax error"; line: number; line_end: number};
export type OutlineEntry = CodeEntry | MethodEntry | HeadingEntry | FrontmatterEntry | JSONEntry | ParseErrorEntry;
export type PublicOutlineEntry = OutlineEntry;
export type LocatedEntry = {
  entry: OutlineEntry;
  end: number;
  offset: number;
  order: number;
  nameFrom?: number;
  nameTo?: number;
  complete?: boolean;
};

export type SourceFormat = {
  kind: Exclude<FileKind, "none">;
  language: Language | null;
  jsx?: true;
};
// Parsers consume BOM-free text, but every syntax-node offset must still address
// the original UTF-16 source. Shift only the root's child positions; descendants
// already use positions relative to those children.
export function parseSource(parser: Parser, source: string): Tree {
  if (!source.startsWith("\uFEFF")) {
    return parser.parse(source);
  }
  const tree = parser.parse(source.slice(1));
  const {Tree} = require("@lezer/common") as typeof import("@lezer/common");
  return new Tree(tree.type, tree.children, tree.positions.map((position) => position + 1),
    source.length, tree.propValues);
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

export function children(node: SyntaxNode): SyntaxNode[] {
  const result: SyntaxNode[] = [];
  for (let child = node.firstChild; child !== null; child = child.nextSibling) {
    result.push(child);
  }
  return result;
}

export function descendants(node: SyntaxNode, name: string): SyntaxNode[] {
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

export function firstDescendant(node: SyntaxNode, names: ReadonlySet<string>): SyntaxNode | null {
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

export function nodeText(source: string, node: SyntaxNode): string {
  return source.slice(node.from, node.to);
}

export function hasParseError(tree: Tree): boolean {
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

export function parseErrorEntries(tree: Tree, lines: LineMap): LocatedEntry[] {
  const entries: LocatedEntry[] = [];
  const seen = new Set<number>();
  tree.iterate({enter(node) {
    if (!node.type.isError || lines.count === 0) return;
    const offset = Math.min(node.from, Math.max(0, lines.source.length - 1));
    const line = lines.lineAt(offset);
    if (seen.has(line)) return;
    seen.add(line);
    entries.push({entry: {kind: "parse_error", name: "syntax error", line, line_end: line},
      offset, end: node.to, order: entries.length});
  }});
  return entries;
}

export function addNamedEntries(
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
