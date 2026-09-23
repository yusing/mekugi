#!/usr/bin/env bash
set -euo pipefail

benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
checker="$benchmark_root/check_commentary_coverage.py"
manifest="$benchmark_root/tasks/commentary-coverage/task.json"
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT

for mode in mekugi-diagnostic mentor-handoff; do
	python3 "$checker" validate "$manifest" "$mode"
done
if python3 "$checker" validate "$manifest" paired >/dev/null 2>&1; then
	printf 'commentary coverage accepted an unsupported mode\n' >&2
	exit 1
fi

cat >"$fixture/operations.jsonl" <<'JSONL'
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0,"command":"sed -i 's/draft/current/' coverage.go"}}
{"type":"item.completed","item":{"type":"agent_message","text":"coverage:command\n\ncoverage:edited"}}
{"type":"item.completed","item":{"type":"agent_message","text":"coverage:reported"}}
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0,"command":"gofmt -w coverage.go && go test ./..."}}
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0,"command":"printf '%s\\n' coverage:exec-complete"}}
{"type":"item.completed","item":{"type":"agent_message","text":"coverage:validated\n\ncoverage:code-mode"}}
{"type":"item.completed","item":{"type":"agent_message","text":"Tokens for this session\n\n| Category | Tokens | API USD |\n| --- | ---: | ---: |\n| Input | 120 | — |\n| Cached input | 80 | n/a |\n| Uncached input | 40 | n/a |\n| Output | 30 | n/a |\n| Reasoning | 20 | — |\n| Total | — | n/a |\n"}}
JSONL

python3 "$checker" check "$manifest" mekugi-diagnostic mekugi \
	"$fixture/operations.jsonl" >"$fixture/result.json"
jq -e '.passed == true and .profiles == ["operations", "reporting", "terminal"]' \
	"$fixture/result.json" >/dev/null

grep -v 'coverage:reported' "$fixture/operations.jsonl" >"$fixture/missing-report.jsonl"
if python3 "$checker" check "$manifest" mekugi-diagnostic mekugi \
	"$fixture/missing-report.jsonl" >"$fixture/missing.json"; then
	printf 'commentary coverage accepted a missing issue-report marker\n' >&2
	exit 1
fi
jq -e '.passed == false and (.missing | index("message:regex:coverage:reported") != null)' \
	"$fixture/missing.json" >/dev/null

jq -c 'select(.item.type != "agent_message" or (.item.text | contains("coverage:edited") | not))' \
	"$fixture/operations.jsonl" >"$fixture/missing-journal.jsonl"
if python3 "$checker" check "$manifest" mekugi-diagnostic mekugi \
	"$fixture/missing-journal.jsonl" >"$fixture/missing-journal.json"; then
	printf 'commentary coverage accepted missing edit journal delivery\n' >&2
	exit 1
fi
jq -e '.passed == false and (.missing | index("message:regex:coverage:edited") != null)' \
	"$fixture/missing-journal.json" >/dev/null

jq -c 'if (.item.command // "" | contains("go test ./...")) then .item.exit_code = 1 else . end' \
	"$fixture/operations.jsonl" >"$fixture/failed-command.jsonl"
if python3 "$checker" check "$manifest" mekugi-diagnostic mekugi \
	"$fixture/failed-command.jsonl" >"$fixture/missing-command.json"; then
	printf 'commentary coverage accepted a failed validation command\n' >&2
	exit 1
fi
jq -e '.passed == false and (.missing | index("command:contains:go test ./...") != null)' \
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
if python3 "$checker" check "$manifest" mekugi-diagnostic mekugi \
	"$fixture/malformed.jsonl" >/dev/null 2>&1; then
	printf 'commentary coverage accepted malformed Codex JSONL\n' >&2
	exit 1
fi

jq '.commentary_coverage.version = 2' "$manifest" >"$fixture/invalid-manifest.json"
if python3 "$checker" validate "$fixture/invalid-manifest.json" mekugi-diagnostic >/dev/null 2>&1; then
	printf 'commentary coverage accepted an unsupported manifest version\n' >&2
	exit 1
fi

printf '%s\n' '{"version":1}' >"$fixture/ordinary-task.json"
python3 "$checker" validate "$fixture/ordinary-task.json" paired
