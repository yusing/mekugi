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
  return {executable: "rtk", commands: [...Object.keys(subcommands), ...direct, "eslint"]};
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

export function rewriteCommand(argv: string[]): {arguments: string[]} {
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
  return {arguments: ["rtk", ...argv]};
}
