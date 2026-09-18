#!/usr/bin/env bash
set -euo pipefail

benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
checker="$benchmark_root/check_commentary_coverage.py"
manifest="$benchmark_root/tasks/commentary-coverage/task.json"
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT

for mode in mekugi-diagnostic ctp-only mentor-handoff; do
	python3 "$checker" validate "$manifest" "$mode"
done
if python3 "$checker" validate "$manifest" paired >/dev/null 2>&1; then
	printf 'commentary coverage accepted an unsupported mode\n' >&2
	exit 1
fi

cat >"$fixture/operations.jsonl" <<'JSONL'
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0,"command":"hpatch 'new go.mod'"}}
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0,"command":"journal add coverage:bash --report-now"}}
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":1,"command":"hpatch 'stale target'"}}
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0,"command":"hpatch --recover amber 'maple target'"}}
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0,"command":"journal add coverage:recovered --report-now"}}
{"type":"item.completed","item":{"type":"agent_message","text":"coverage:bash\n\ncoverage:recovered"}}
{"type":"item.completed","item":{"type":"agent_message","text":"coverage:reported"}}
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0,"command":"journal add coverage:posix --report-now"}}
{"type":"item.completed","item":{"type":"agent_message","text":"coverage:posix\n\ncoverage:code-mode"}}
{"type":"item.completed","item":{"type":"agent_message","text":"Running the requested operation."}}
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0,"command":"printf coverage:exec-complete"}}
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0,"command":"publish coverage%3Acode-mode"}}
{"type":"item.completed","item":{"type":"agent_message","text":"Tokens for this session\n\n| Category | Tokens | API USD |\n| --- | ---: | ---: |\n| Input | 120 | — |\n| Cached input | 80 | n/a |\n| Uncached input | 40 | n/a |\n| Output | 30 | n/a |\n| Reasoning | 20 | — |\n| Total | — | n/a |\n"}}
JSONL

python3 "$checker" check "$manifest" mekugi-diagnostic mekugi \
	"$fixture/operations.jsonl" >"$fixture/result.json"
jq -e '.passed == true and .profiles == ["operations", "reporting", "terminal"]' \
	"$fixture/result.json" >/dev/null

grep -v 'coverage:reported' "$fixture/operations.jsonl" >"$fixture/ctp.jsonl"
for arm in native ctp; do
	python3 "$checker" check "$manifest" ctp-only "$arm" \
		"$fixture/ctp.jsonl" >"$fixture/result.json"
	jq -e '.passed == true and .profiles == ["operations", "terminal"]' \
		"$fixture/result.json" >/dev/null
done

if python3 "$checker" check "$manifest" mekugi-diagnostic mekugi \
	"$fixture/ctp.jsonl" >"$fixture/missing.json"; then
	printf 'commentary coverage accepted a missing report_issue message\n' >&2
	exit 1
fi
jq -e '.passed == false and (.missing | index("message:regex:coverage:reported") != null)' \
	"$fixture/missing.json" >/dev/null

# Markers alone must not pass when shell recovery is missing or failed.
for recovery_status in missing failed; do
	jq -c --arg state "$recovery_status" '
		if (.item.command // "" | contains("hpatch --recover ")) then
			if $state == "missing" then empty else .item.exit_code = 1 end
		else . end
	' "$fixture/ctp.jsonl" >"$fixture/recovery-$recovery_status.jsonl"
	if python3 "$checker" check "$manifest" ctp-only ctp \
		"$fixture/recovery-$recovery_status.jsonl" >"$fixture/missing-recovery.json"; then
		printf 'commentary coverage accepted %s shell recovery\n' "$recovery_status" >&2
		exit 1
	fi
	jq -e '.passed == false and (.missing | index("command:contains:hpatch --recover ") != null)' \
		"$fixture/missing-recovery.json" >/dev/null
done

# Successful commands alone do not establish journal delivery.
jq -c 'select(.item.type != "agent_message" or (.item.text | contains("coverage:recovered") | not))' \
	"$fixture/ctp.jsonl" >"$fixture/missing-journal.jsonl"
if python3 "$checker" check "$manifest" ctp-only ctp \
	"$fixture/missing-journal.jsonl" >"$fixture/missing-journal.json"; then
	printf 'commentary coverage accepted missing recovery journal delivery\n' >&2
	exit 1
fi
jq -e '.passed == false and (.missing | index("message:regex:coverage:recovered") != null)' \
	"$fixture/missing-journal.json" >/dev/null

grep -v 'coverage%3Acode-mode' "$fixture/ctp.jsonl" >"$fixture/failed-command.jsonl"
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","status":"failed","exit_code":1,"command":"publish coverage%3Acode-mode"}}' >>"$fixture/failed-command.jsonl"
if python3 "$checker" check "$manifest" ctp-only ctp \
	"$fixture/failed-command.jsonl" >"$fixture/missing-command.json"; then
	printf 'commentary coverage accepted a failed runtime publication command\n' >&2
	exit 1
fi
jq -e '.passed == false and (.missing | index("command:regex:coverage(?:%3A|:)code-mode") != null)' \
	"$fixture/missing-command.json" >/dev/null

cat >"$fixture/collaboration.jsonl" <<'JSONL'
{"type":"item.completed","item":{"type":"collab_tool_call"}}
{"type":"item.completed","item":{"type":"agent_message","text":"Tokens for this session\n\n| Category | Tokens | API USD |\n| --- | ---: | ---: |\n| Input | 120 | — |\n| Cached input | 80 | n/a |\n| Uncached input | 40 | n/a |\n| Output | 30 | n/a |\n| Reasoning | 20 | — |\n| Total | — | n/a |\n"}}
JSONL
for arm in mekugi mekugi-mentor; do
	python3 "$checker" check "$manifest" mentor-handoff "$arm" \
		"$fixture/collaboration.jsonl" >"$fixture/result.json"
	jq -e '.passed == true and .profiles == ["collaboration", "terminal"]' \
		"$fixture/result.json" >/dev/null
done

printf '%s\n' '{invalid' >"$fixture/malformed.jsonl"
if python3 "$checker" check "$manifest" ctp-only ctp \
	"$fixture/malformed.jsonl" >/dev/null 2>&1; then
	printf 'commentary coverage accepted malformed Codex JSONL\n' >&2
	exit 1
fi

jq '.commentary_coverage.version = 2' "$manifest" >"$fixture/invalid-manifest.json"
if python3 "$checker" validate "$fixture/invalid-manifest.json" ctp-only >/dev/null 2>&1; then
	printf 'commentary coverage accepted an unsupported manifest version\n' >&2
	exit 1
fi

printf '%s\n' '{"version":1}' >"$fixture/ordinary-task.json"
python3 "$checker" validate "$fixture/ordinary-task.json" paired
