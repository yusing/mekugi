export const inspectFileShapeSchema = {
  success: {
    ok: true,
    data: {
      path: "string",
      kind: "code | markdown | json | none",
      language: "go | javascript | typescript | python | null",
      size_bytes: "integer",
      line_count: "integer | null",
      parse_complete: "boolean",
      outline: "outline_entry[]",
    },
    truncated: "boolean",
    truncation: "null | {reason: output_bytes | output_tokens, after_entries: integer}",
  },
  failure: {
    ok: false,
    path: "string | null",
    error: {
      code: "usage | not_found | not_regular | not_utf8 | read | parse | output_limit",
      message: "string",
    },
  },
  outline_entry: [
    {
      kind: "import | constant | variable | type | class | function",
      name: "string",
      line: "LINE:HASH",
      line_end: "LINE:HASH",
    },
    {
      kind: "method",
      name: "string",
      receiver: "string",
      line: "LINE:HASH",
      line_end: "LINE:HASH",
    },
    {
      kind: "heading",
      name: "string",
      level: "1 | 2 | 3 | 4 | 5 | 6",
      line: "LINE:HASH",
      line_end: "LINE:HASH",
    },
    {
      kind: "frontmatter",
      name: "string",
      line: "LINE:HASH",
      line_end: "LINE:HASH",
    },
    {
      kind: "json",
      pointer: "RFC 6901 string",
      value_type: "object | array | string | number | boolean | null",
      line: "LINE:HASH",
      line_end: "LINE:HASH",
    },
  ],
} as const;

export const inspectFileShapeSchemaJSON = JSON.stringify(inspectFileShapeSchema, null, 2);
