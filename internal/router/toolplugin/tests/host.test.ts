import {afterEach, describe, expect, test} from "bun:test";
import {spawnSync} from "node:child_process";
import {chmod, mkdir, mkdtemp, rm, symlink, writeFile} from "node:fs/promises";
import {tmpdir} from "node:os";
import path from "node:path";
import {fileURLToPath} from "node:url";

type HostResponse = {
  errors: string[];
  plugins: Array<{
    id: string;
    module: string;
    tools: Array<{
      specification: Record<string, unknown>;
    }>;
  }>;
};

const hostPath = fileURLToPath(new URL("../host.mjs", import.meta.url));
const temporaryDirectories: string[] = [];

async function temporaryDirectory(): Promise<string> {
  const directory = await mkdtemp(path.join(tmpdir(), "tool-plugin-host-"));
  temporaryDirectories.push(directory);
  return directory;
}

afterEach(async () => {
  await Promise.all(
    temporaryDirectories.splice(0).map((directory) => rm(directory, {recursive: true, force: true})),
  );
});

function invokeHost(
  snapshotRoot: string,
  request: Record<string, unknown>,
  environment: Record<string, string | undefined> = process.env,
): {status: number | null; stdout: string; stderr: string} {
  const result = spawnSync("node", [hostPath], {
    cwd: snapshotRoot,
    encoding: "utf8",
    env: {...environment, NODE_NO_WARNINGS: "1"},
    input: JSON.stringify({...request, snapshotRoot}),
  });
  return {
    status: result.status,
    stdout: result.stdout,
    stderr: result.stderr,
  };
}

function pluginDeclaration(format?: {
  type: string;
  syntax: string;
  definition: string;
}): string {
  const formatField = format === undefined ? "" : `, format: ${JSON.stringify(format)}`;
  return `export default {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "grammar.test",
  tools: [{
    specification: {
      type: "custom",
      name: "grammar_test",
      description: "test tool"${formatField}
    },
    parse(input) { return input; },
    argv(input) { return [input]; },
    execute() { return {stdout: "", exitCode: 0}; }
  }]
};
`;
}

async function validateDeclaration(
  declaration: string,
): Promise<{result: ReturnType<typeof invokeHost>; response: HostResponse}> {
  const directory = await temporaryDirectory();
  await writeFile(path.join(directory, "plugin.mjs"), declaration, "utf8");
  const result = invokeHost(directory, {
    operation: "validate",
    modules: ["plugin.mjs"],
  });
  expect(result.status).toBe(0);
  return {
    result,
    response: JSON.parse(result.stdout) as HostResponse,
  };
}

describe("plugin snapshot paths", () => {
  test("accepts a snapshot root reached through a symlink", async () => {
    const parent = await temporaryDirectory();
    const directory = path.join(parent, "canonical");
    const alias = path.join(parent, "alias");
    await mkdir(directory);
    await writeFile(path.join(directory, "plugin.mjs"), pluginDeclaration(), "utf8");
    await symlink(directory, alias, "dir");

    const result = invokeHost(alias, {
      operation: "validate",
      modules: ["plugin.mjs"],
    });

    expect(result.status).toBe(0);
    const response = JSON.parse(result.stdout) as HostResponse;
    expect(response.errors).toEqual([]);
    expect(response.plugins).toHaveLength(1);
  });
});

