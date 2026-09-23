declare module "mekugi:core/v1" {
  export class SharedCoreError extends Error {
    readonly code: string;
  }

  export type SharedCoreInput = string | Uint8Array;

  export type LineBounds = {
    readonly byteStart: number;
    readonly byteContentEnd: number;
    readonly byteEnd: number;
  };

  export type SourceCapabilities = {
    kind: "code" | "markdown" | "json";
    language?: "go" | "javascript" | "typescript" | "python";
    jsx?: boolean;
    outline?: boolean;
    semanticResolver?: "gopls" | "typescript" | "python";
    syntaxValidation?: boolean;
  };

  export type ParsedShellHeader = {
    interpreter?: string[];
    body?: string;
    /** One-based source line of the params directive, when present. */
    paramsLine?: number;
    params?: Record<string, unknown>;
  };

  export function lineCount(value: SharedCoreInput): number;
  export function lineBounds(value: SharedCoreInput, line: number): LineBounds | null;
  export function parsePositiveInteger(value: string): number;
  export function decodeQuotedOperand(value: string): {value: string; rest: string};
  export function classifySourcePath(value: string): SourceCapabilities | null;
  export function isGoIdentifier(value: string): boolean;
  export function decodeGoStringLiteral(value: string): string;
  export function parseShellHeader(value: string): ParsedShellHeader;
  export function interpreterIdentity(value: string): string;
}
