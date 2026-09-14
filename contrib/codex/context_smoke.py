#!/usr/bin/env python3
"""Live Codex shell, plaintext assignment, and router-restart smoke test."""

import argparse
import json
from pathlib import Path
import subprocess
import textwrap

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument(
    "--sandbox",
    choices=("read-only", "workspace-write", "danger-full-access"),
    default="read-only",
    help="Codex sandbox mode (default: read-only).",
)
sandbox = parser.parse_args().sandbox
repository = Path(__file__).resolve().parents[2]
test_dir = Path(subprocess.check_output(
    ["mktemp", "-d", "/tmp/mekugi-context-test-XXXXXX"], text=True
).strip())
print(f"Artifacts and launcher: {test_dir}", flush=True)
with (test_dir / "build.log").open("w") as log:
    subprocess.run(
        ["go", "build", "-o", str(test_dir) + "/", "./cmd/mekugi", "./cmd/shell"],
        cwd=repository, stdout=log, stderr=subprocess.STDOUT, check=True,
    )
launcher = test_dir / "launch"
launcher.write_text(
    '#!/bin/sh\nset -eu\n'
    'build_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)\n'
    'PATH="$build_dir:$PATH"\nexport PATH\n'
    'exec "$build_dir/mekugi" codex "$@"\n'
)
launcher.chmod(0o700)
common = [
    str(launcher), "--sandbox", sandbox, "--ask-for-approval", "never",
    "--model", "gpt-5.6-sol", "-c", 'model_reasoning_effort="medium"',
    "exec", "--skip-git-repo-check",
]

def run(stage, arguments):
    with (test_dir / f"{stage}.jsonl").open("w") as out, \
         (test_dir / f"{stage}.stderr").open("w") as err:
        result = subprocess.run(
            ["timeout", "--signal=INT", "--kill-after=15s", "180s",
             *common, *arguments],
            cwd=test_dir, stdin=subprocess.DEVNULL, stdout=out, stderr=err,
        )
    if result.returncode:
        raise RuntimeError(f"{stage} exited {result.returncode}; inspect its artifacts")
    events = []
    for line in (test_dir / f"{stage}.jsonl").read_text().splitlines():
        try:
            events.append(json.loads(line))
        except ValueError:
            pass  # Non-JSON output is retained, never interpreted as success.
    return events

def completed_items(events):
    return [e["item"] for e in events if e.get("type") == "item.completed"]

def require_child_model(events):
    starts = [
        i.get("text", "") for i in completed_items(events)
        if i.get("type") == "agent_message"
        and i.get("text", "").startswith("[`/root/plaintext_probe`] Started.")
    ]
    assert any(
        "Model: `gpt-5.6-sol`" in text and "Reasoning effort: `medium`" in text
        for text in starts
    ), "Child did not report Sol medium"

def require_child_result(events, marker):
    prefix = "[`/root` <- `/root/plaintext_probe`] Reply received:\n"
    matches = [
        i.get("text", "") for i in completed_items(events)
        if i.get("type") == "agent_message" and i.get("text", "").startswith(prefix)
    ]
    for text in matches:
        if "Journal result `/root/plaintext_probe`" not in text or "Journal saved:" in text:
            continue
        for item in text.split("\n- ")[1:]:
            _, separator, body = item.partition("\n\n")
            if not separator:
                continue
            question, separator, answer = textwrap.dedent(body).partition("**Answer:**")
            if separator and "**Question:**" in question \
                    and f"text exactly {marker}" in question and answer.strip() == marker:
                return
    raise AssertionError(f"Missing native child result/answer association for {marker}")

shell = run("shell", ["--json",
    'Only test script execution. Do not read or edit files or spawn agents. '
    'Call functions.shell with the Bash script printf "CONTEXT_SHELL_OK\\n". '
    'Report the observed result.'])
assert any(
    i.get("type") == "command_execution" and i.get("exit_code") == 0
    and i.get("aggregated_output", "").strip() == "CONTEXT_SHELL_OK"
    for i in completed_items(shell)
), "No successful shell output; inspect shell artifacts"

initial = run("assignment", ["--json",
    'Only test native assignment association. Do not read or edit files or run commands. '
    'Use mekugi_collaboration.spawn_agent with task_name plaintext_probe, fork_turns none, '
    'model gpt-5.6-sol, reasoning_effort medium, and no fixed-model agent role. '
    'Assign: "Do not inspect files, run commands, or spawn agents. Finish directly through '
    'functions.journal with one add mutation, answer true, text exactly CONTEXT_ASSIGNMENT_OK. '
    'If association rejects, return the exact error without answer true." '
    'Wait for native completion. Do not look up the child journal to get its result. '
    'Report what was actually received.'])
require_child_model(initial)
require_child_result(initial, "CONTEXT_ASSIGNMENT_OK")
thread = next(e["thread_id"] for e in initial if e.get("type") == "thread.started")

# The first launcher has exited. A new router resumes the exact session, in the same workspace.
resumed = run("resume", ["resume", thread, "--json",
    'Only test the existing child after router restart. Do not inspect files, run commands, '
    'or spawn a replacement. Use mekugi_collaboration.followup_task for /root/plaintext_probe. '
    'Assign: "Do not inspect files, run commands, or spawn agents. Finish directly through '
    'functions.journal with one add mutation, answer true, text exactly CONTEXT_FOLLOWUP_OK. '
    'If association rejects, return the exact error without answer true." '
    'Observe native completion without a journal lookup. If the child cannot resume, report '
    'that and do not replace it. Report the observed result.'])
require_child_result(resumed, "CONTEXT_FOLLOWUP_OK")
print(f"PASS: shell, plaintext assignment, and resumed follow-up. Launcher: {launcher}")
