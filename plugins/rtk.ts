// Executor-side routing policy. The shell owns parsing, expansion and execution;
// this plugin only maps already-expanded argv, without rebuilding shell source.
const subcommands: Record<string, readonly string[]> = {
  git: ["diff", "log", "status", "show", "commit", "checkout", "push", "pull", "branch", "fetch", "stash", "worktree"],
  go: ["test", "build", "vet"],
  cargo: ["build", "test", "clippy", "check", "install", "nextest"],
  bun: ["install", "run", "build", "test", "add", "remove", "pm", "x"],
  pnpm: ["list", "outdated", "install"],
  npm: ["run"],
  docker: ["ps", "images", "logs", "compose"],
  kubectl: ["get", "logs"],
  oc: ["get", "logs"],
  dotnet: ["build", "test", "restore", "format"],
  deno: ["run", "check", "lint", "test", "task", "compile", "install"],
  gh: ["pr", "issue", "run", "repo"],
  next: ["build"],
  uv: ["run"],
};

const direct = new Set([
  "ls", "tree", "rg", "wget", "wc", "curl", "aws", "psql",
  "jest", "vitest", "ctest", "prisma", "tsc", "prettier", "playwright",
  "ruff", "sqlfluff", "pytest", "mypy", "phpunit", "phpstan", "pest",
  "paratest", "ecs", "pint", "phpt", "rake", "rubocop", "rspec",
  "sbt", "golangci-lint", "mvn", "mvnd", "npx", "bunx",
]);

export function commandRouting(): {executable: string; commands: string[]} {
  return {executable: "rtk", commands: [...Object.keys(subcommands), ...direct, "eslint", "timeout", "env", "nice"]};
}

function subcommandIndex(argv: string[]): number {
  if (argv[0] !== "git" && argv[0] !== "pnpm") return 1;
  let index = 1;
  const valued = argv[0] === "git"
    ? ["-C", "-c", "--git-dir", "--work-tree"]
    : ["-F", "--filter"];
  const flags = argv[0] === "git"
    ? ["--no-pager", "--no-optional-locks", "--bare", "--literal-pathspecs"]
    : [];
  while (index < argv.length && argv[index].startsWith("-")) {
    const argument = argv[index];
    if (valued.includes(argument)) index += 2;
    else if (flags.includes(argument) || valued.some((flag) => flag.startsWith("--") && argument.startsWith(`${flag}=`))) index++;
    else return -1;
  }
  return index;
}

// Recognize only wrapper forms whose command boundary and lookup environment
// are known. Keep wrappers outside RTK so they still own timing and priority.
function wrappedCommandIndex(argv: string[]): number {
  let index = 1;
  if (argv[0] === "env") {
    if (argv[index] === "--") index++;
    while (index < argv.length && /^[A-Za-z_][A-Za-z0-9_]*=/u.test(argv[index])) {
      // A changed PATH could hide RTK even when it exists in the outer shell.
      if (argv[index].startsWith("PATH=")) return -1;
      index++;
    }
    return index < argv.length && !argv[index].startsWith("-") ? index : -1;
  }
  if (argv[0] === "timeout") {
    while (index < argv.length && argv[index].startsWith("-")) {
      const argument = argv[index++];
      // Foreground timeout signals only its direct child, not RTK's child.
      if (argument === "--foreground") return -1;
      if (argument === "--") break;
      if (["-s", "--signal", "-k", "--kill-after"].includes(argument)) {
        if (index >= argv.length) return -1;
        index++;
      } else if (!["--preserve-status", "-v", "--verbose"].includes(argument)
        && !/^(?:--signal|--kill-after)=.+/u.test(argument)
        && !/^-[sk].+/u.test(argument)) return -1;
    }
    if (!/^(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)[smhd]?$/u.test(argv[index] ?? "")) return -1;
    return index + 1 < argv.length ? index + 1 : -1;
  }
  if (argv[0] === "nice") {
    if (argv[index] === "-n" || argv[index] === "--adjustment") {
      index++;
      if (!/^[+-]?[0-9]+$/u.test(argv[index] ?? "")) return -1;
      index++;
    } else if (/^(?:--adjustment=|-[n]?)[+-]?[0-9]+$/u.test(argv[index] ?? "")) index++;
    if (argv[index] === "--") index++;
    return index < argv.length && !argv[index].startsWith("-") ? index : -1;
  }
  return -1;
}

export function rewriteCommand(argv: string[]): {arguments: string[]} {
  let offset = 0;
  while (["timeout", "env", "nice"].includes(argv[offset])) {
    const index = wrappedCommandIndex(argv.slice(offset));
    if (index < 0) return {arguments: argv};
    offset += index;
  }
  const inner = rewriteDisplayCommand(argv.slice(offset));
  return {arguments: [...argv.slice(0, offset), ...inner.arguments]};
}

function rewriteDisplayCommand(argv: string[]): {arguments: string[]} {
  const [name] = argv;
  const unchanged = {arguments: argv};
  // Explicit machine-readable output and help are not display summaries.
  if (argv.some((arg) => [
    "--help", "--version", "--json", "-json", "--jsonl", "--porcelain", "--null", "-0", "-z",
    "--null-data", "--print0", "-print0", "--format", "--pretty", "--raw",
  ].includes(arg) || /^(?:--json|--porcelain|--format|--pretty)=/u.test(arg))) return unchanged;
  if (name === "eslint") return {arguments: ["rtk", "lint", ...argv]};
  if (direct.has(name)) return {arguments: ["rtk", ...argv]};
  if (!Object.hasOwn(subcommands, name)) return unchanged;
  const index = subcommandIndex(argv);
  if (index < 0 || !subcommands[name].includes(argv[index])) return unchanged;
  // RTK treats diff output as patch text and can discard --check diagnostics.
  const separator = argv.indexOf("--", index + 1);
  if (name === "git" && argv[index] === "diff"
    && argv.slice(index + 1, separator < 0 ? undefined : separator).includes("--check")) return unchanged;
  return {arguments: ["rtk", ...argv]};
}
