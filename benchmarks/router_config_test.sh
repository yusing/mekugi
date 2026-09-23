#!/usr/bin/env bash
set -euo pipefail
benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
cat >"$fixture/mekugi" <<'SH'
#!/bin/sh
printf '%s\n' "$@" >"$CAPTURE"
SH
chmod +x "$fixture/mekugi"
for mode in passthrough mekugi; do
  for main_mentor in false true; do
  for mentor in false true; do
   CAPTURE="$fixture/args" PATH="$fixture:$PATH" BENCH_ARTIFACT_DIR=/benchmark-artifacts/session \
    MEKUGI_RUNTIME_DIR="$fixture/runtime" MEKUGI_BENCH_MODE="$mode" MEKUGI_BENCH_MENTOR="$mentor" MEKUGI_BENCH_MAIN_MENTOR="$main_mentor" \
    bash "$benchmark_root/session-entry.sh" exec 'prompt with spaces'
   python3 - "$fixture/args" "$mode" "$mentor" "$main_mentor" <<'PY'
import pathlib,sys
path,mode,mentor,main_mentor=sys.argv[1:]
args=pathlib.Path(path).read_text().splitlines()
assert args == ['--mode',mode,f'--main-mentor-handoff={main_mentor}',f'--mentor-handoff={mentor}',
 '--capture-output','/benchmark-artifacts/session/capture.jsonl',
 '--metrics-output','/benchmark-artifacts/session/metrics.json','codex','--disable','apps','exec','prompt with spaces']
PY
  done
  done
done
BENCH_RUN_DIR="$fixture" BENCH_DEPENDENCY_CACHE="$fixture/cache" CODEX_AUTH_PATH="$fixture/auth.json" \
 docker compose --profile '*' -f "$benchmark_root/compose.yaml" config --format json >"$fixture/config.json"
python3 - "$fixture/config.json" <<'PY'
import json,sys
services=json.load(open(sys.argv[1]))['services']
assert set(services)=={'control-agent','mekugi-agent','dependency-loader','grader'}
for name in ('control-agent','mekugi-agent'):
 s=services[name]
 assert s['read_only'] and 'NET_ADMIN' in s['cap_add'] and 'SYS_ADMIN' in s['cap_add']
 assert 'apparmor:unconfined' in s['security_opt']
 assert not s.get('ports') and not s.get('privileged')
assert set(services['control-agent']['networks']).isdisjoint(services['mekugi-agent']['networks'])
PY
printf '%s\n' 'Session mode and mentor arguments plus container isolation configuration passed'