describe("plugin declaration validation", () => {
  test.each([
    [
      "lark",
      "start: (\n  WORD\n  | \"ok\"\n)\n%import common.WORD\n%import common.WS\n%ignore WS",
    ],
    ["regex", "^(?:[a-z]+|[0-9]{1,3})$"],
  ])("accepts the supported %s grammar subset", async (syntax, definition) => {
    const {response} = await validateDeclaration(pluginDeclaration({
      type: "grammar",
      syntax,
      definition,
    }));
    expect(response.errors).toEqual([]);
    expect(response.plugins).toHaveLength(1);
    expect(response.plugins[0]?.tools[0]?.specification).toMatchObject({
      name: "grammar_test",
      format: {type: "grammar", syntax, definition},
    });
  });

  test.each([
    ["invalid token", "lark", "start: @", "unexpected token"],
    ["priority", "lark", "start.2: \"ok\"", "unsupported priority"],
    ["template", "lark", "template{x}: x\nstart: template{\"ok\"}", "invalid rule declaration"],
    ["non-common import", "lark", "%import other.WORD\nstart: WORD", "imports outside common"],
    ["declare", "lark", "%declare WORD\nstart: WORD", "unsupported %declare"],
    ["duplicate rule", "lark", "start: \"a\"\nstart: \"b\"", "defined more than once"],
    ["duplicate terminal", "lark", "TOKEN: \"a\"\nTOKEN: \"b\"\nstart: TOKEN", "defined more than once"],
    ["newline", "regex", "one\ntwo", "must be one line"],
    ["lookaround", "regex", "(?=ok)ok", "look-around"],
    ["lazy quantifier", "regex", "ok+?", "unsupported construct"],
    ["backreference", "regex", String.raw`(ok)\1`, "backreferences are not supported"],
  ])("rejects %s", async (_name, syntax, definition, diagnostic) => {
    const {response} = await validateDeclaration(pluginDeclaration({
      type: "grammar",
      syntax,
      definition,
    }));
    expect(response.plugins).toEqual([]);
    expect(response.errors.join("\n")).toContain(diagnostic);
  });

  test("rejects a user declaration claiming a native executor", async () => {
    const {response} = await validateDeclaration(
      pluginDeclaration()
        .replace('name: "grammar_test"', 'name: "mread"')
        .replace('parse(input) { return input; },\n    argv(input) { return [input]; },\n    execute() { return {stdout: "", exitCode: 0}; }', 'nativeExecutor: "mread"'),
    );
    expect(response.plugins).toEqual([]);
    expect(response.errors.join("\n")).toContain("native executor is not a bundled tool");
  });

  test("reports independent declaration errors together", async () => {
    const directory = await temporaryDirectory();
    await Promise.all([
      writeFile(
        path.join(directory, "bad-default.mjs"),
        "export default {apiVersion: 'wrong'};\n",
        "utf8",
      ),
      writeFile(
        path.join(directory, "bad-tool.mjs"),
        pluginDeclaration().replace('name: "grammar_test"', 'name: "eval"'),
        "utf8",
      ),
    ]);
    const result = invokeHost(directory, {
      operation: "validate",
      modules: ["bad-default.mjs", "bad-tool.mjs"],
    });
    expect(result.status).toBe(0);
    const response = JSON.parse(result.stdout) as HostResponse;
    expect(response.plugins).toEqual([]);
    expect(response.errors.join("\n")).toContain("default export must contain only");
    expect(response.errors.join("\n")).toContain("collides with a shell keyword or built-in");
  });
});

describe("plugin execution", () => {
  test("applies the executor contract", async () => {
    const directory = await temporaryDirectory();
    await writeFile(
      path.join(directory, "plugin.mjs"),
      `export default {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "execution.test",
  tools: [{
    specification: {type: "custom", name: "execution_test", description: "test tool"},
    parse(input) { return input; },
    argv(input) { return ["--fixed", input]; },
    execute(argv) {
      return {stdout: argv.join("|"), stderr: "fixture stderr", exitCode: 7};
    }
  }]
};
`,
      "utf8",
    );

    const executed = invokeHost(directory, {
      operation: "execute",
      outputBudgetBytes: 16 * 1024 * 1024,
      module: "plugin.mjs",
      index: 0,
      arguments: ["one", "two words"],
    });
    expect(executed.status).toBe(0);
    expect(JSON.parse(executed.stdout)).toEqual({
      stdout: "one|two words",
      stderr: "fixture stderr",
      exitCode: 7,
    });
  });

  test("rejects invalid executor results", async () => {
    const directory = await temporaryDirectory();
    await writeFile(
      path.join(directory, "plugin.mjs"),
      pluginDeclaration().replace(
        'execute() { return {stdout: "", exitCode: 0}; }',
        'execute() { return {stdout: 1, exitCode: 300}; }',
      ),
      "utf8",
    );
    const result = invokeHost(directory, {
      operation: "execute",
      outputBudgetBytes: 16 * 1024 * 1024,
      module: "plugin.mjs",
      index: 0,
      arguments: [],
    });
    expect(result.status).toBe(1);
    expect(result.stderr).toContain(
      "executor must return stdout/stderr strings and an exitCode from 0 through 255",
    );
  });

});

