import {describe, expect, test} from "bun:test";
import {commandRouting, rewriteCommand} from "../../../../plugins/rtk.ts";

describe("RTK argv routing policy", () => {
  test.each([
    [["git", "status", "--short"], ["rtk", "git", "status", "--short"]],
    [["git", "-C", "a b", "-c", "color.ui=false", "diff"], ["rtk", "git", "-C", "a b", "-c", "color.ui=false", "diff"]],
    [["timeout", "120", "go", "test"], ["timeout", "120", "rtk", "go", "test"]],
    [["timeout", "-k", "2", "--signal=TERM", "--", "120", "git", "status"], ["timeout", "-k", "2", "--signal=TERM", "--", "120", "rtk", "git", "status"]],
    [["env", "FOO=bar", "timeout", "120", "go", "test"], ["env", "FOO=bar", "timeout", "120", "rtk", "go", "test"]],
    [["nice", "-n", "5", "go", "test"], ["nice", "-n", "5", "rtk", "go", "test"]],
    [["nice", "--adjustment=5", "go", "test"], ["nice", "--adjustment=5", "rtk", "go", "test"]],
    [["git", "diff", "--", "--check"], ["rtk", "git", "diff", "--", "--check"]],
    [["go", "test", "./..."], ["rtk", "go", "test", "./..."]],
    [["cargo", "test", "--", "test name"], ["rtk", "cargo", "test", "--", "test name"]],
    [["npm", "run", "test", "--", "--watch=false"], ["rtk", "npm", "run", "test", "--", "--watch=false"]],
    [["pnpm", "--filter", "@app/web", "install"], ["rtk", "pnpm", "--filter", "@app/web", "install"]],
    [["eslint", "a b.ts"], ["rtk", "lint", "eslint", "a b.ts"]],
    [["eslint", "src"], ["rtk", "lint", "eslint", "src"]],
    [["npx", "tsc", "--noEmit"], ["rtk", "npx", "tsc", "--noEmit"]],
    [["rg", "a'\"$(touch nope)\n*", "file name"], ["rtk", "rg", "a'\"$(touch nope)\n*", "file name"]],
    [["uv", "run", "--project", "backend", "pytest"], ["rtk", "uv", "run", "--project", "backend", "pytest"]],
  ])("maps %j without shell quoting or parsing", (input, output) => {
    expect(rewriteCommand(input).arguments).toEqual(output);
  });

  test.each([
    ["rtk", "git", "status"], ["mcat", "file"], ["mread", "ref"],
    ["hgrep", "-F", "text"], ["hsymbol", "refs"], ["inspect_file", "."],
    ["mrun", "-n", "1", "--", "git", "status"], ["journal", "list"],
    ["cat", "file"], ["sed", "-n", "1p", "file"], ["jq", "."],
    ["/usr/bin/git", "status"], ["git", "rev-parse", "HEAD"],
    ["git", "--namespace=other", "status"], ["git", "-C"],
    ["go", "env"], ["go", "test", "-json"], ["npm", "install"],
    ["pnpm", "run", "custom"], ["git", "status", "--porcelain=v2"],
    ["gh", "pr", "list", "--json", "number"], ["ls", "--help"],
    ["git", "add"], ["git", "-C", "repo", "add"], ["git", "add", "-p"],
    ["git", "add", "-vp", "file.txt"], ["git", "add", "file.txt"],
    ["git", "add", "--interactive"], ["pnpm", "typecheck"],
    ["pnpm", "--filter", "@app/web", "typecheck"], ["find", "missing"],
    ["diff", "one.bin", "two.bin"],
    ["git", "diff", "--check"], ["git", "-c", "color.ui=false", "--no-pager", "diff", "--check"],
    ["timeout", "120", "git", "diff", "--check"], ["timeout", "120", "go", "test", "-json"],
    ["timeout", "--unknown", "120", "go", "test"], ["timeout", "--help"],
    ["timeout", "--foreground", "--signal=KILL", "1", "go", "test"],
    ["timeout", "--foreground", "-k", "1", "1", "go", "test"],
    ["timeout", "120"], ["timeout", "-k"], ["timeout", "invalid", "go", "test"],
    ["env", "PATH=/elsewhere", "go", "test"], ["env", "-i", "go", "test"],
    ["env", "--unset=PATH", "go", "test"], ["env", "FOO=bar"],
    ["nice", "-n", "invalid", "go", "test"], ["nice", "--help"],
    ["timeout", "120", "/usr/bin/go", "test"], ["timeout", "120", "rtk", "go", "test"],
    ["unknown", "test"],
  ])("leaves raw or unsupported argv %j unchanged", (...input) => {
    expect(rewriteCommand(input).arguments).toEqual(input);
  });

  test("advertises only routing candidates and does not require RTK to load", () => {
    const policy = commandRouting();
    expect(policy.executable).toBe("rtk");
    expect(policy.commands).toContain("git");
    for (const name of ["rtk", "mrun", "mcat", "mread", "hgrep", "hsymbol", "inspect_file", "cat"]) {
      expect(policy.commands).not.toContain(name);
    }
  });
});
