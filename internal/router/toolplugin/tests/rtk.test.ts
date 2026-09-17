import {describe, expect, test} from "bun:test";
import {commandRouting, rewriteCommand} from "../../../../plugins/rtk.ts";

describe("RTK argv routing policy", () => {
  test.each([
    [["git", "status", "--short"], ["rtk", "git", "status", "--short"]],
    [["git", "-C", "a b", "-c", "color.ui=false", "diff"], ["rtk", "git", "-C", "a b", "-c", "color.ui=false", "diff"]],
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
    ["rtk", "git", "status"], ["hcat", "file"], ["hread", "ref"],
    ["hgrep", "-F", "text"], ["hsymbol", "refs"], ["inspect_file", "."],
    ["hrun", "-n", "1", "--", "git", "status"], ["journal", "list"],
    ["cat", "file"], ["sed", "-n", "1p", "file"], ["jq", "."],
    ["env", "git", "status"], ["/usr/bin/git", "status"], ["git", "rev-parse", "HEAD"],
    ["git", "--namespace=other", "status"], ["git", "-C"],
    ["go", "env"], ["go", "test", "-json"], ["npm", "install"],
    ["pnpm", "run", "custom"], ["git", "status", "--porcelain=v2"],
    ["gh", "pr", "list", "--json", "number"], ["ls", "--help"],
    ["git", "add"], ["git", "-C", "repo", "add"], ["git", "add", "-p"],
    ["git", "add", "-vp", "file.txt"], ["git", "add", "file.txt"],
    ["git", "add", "--interactive"], ["pnpm", "typecheck"],
    ["pnpm", "--filter", "@app/web", "typecheck"], ["find", "missing"],
    ["diff", "one.bin", "two.bin"],
    ["unknown", "test"],
  ])("leaves raw or unsupported argv %j unchanged", (...input) => {
    expect(rewriteCommand(input).arguments).toEqual(input);
  });

  test("advertises only routing candidates and does not require RTK to load", () => {
    const policy = commandRouting();
    expect(policy.executable).toBe("rtk");
    expect(policy.commands).toContain("git");
    for (const name of ["rtk", "hrun", "hcat", "hread", "hgrep", "hsymbol", "inspect_file", "cat"]) {
      expect(policy.commands).not.toContain(name);
    }
  });
});