for (const result of [
  {exitCode: 0, terminationReason: "output_limit"},
  {exitCode: 1, terminationReason: "unknown"},
  {exitCode: 1, terminationReason: null},
]) {
  test(`rejects invalid executor termination metadata ${JSON.stringify(result)}`, async () => {
    const directory = await temporaryDirectory();
    const declaration = pluginDeclaration().replace(
      'return {stdout: "", exitCode: 0};',
      `return ${JSON.stringify(result)};`,
    );
    await writeFile(path.join(directory, "plugin.mjs"), declaration);
    const response = invokeHost(directory, {
      operation: "execute", module: "plugin.mjs", index: 0, arguments: [], outputBudgetBytes: 1024,
    });
    expect(response.status).toBe(1);
    expect(response.stderr).toContain("terminationReason must be output_limit with nonzero exitCode");
  });
}

describe("authoritative Rust regex validation", () => {
  const valid = [
    "[+?]", "[()?!+*]", "[]?+*]+", "[^]?+*]+", "[a-z&&[^aeiou]]+", "[[:alpha:]]+",
    String.raw`\(\?=`, String.raw`\+\?`, String.raw`\+?`, String.raw`\}?`,
    String.raw`\p{Letter}?`, String.raw`\p{Script=Greek}+`, String.raw`\u{1F600}`,
    String.raw`\Afoo\nbar\z`, "(?i:foo)(?-i:Bar)", "(?m)^foo$", "(?s:.)", "(?U:a+)",
    "(?-x:foo)", "[(?x)]", String.raw`\b{start}?`, "(?P<n[>a)", "a{1,3}", "a{2}",
  ];
  for (const definition of valid) {
    test(`accepts Rust grammar ${JSON.stringify(definition)}`, async () => {
      const {response} = await validateDeclaration(pluginDeclaration({type: "grammar", syntax: "regex", definition}));
      expect(response.errors).toEqual([]);
      expect(response.plugins).toHaveLength(1);
    });
  }
  for (const definition of [
    "[z-a]", String.raw`\q`, String.raw`(a)\1`, "(?=a)", "(?<=a)", "[[]", "a{3,1}",
    "a*?", "a+?", "a??", "a{2}?", "a{1,3}?", "[^^]+?", "[a-z&&[^x]]+?", "(?i:a+?)",
    "(?P<n[>a+?)", "(?<n]>a+?)", "(?x:a)", "(?x)a", "(?i-x:a)(?x:b)",
  ]) {
    test(`rejects unsupported Rust grammar ${JSON.stringify(definition)}`, async () => {
      const {response} = await validateDeclaration(pluginDeclaration({type: "grammar", syntax: "regex", definition}));
      expect(response.plugins).toEqual([]);
      expect(response.errors.join("\n")).toMatch(/Rust regex validation failed|unsupported/u);
    });
  }
  test("passes large patterns through stdin rather than operating-system argv", async () => {
    const {response} = await validateDeclaration(pluginDeclaration({
      type: "grammar", syntax: "regex", definition: "a".repeat(150_000),
    }));
    expect(response.errors).toEqual([]);
  });
  test.each([
    ["start: /[+?]/", true],
    ["start: /[z-a]/", false],
    [String.raw`start: /\q/`, false],
    ["start: /a+?/", false],
    ["start: /ok/i", true],
    ["start: /ok/x", false],
    ["start: /ok/z", false],
  ])("validates Lark regex terminals with the same Rust owner: %s", async (definition, accepted) => {
    const {response} = await validateDeclaration(pluginDeclaration({type: "grammar", syntax: "lark", definition}));
    expect(response.errors.length === 0).toBe(accepted);
  });
  test.each([
    [undefined, true],
    [{type: "grammar", syntax: "lark", definition: 'start: "ok"'}, true],
    [{type: "grammar", syntax: "regex", definition: "ok"}, false],
    [{type: "grammar", syntax: "lark", definition: "start: /ok/"}, false],
  ])("requires rg only when the declaration contains a regex", async (format, accepted) => {
    const directory = await temporaryDirectory();
    await writeFile(path.join(directory, "plugin.mjs"), pluginDeclaration(format));
    const result = invokeHost(directory, {operation: "validate", modules: ["plugin.mjs"], regexValidator: ""});
    const response = JSON.parse(result.stdout) as HostResponse;
    expect(result.status).toBe(0);
    expect(response.errors.length === 0).toBe(accepted);
    if (!accepted) expect(response.errors.join("\n")).toContain("requires ripgrep (rg) on the router PATH");
  });
  test("reports missing and unusable validator executables", async () => {
    const directory = await temporaryDirectory();
    await writeFile(path.join(directory, "plugin.mjs"), pluginDeclaration({type: "grammar", syntax: "regex", definition: "ok"}));
    const denied = path.join(directory, "denied");
    await writeFile(denied, "not an executable", {mode: 0o600});
    for (const [regexValidator, diagnostic] of [[path.join(directory, "missing"), "requires ripgrep"], [denied, "cannot validate Rust regex"]]) {
      const result = invokeHost(directory, {operation: "validate", modules: ["plugin.mjs"], regexValidator});
      const response = JSON.parse(result.stdout) as HostResponse;
      expect(response.plugins).toEqual([]);
      expect(response.errors.join("\n")).toContain(diagnostic);
    }
  });
});

