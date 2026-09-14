import { spawnSync } from "node:child_process";
import { realpath } from "node:fs/promises";
import { registerHooks } from "node:module";
import path from "node:path";
import process from "node:process";
import { createInterface } from "node:readline";
import { fileURLToPath, pathToFileURL } from "node:url";

const API_VERSION = "mekugi-tool-plugin/v1";
const MAX_GRAMMAR_BYTES = 1024 * 1024;
const MAX_DIAGNOSTIC_BYTES = 16 * 1024;
let regexValidator = "rg";
const identifierPattern = /^[A-Za-z][A-Za-z0-9._-]*$/;
const toolNamePattern = /^[A-Za-z_][A-Za-z0-9_-]*$/;
const ruleHeaderPattern = /^[!?]?([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$/s;
const priorityHeaderPattern = /^[!?]?[A-Za-z_][A-Za-z0-9_]*\.-?[0-9]+\s*:/;
const commonImportPattern = /^%import\s+common\.[A-Za-z_][A-Za-z0-9_]*(?:\s*->\s*[A-Za-z_][A-Za-z0-9_]*)?$/;

const shellCommandNames = new Set([
  "alias", "bg", "bind", "break", "builtin", "caller", "case", "cd", "command",
  "compgen", "complete", "compopt", "continue", "coproc", "declare", "dirs", "disown",
  "do", "done", "echo", "elif", "else", "enable", "esac", "eval", "exec", "exit",
  "export", "false", "fc", "fg", "fi", "for", "function", "getopts", "hash", "help",
  "history", "if", "in", "jobs", "kill", "let", "local", "logout", "mapfile", "popd",
  "printf", "pushd", "pwd", "read", "readarray", "readonly", "return", "select", "set",
  "shift", "shopt", "source", "suspend", "test", "then", "time", "times", "trap",
  "true", "type", "typeset", "ulimit", "umask", "unalias", "unset", "until", "wait",
  "while", "hrun",
]);

function byteLength(value) {
  return Buffer.byteLength(value, "utf8");
}

function exactKeys(value, allowed) {
  const actual = Object.keys(value).sort();
  const expected = [...allowed].sort();
  return actual.length === expected.length && actual.every((key, index) => key === expected[index]);
}

function inside(root, candidate) {
  const relative = path.relative(root, candidate);
  return relative === "" || (!relative.startsWith(`..${path.sep}`) && relative !== ".." && !path.isAbsolute(relative));
}

function errorText(error) {
  const text = error?.message ?? String(error);
  const encoded = Buffer.from(text, "utf8");
  if (encoded.length <= MAX_DIAGNOSTIC_BYTES) {
    return text;
  }
  return encoded.subarray(0, MAX_DIAGNOSTIC_BYTES).toString("utf8");
}

// Syntax belongs to ripgrep's Rust engine. This scanner checks only provider
// modifiers after compilation, without mistaking escaped text or class members
// for operators. Extended mode is excluded by the provider's one-line contract.
function unsupportedRegexModifier(definition) {
  const classes = [];
  let repetition = false;
  for (let index = 0; index < definition.length; index += 1) {
    const character = definition[index];
    if (character === "\\") {
      const escaped = definition[++index];
      if ("pPxuUb".includes(escaped) && definition[index + 1] === "{") {
        index = definition.indexOf("}", index + 2);
      }
      if (classes.length > 0) classes[classes.length - 1] = "body";
      repetition = false;
      continue;
    }
    if (classes.length > 0) {
      const first = classes.at(-1);
      if (character === "[") {
        classes[classes.length - 1] = "body";
        classes.push("start");
      } else if (character === "]" && first === "body") {
        classes.pop();
      } else {
        classes[classes.length - 1] = character === "^" && first === "start" ? "negated" : "body";
      }
      continue;
    }
    if (character === "[") {
      classes.push("start");
      repetition = false;
      continue;
    }
    if (character === "(" && definition[index + 1] === "?") {
      if (definition.startsWith("(?P<", index) || definition.startsWith("(?<", index)) {
        index = definition.indexOf(">", index + 3);
        repetition = false;
        continue;
      }
      const flags = /^\(\?([imsRUux-]*)(?:[:)])/u.exec(definition.slice(index));
      if (flags !== null) {
        if (flags[1].split("-", 1)[0].includes("x")) {
          return "regex grammar uses unsupported extended mode";
        }
        index += flags[0].length - 1;
      }
      repetition = false;
      continue;
    }
    if (character === "?" && repetition) {
      return "regex grammar uses unsupported construct: lazy repetition";
    }
    if (character === "{") {
      index = definition.indexOf("}", index + 1);
      repetition = true;
      continue;
    }
    repetition = character === "*" || character === "+" || character === "?";
  }
  return null;
}

function validateRegexGrammar(definition) {
  if (/[\r\n]/u.test(definition)) {
    return "regex grammar must be one line";
  }
  if (regexValidator === "") {
    return "regex grammar validation requires ripgrep (rg) on the router PATH";
  }
  // Patterns travel on stdin, not argv: the admitted grammar size can exceed
  // the operating system's per-argument limit. Never search workspace files.
  const result = spawnSync(regexValidator, [
    "--no-config", "--engine", "default", "--multiline", "--quiet",
    "--file", "-", "--", process.platform === "win32" ? "NUL" : "/dev/null",
  ], {
    input: `${definition}\n`, encoding: "utf8", timeout: 1_000,
    killSignal: "SIGKILL", maxBuffer: MAX_DIAGNOSTIC_BYTES,
  });
  if (result.error !== undefined) {
    return result.error.code === "ENOENT"
      ? "regex grammar validation requires ripgrep (rg) on the router PATH"
      : `cannot validate Rust regex grammar: ${errorText(result.error)}`;
  }
  if (result.status !== 0 && result.status !== 1) {
    return `Rust regex validation failed: ${errorText(result.stderr.trim().split("\n\n", 1)[0] || `validator exited with status ${result.status}`)}`;
  }
  return unsupportedRegexModifier(definition);
}

function tokenizeLarkExpression(expression) {
  const tokens = [];
  for (let index = 0; index < expression.length;) {
    const character = expression[index];
    if (/\s/.test(character)) {
      index += 1;
      continue;
    }
    if (expression.startsWith("//", index)) {
      break;
    }
    if (expression.startsWith("->", index)) {
      tokens.push({kind: "->"});
      index += 2;
      continue;
    }
    if (expression.startsWith("..", index)) {
      tokens.push({kind: ".."});
      index += 2;
      continue;
    }
    if (character === "'" || character === '"') {
      const quote = character;
      let escaped = false;
      let closed = false;
      index += 1;
      while (index < expression.length) {
        const next = expression[index];
        index += 1;
        if (escaped) {
          escaped = false;
        } else if (next === "\\") {
          escaped = true;
        } else if (next === quote) {
          closed = true;
          break;
        }
      }
      if (!closed) {
        throw new Error("unterminated string terminal");
      }
      tokens.push({kind: "atom"});
      continue;
    }
    if (character === "/") {
      let escaped = false;
      let closed = false;
      let body = "";
      index += 1;
      while (index < expression.length) {
        const next = expression[index];
        index += 1;
        if (escaped) {
          body += `\\${next}`;
          escaped = false;
        } else if (next === "\\") {
          escaped = true;
        } else if (next === "/") {
          closed = true;
          break;
        } else {
          body += next;
        }
      }
      if (!closed) {
        throw new Error("unterminated regex terminal");
      }
      const flagsStart = index;
      while (index < expression.length && /[A-Za-z]/.test(expression[index])) {
        index += 1;
      }
      const flags = expression.slice(flagsStart, index);
      const regexError = validateRegexGrammar(flags === "" ? body : `(?${flags}:${body})`);
      if (regexError !== null) {
        throw new Error(regexError);
      }
      tokens.push({kind: "atom"});
      continue;
    }
    const identifier = expression.slice(index).match(/^\$?[A-Za-z_][A-Za-z0-9_]*/);
    if (identifier !== null) {
      tokens.push({kind: "identifier"});
      index += identifier[0].length;
      continue;
    }
    const number = expression.slice(index).match(/^[0-9]+/);
    if (number !== null) {
      tokens.push({kind: "number"});
      index += number[0].length;
      continue;
    }
    if ("()[]|?*+~".includes(character)) {
      tokens.push({kind: character});
      index += 1;
      continue;
    }
    if (character === "{" || character === "}") {
      throw new Error("template syntax is unsupported");
    }
    throw new Error(`unexpected token ${JSON.stringify(character)}`);
  }
  return tokens;
}

function validateLarkExpression(expression) {
  const tokens = tokenizeLarkExpression(expression);
  let position = 0;

  function parseAlternatives(closing) {
    parseExpansion(closing);
    while (tokens[position]?.kind === "|") {
      position += 1;
      parseExpansion(closing);
    }
  }

  function parseExpansion(closing) {
    let atoms = 0;
    while (position < tokens.length) {
      const kind = tokens[position].kind;
      if (kind === "|" || kind === closing || kind === "->") {
        break;
      }
      parseAtom();
      atoms += 1;
    }
    if (atoms === 0) {
      throw new Error("empty grammar expansion");
    }
    if (tokens[position]?.kind === "->") {
      position += 1;
      if (tokens[position]?.kind !== "identifier") {
        throw new Error("alias requires a rule name");
      }
      position += 1;
    }
  }

  function parseAtom() {
    const token = tokens[position];
    if (token === undefined) {
      throw new Error("missing grammar atom");
    }
    if (token.kind === "atom" || token.kind === "identifier") {
      position += 1;
    } else if (token.kind === "(" || token.kind === "[") {
      const closing = token.kind === "(" ? ")" : "]";
      position += 1;
      parseAlternatives(closing);
      if (tokens[position]?.kind !== closing) {
        throw new Error(`missing ${JSON.stringify(closing)}`);
      }
      position += 1;
    } else {
      throw new Error(`unexpected ${JSON.stringify(token.kind)} in grammar expression`);
    }

    if (tokens[position]?.kind === "..") {
      position += 1;
      if (tokens[position]?.kind !== "atom") {
        throw new Error("literal range requires a terminal");
      }
      position += 1;
    }
    if (["?", "*", "+"].includes(tokens[position]?.kind)) {
      position += 1;
    } else if (tokens[position]?.kind === "~") {
      position += 1;
      if (tokens[position]?.kind !== "number") {
        throw new Error("bounded repetition requires a number");
      }
      position += 1;
      if (tokens[position]?.kind === "..") {
        position += 1;
        if (tokens[position]?.kind !== "number") {
          throw new Error("bounded repetition range requires a number");
        }
        position += 1;
      }
    }
  }

  parseAlternatives(undefined);
  if (position !== tokens.length) {
    throw new Error(`unexpected ${JSON.stringify(tokens[position].kind)} after grammar expression`);
  }
}

function larkStatements(definition) {
  const statements = [];
  let current = "";
  for (const physical of definition.split(/\r?\n/)) {
    const line = physical.trim();
    if (line === "" || line.startsWith("//")) {
      continue;
    }
    const indented = /^\s/.test(physical);
    const continuation = indented || line.startsWith("|") || line.startsWith(")") || line.startsWith("]");
    if (current !== "" && !continuation) {
      statements.push(current);
      current = "";
    }
    current += `${current === "" ? "" : " "}${line}`;
  }
  if (current !== "") {
    statements.push(current);
  }
  return statements;
}

function validateLarkGrammar(definition) {
  let hasStart = false;
  const declarations = new Set();
  for (const statement of larkStatements(definition)) {
    if (statement.startsWith("%import")) {
      if (!commonImportPattern.test(statement)) {
        return "Lark grammar imports outside common";
      }
      continue;
    }
    if (statement.startsWith("%declare")) {
      return "Lark grammar uses unsupported %declare";
    }
    if (statement.startsWith("%ignore")) {
      const expression = statement.slice("%ignore".length).trim();
      if (expression === "") {
        return "Lark grammar has an empty %ignore expression";
      }
      try {
        validateLarkExpression(expression);
      } catch (error) {
        return `invalid Lark %ignore expression: ${errorText(error)}`;
      }
      continue;
    }

    let modifiesDeclaration = false;
    let rule = statement;
    if (rule.startsWith("%extend ") || rule.startsWith("%override ")) {
      modifiesDeclaration = true;
      rule = rule.slice(rule.indexOf(" ") + 1).trim();
    } else if (rule.startsWith("%")) {
      return "Lark grammar uses an unsupported directive";
    }
    if (priorityHeaderPattern.test(rule)) {
      return "Lark grammar uses an unsupported priority";
    }
    const match = rule.match(ruleHeaderPattern);
    if (match === null) {
      return "Lark grammar contains an invalid rule declaration";
    }
    if (!modifiesDeclaration) {
      if (declarations.has(match[1])) {
        return `Lark declaration ${JSON.stringify(match[1])} is defined more than once`;
      }
      declarations.add(match[1]);
    }
    hasStart ||= match[1] === "start";
    if (match[2] === "") {
      return `Lark rule ${JSON.stringify(match[1])} has an empty expression`;
    }
    try {
      validateLarkExpression(match[2]);
    } catch (error) {
      return `invalid Lark rule ${JSON.stringify(match[1])}: ${errorText(error)}`;
    }
  }
  if (!hasStart) {
    return "Lark grammar does not define start";
  }
  return null;
}

function validateSpecification(specification, label, errors) {
  if (specification === null || typeof specification !== "object" || Array.isArray(specification)) {
    errors.push(`${label}: specification must be an object`);
    return null;
  }
  const allowed = specification.format === undefined
    ? ["type", "name", "description"]
    : ["type", "name", "description", "format"];
  if (!exactKeys(specification, allowed)) {
    errors.push(`${label}: specification contains unsupported or missing fields`);
    return null;
  }
  if (specification.type !== "custom") {
    errors.push(`${label}: specification type must be "custom"`);
  }
  if (typeof specification.name !== "string" || !toolNamePattern.test(specification.name)) {
    errors.push(`${label}: specification name is invalid`);
  } else if (shellCommandNames.has(specification.name)) {
    errors.push(`${label}: specification name collides with a shell keyword or built-in`);
  }
  if (typeof specification.description !== "string" || specification.description === "") {
    errors.push(`${label}: specification description must not be empty`);
  }
  if (specification.format !== undefined) {
    const format = specification.format;
    if (format === null || typeof format !== "object" || Array.isArray(format)
        || !exactKeys(format, ["type", "syntax", "definition"])) {
      errors.push(`${label}: specification format must contain only type, syntax, and definition`);
    } else if (typeof format.definition !== "string" || byteLength(format.definition) === 0 || byteLength(format.definition) > MAX_GRAMMAR_BYTES) {
      errors.push(`${label}: grammar definition must contain 1 to ${MAX_GRAMMAR_BYTES} UTF-8 bytes`);
    } else if (format.type !== "grammar" || (format.syntax !== "lark" && format.syntax !== "regex")) {
      errors.push(`${label}: specification format must be a lark or regex grammar`);
    } else {
      const grammarError = format.syntax === "regex"
        ? validateRegexGrammar(format.definition)
        : validateLarkGrammar(format.definition);
      if (grammarError !== null) {
        errors.push(`${label}: ${grammarError}`);
      }
    }
  }
  return specification;
}

function validateTool(tool, modulePath, index, errors) {
  const label = `${modulePath}: tool ${index + 1}`;
  if (tool === null || typeof tool !== "object" || Array.isArray(tool)
      || !exactKeys(tool, ["specification", "parse", "argv", "translate", "execute"])) {
    errors.push(`${label}: declaration contains unsupported or missing fields`);
    return null;
  }
  const specification = validateSpecification(tool.specification, label, errors);
  for (const name of ["parse", "argv", "translate", "execute"]) {
    if (typeof tool[name] !== "function") {
      errors.push(`${label}: ${name} must be a function`);
    }
  }
  if (specification === null || errors.some((message) => message.startsWith(`${label}:`))) {
    return null;
  }
  return {specification};
}

async function loadDeclaration(snapshotRoot, modulePath) {
  const absolute = path.resolve(snapshotRoot, modulePath);
  if (!inside(snapshotRoot, absolute)) {
    throw new Error(`${modulePath}: declaration escapes the plugin snapshot`);
  }
  return (await import(pathToFileURL(absolute).href)).default;
}

async function validateModule(snapshotRoot, modulePath) {
  const errors = [];
  let declaration;
  try {
    declaration = await loadDeclaration(snapshotRoot, modulePath);
  } catch (error) {
    return {errors: [`${modulePath}: cannot load declaration: ${errorText(error)}`], plugin: null};
  }
  if (declaration === null || typeof declaration !== "object" || Array.isArray(declaration)
      || !exactKeys(declaration, ["apiVersion", "id", "tools"])) {
    return {
      errors: [`${modulePath}: default export must contain only apiVersion, id, and tools`],
      plugin: null,
    };
  }
  if (declaration.apiVersion !== API_VERSION) {
    errors.push(`${modulePath}: apiVersion must be "${API_VERSION}"`);
  }
  if (typeof declaration.id !== "string" || !identifierPattern.test(declaration.id)) {
    errors.push(`${modulePath}: id is invalid`);
  }
  if (!Array.isArray(declaration.tools) || declaration.tools.length === 0) {
    errors.push(`${modulePath}: tools must not be empty`);
  }
  const tools = [];
  if (Array.isArray(declaration.tools)) {
    for (const [index, tool] of declaration.tools.entries()) {
      const normalized = validateTool(tool, modulePath, index, errors);
      if (normalized !== null) {
        tools.push(normalized);
      }
    }
  }
  return {
    errors,
    plugin: errors.length === 0 ? {id: declaration.id, module: modulePath, tools} : null,
  };
}

async function loadTool(snapshotRoot, modulePath, index) {
  const declaration = await loadDeclaration(snapshotRoot, modulePath);
  if (declaration?.apiVersion !== API_VERSION || !Array.isArray(declaration.tools)) {
    throw new Error("snapshotted plugin declaration is invalid");
  }
  const tool = declaration.tools[index];
  if (tool === null || typeof tool !== "object") {
    throw new Error(`snapshotted plugin tool ${index + 1} is unavailable`);
  }
  return tool;
}

function validateArguments(argumentsValue) {
  if (!Array.isArray(argumentsValue)
      || argumentsValue.some((argument) => typeof argument !== "string")) {
    throw new Error("argv must contain only strings");
  }
  return argumentsValue;
}

function isJSONNativeValue(value, seen = new Set()) {
  if (value === null || typeof value === "string" || typeof value === "boolean") {
    return true;
  }
  if (typeof value === "number") {
    return Number.isFinite(value);
  }
  if (typeof value !== "object" || seen.has(value)) {
    return false;
  }
  if (!Array.isArray(value)) {
    const prototype = Object.getPrototypeOf(value);
    if (prototype !== Object.prototype && prototype !== null) {
      return false;
    }
  }
  seen.add(value);
  const valid = Object.keys(value).every((key) => isJSONNativeValue(value[key], seen));
  seen.delete(value);
  return valid;
}

function validateExecParams(params) {
  if (params === null || typeof params !== "object" || Array.isArray(params)
      || Object.hasOwn(params, "cmd")) {
    throw new Error("exec carrier params must be an object without cmd");
  }
  if (Object.hasOwn(params, "login") && params.login !== false) {
    throw new Error("exec carrier params login must be false");
  }
  let snapshot;
  try {
    if (!isJSONNativeValue(params)) {
      throw new Error("not JSON-native");
    }
    snapshot = JSON.parse(JSON.stringify(params));
  } catch {
    throw new Error("exec carrier params must contain only JSON-native values");
  }
  return snapshot;
}

function validateCarrier(carrier) {
  if (carrier === null || typeof carrier !== "object" || Array.isArray(carrier)) {
    throw new Error("translator must return a carrier object");
  }
  if (carrier.kind === "exec") {
    const keys = Object.keys(carrier);
    if (!keys.every((key) => ["kind", "template", "params", "retainInput"].includes(key))) {
      throw new Error("translator returned a malformed carrier");
    }
    const normalized = {kind: "exec"};
    if (carrier.template !== undefined) {
      if (typeof carrier.template !== "string"
          || carrier.template.split("{.}").length !== 2) {
        throw new Error("translator returned a malformed carrier");
      }
      normalized.template = carrier.template;
    }
    if (carrier.params !== undefined) {
      normalized.params = validateExecParams(carrier.params);
    }
    if (carrier.retainInput !== undefined) {
      if (typeof carrier.retainInput !== "boolean") {
        throw new Error("translator returned a malformed carrier");
      }
      normalized.retainInput = carrier.retainInput;
    }
    return Object.freeze(normalized);
  }
  if ((carrier.kind === "custom" || carrier.kind === "function")
      && exactKeys(carrier, ["kind", "name", "payload"])
      && typeof carrier.name === "string"
      && toolNamePattern.test(carrier.name)
      && typeof carrier.payload === "string") {
    return carrier;
  }
  throw new Error("translator returned a malformed carrier");
}

async function translateTool(request) {
  const context = Object.freeze({
    resolvePath(path) {
      return request.pathPrefix !== "" && typeof path === "string" && path.startsWith("@shell/")
        ? request.pathPrefix + path.slice("@shell/".length)
        : path;
    },
  });
  const tool = await loadTool(request.snapshotRoot, request.module, request.index);
  let parsed;
  try {
    parsed = await tool.parse(request.input, context);
  } catch (error) {
    return {rejected: true, diagnostic: errorText(error), arguments: [], carrier: {kind: "", name: "", payload: ""}};
  }
  const argumentsValue = validateArguments(await tool.argv(parsed, context));
  const api = Object.freeze({
    custom(name, input) {
      return Object.freeze({kind: "custom", name, payload: input});
    },
    function(name, argumentsJSON) {
      return Object.freeze({kind: "function", name, payload: argumentsJSON});
    },
    exec(template, params, retainInput) {
      const carrier = {kind: "exec"};
      if (template !== undefined) {
        carrier.template = template;
      }
      if (params !== undefined) {
        carrier.params = params;
      }
      if (retainInput !== undefined) {
        carrier.retainInput = retainInput;
      }
      return Object.freeze(carrier);
    },
  });
  const carrier = validateCarrier(await tool.translate(parsed, api, context));
  return {rejected: false, diagnostic: "", arguments: argumentsValue, carrier};
}

function normalizeExecutionOutput(candidate, allowedKeys) {
  try {
    if (candidate === null || typeof candidate !== "object" || Array.isArray(candidate)) {
      return null;
    }
    const keys = Object.keys(candidate);
    const stdout = candidate.stdout;
    const stderr = candidate.stderr;
    const exitCode = candidate.exitCode;
    if (!keys.every((key) => allowedKeys.includes(key))
        || !Number.isSafeInteger(exitCode) || exitCode < 0 || exitCode > 255
        || (stdout !== undefined && typeof stdout !== "string")
        || (stderr !== undefined && typeof stderr !== "string")) {
      return null;
    }
    return {stdout: stdout ?? "", stderr: stderr ?? "", exitCode};
  } catch {
    return null;
  }
}

async function executeTool(request) {
  const tool = await loadTool(request.snapshotRoot, request.module, request.index);
  const argumentsValue = validateArguments(request.arguments);
  const hasInput = request.inputFD === true;
  if (!Number.isSafeInteger(request.outputBudgetBytes) || request.outputBudgetBytes < 1) {
    throw new Error("executor output budget must be a positive integer");
  }
  const context = Object.freeze({
    stdinFD: hasInput ? 3 : null,
    scriptReadFD: hasInput ? 4 : null,
    scriptWriteFD: hasInput ? 5 : null,
    outputBudgetBytes: request.outputBudgetBytes,
  });
  const execution = await tool.execute(argumentsValue, context);
  const current = normalizeExecutionOutput(execution, ["stdout", "stderr", "exitCode", "terminationReason", "failureClass", "omittedOutput"]);
  if (current === null) {
    throw new Error("executor must return stdout/stderr strings and an exitCode from 0 through 255");
  }
  const failureClass = execution.failureClass;
  if (failureClass !== undefined) {
    if (current.exitCode === 0 || ![
      "invalid_arguments", "not_found", "permission_denied", "not_regular", "invalid_source",
      "reader_error", "search_error", "resolver_error", "dependency_unavailable", "no_editable_location", "output_limit",
    ].includes(failureClass)) {
      throw new Error("executor failureClass must be allowlisted with a nonzero exitCode");
    }
    current.failureClass = failureClass;
  }
  const terminationReason = execution.terminationReason;
  if (terminationReason !== undefined) {
    if (terminationReason !== "resolver_cleanup" && (terminationReason !== "output_limit" || current.exitCode === 0)) {
      throw new Error("executor terminationReason must be output_limit with nonzero exitCode or resolver_cleanup");
    }
    current.terminationReason = terminationReason;
  }
  const omitted = execution.omittedOutput;
  if (omitted !== undefined) {
    if (omitted === null || typeof omitted !== "object" || Array.isArray(omitted)
        || Object.keys(omitted).some((key) => !["stdout", "stderr", "stdoutKind", "stderrKind"].includes(key))
        || (omitted.stdoutKind !== undefined && !["rows", "json"].includes(omitted.stdoutKind))
        || (omitted.stderrKind !== undefined && !["rows", "json"].includes(omitted.stderrKind))
        || typeof omitted.stdout !== "string" || typeof omitted.stderr !== "string"
        || byteLength(omitted.stdout) + byteLength(omitted.stderr) > 16 * 1024 * 1024) {
      throw new Error("executor omittedOutput must contain bounded stdout/stderr strings");
    }
    current.omittedOutput = {stdout: omitted.stdout, stderr: omitted.stderr,
      ...(omitted.stdoutKind === undefined ? {} : {stdoutKind: omitted.stdoutKind}),
      ...(omitted.stderrKind === undefined ? {} : {stderrKind: omitted.stderrKind})};
  }
  const currentBytes = byteLength(current.stdout) + byteLength(current.stderr);
  if (currentBytes > request.outputBudgetBytes) {
    throw new Error(`executor stdout and stderr exceed ${request.outputBudgetBytes} UTF-8 bytes`);
  }
  return current;
}

async function registerSnapshot(root) {
  const snapshotRoot = await realpath(path.resolve(root));
  registerHooks({
    resolve(specifier, context, nextResolve) {
      if (specifier === "mekugi:core/v1") {
        return {
          shortCircuit: true,
          url: pathToFileURL(path.join(snapshotRoot, "builtin", "core-v1.mjs")).href,
        };
      }
      if (specifier.startsWith("mekugi:")) {
        throw new Error(`mekugi plugin module is not supported: ${specifier}`);
      }
      const resolved = nextResolve(specifier, context);
      if (resolved.url.startsWith("node:")) {
        return resolved;
      }
      if (!resolved.url.startsWith("file:")) {
        throw new Error(`plugin module protocol is not supported: ${resolved.url}`);
      }
      const filename = fileURLToPath(resolved.url);
      if (!inside(snapshotRoot, filename)) {
        throw new Error(`plugin module escapes the immutable snapshot: ${filename}`);
      }
      if (path.extname(filename) === ".js") {
        return {...resolved, format: "module"};
      }
      return resolved;
    },
  });
  return snapshotRoot;
}

// Only the router-owned built-in declaration uses a warm translation process.
// Configured declarations and executor effects retain their one-shot hosts.
async function serveTranslations() {
  const lines = createInterface({input: process.stdin, crlfDelay: Infinity});
  let snapshotRoot;
  let module;
  for await (const line of lines) {
    const request = JSON.parse(line);
    let response;
    if (snapshotRoot === undefined) {
      snapshotRoot = await registerSnapshot(request.snapshotRoot);
      module = request.module;
      await loadDeclaration(snapshotRoot, module);
      response = {ready: true};
    } else {
      response = await translateTool({...request, snapshotRoot, module});
    }
    await new Promise((resolve, reject) => {
      process.stdout.write(JSON.stringify(response) + "\n", (error) => error ? reject(error) : resolve());
    });
  }
}

async function main() {
  if (process.argv[2] === "--translate-server") {
    return serveTranslations();
  }
  const request = JSON.parse(await new Promise((resolve, reject) => {
    const chunks = [];
    process.stdin.on("data", (chunk) => chunks.push(chunk));
    process.stdin.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    process.stdin.on("error", reject);
  }));
  const snapshotRoot = await registerSnapshot(request.snapshotRoot);

  let response;
  switch (request.operation) {
    case "validate": {
      if (request.regexValidator !== undefined) {
        if (typeof request.regexValidator !== "string") {
          throw new Error("regex validator must be an executable path");
        }
        regexValidator = request.regexValidator;
      }
      const plugins = [];
      const errors = [];
      for (const modulePath of request.modules) {
        const result = await validateModule(snapshotRoot, modulePath);
        errors.push(...result.errors);
        if (result.plugin !== null) {
          plugins.push(result.plugin);
        }
      }
      response = {plugins, errors};
      break;
    }
    case "format-output": {
      const {formatHRunOutput} = await import(pathToFileURL(path.join(snapshotRoot, "builtin/hrun.js")).href);
      response = formatHRunOutput(validateArguments(request.arguments));
      break;
    }
    case "translate":
      response = await translateTool(request);
      break;
    case "execute":
      response = await executeTool(request);
      break;
    default:
      throw new Error(`unsupported plugin runtime operation ${JSON.stringify(request.operation)}`);
  }
  process.stdout.write(JSON.stringify(response));
}

main().catch((error) => {
  process.stderr.write(`${error?.stack ?? String(error)}\n`);
  process.exitCode = 1;
  process.stdin.destroy();
});