test("regex validation ignores ripgrep configuration and cannot switch to PCRE", async () => {
  const directory = await temporaryDirectory();
  const config = path.join(directory, "ripgrep.conf");
  await writeFile(config, "--pcre2\n");
  await writeFile(path.join(directory, "plugin.mjs"), pluginDeclaration({type: "grammar", syntax: "regex", definition: "(?=a)a"}));
  const result = invokeHost(directory, {operation: "validate", modules: ["plugin.mjs"]}, {...process.env, RIPGREP_CONFIG_PATH: config});
  const response = JSON.parse(result.stdout) as HostResponse;
  expect(response.plugins).toEqual([]);
  expect(response.errors.join("\n")).toContain("look-around");
});

test("bounds a stalled regex validator without retaining its child", async () => {
  const directory = await temporaryDirectory();
  const validator = path.join(directory, "validator");
  await writeFile(validator, "#!/bin/sh\nexec /bin/sleep 10\n");
  await chmod(validator, 0o700);
  await writeFile(path.join(directory, "plugin.mjs"), pluginDeclaration({type: "grammar", syntax: "regex", definition: "ok"}));
  const started = performance.now();
  const result = invokeHost(directory, {operation: "validate", modules: ["plugin.mjs"], regexValidator: validator});
  const response = JSON.parse(result.stdout) as HostResponse;
  expect(response.plugins).toEqual([]);
  expect(response.errors.join("\n")).toContain("cannot validate Rust regex grammar");
  expect(performance.now() - started).toBeLessThan(3_000);
});

for (const result of [
  {exitCode: 0, failureClass: "not_found"},
  {exitCode: 1, failureClass: "private /path stderr"},
  {exitCode: 1, failureClass: null},
  {exitCode: 1, failureClass: {code: "not_found"}},
]) {
  test(`rejects unsafe reader failure metadata ${JSON.stringify(result)}`, async () => {
    const directory = await temporaryDirectory();
    await writeFile(path.join(directory, "plugin.mjs"), pluginDeclaration().replace(
      'return {stdout: "", exitCode: 0};', `return ${JSON.stringify(result)};`,
    ));
    const response = invokeHost(directory, {
      operation: "execute", module: "plugin.mjs", index: 0, arguments: [], outputBudgetBytes: 1024,
    });
    expect(response.status).toBe(1);
    expect(response.stderr).toContain("failureClass must be allowlisted with a nonzero exitCode");
    expect(response.stderr).not.toContain("private /path stderr");
  });
}

test("passes allowlisted failure metadata without changing command output", async () => {
  const directory = await temporaryDirectory();
  await writeFile(path.join(directory, "plugin.mjs"), pluginDeclaration().replace(
    'return {stdout: "", exitCode: 0};', 'return {stdout: "", stderr: "original diagnostic", exitCode: 1, failureClass: "not_found"};',
  ));
  const response = invokeHost(directory, {
    operation: "execute", module: "plugin.mjs", index: 0, arguments: [], outputBudgetBytes: 1024,
  });
  expect(response.status).toBe(0);
  expect(JSON.parse(response.stdout)).toEqual({stdout: "", stderr: "original diagnostic", exitCode: 1, failureClass: "not_found"});
});
