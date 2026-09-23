#!/usr/bin/env bash
# shellcheck source-path=SCRIPTDIR
set -euo pipefail
benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
# Importing definitions must not create workspaces, install traps, or run tools.
before_traps=$(trap -p)
# shellcheck source=bench.sh
source "$benchmark_root/bench.sh"
[[ $(trap -p) == "$before_traps" ]]
[[ ! -v run_dir ]]

for mode in paired control-only mekugi-only mekugi-diagnostic mentor-handoff; do
 (
  export BENCHMARK_MODE=$mode MODEL=gpt-5.6-luna REPETITIONS=1
  export BENCHMARK_REPORT_ISSUES=false
  configure_benchmark
  case $mode in
   paired) expected=(control mekugi); retained=(control mekugi) ;;
   control-only) expected=(control); retained=(control) ;;
   mekugi-only) expected=(mekugi); retained=(control mekugi) ;;
   mekugi-diagnostic) expected=(mekugi); retained=(mekugi) ;;
   mentor-handoff) expected=(mekugi mekugi-mentor); retained=(mekugi mekugi-mentor) ;;
  esac
  [[ ${run_arms[*]} == "${expected[*]}" && ${retained_arms[*]} == "${retained[*]}" ]]
  calls="$fixture/$mode"
  run_attempt() {
   local arm=$1
   printf '%s %s %s %s %s %s %s\n' "$arm" "$2" "$3" "${arm_services[$arm]}" \
    "${arm_modes[$arm]}" "${arm_instructions[$arm]}" "${arm_mentor[$arm]}" >>"$calls"
  }
  (run_block 1)
  (run_block 2)
  case $mode in
   paired) cat >"$fixture/want" <<'EOF'
mekugi 1 1 mekugi-agent mekugi mekugi.md false
control 1 2 control-agent passthrough control.md false
control 2 1 control-agent passthrough control.md false
mekugi 2 2 mekugi-agent mekugi mekugi.md false
EOF
    ;;
   control-only) cat >"$fixture/want" <<'EOF'
control 1 1 control-agent passthrough control.md false
control 2 1 control-agent passthrough control.md false
EOF
    ;;
   mekugi-only) cat >"$fixture/want" <<'EOF'
mekugi 1 2 mekugi-agent mekugi mekugi.md false
mekugi 2 2 mekugi-agent mekugi mekugi.md false
EOF
    ;;
   mekugi-diagnostic) cat >"$fixture/want" <<'EOF'
mekugi 1 1 mekugi-agent mekugi mekugi.md false
mekugi 2 1 mekugi-agent mekugi mekugi.md false
EOF
    ;;
   mentor-handoff) cat >"$fixture/want" <<'EOF'
mekugi-mentor 1 1 mekugi-agent mekugi mekugi.md true
mekugi 1 2 control-agent mekugi mekugi.md false
mekugi 2 1 control-agent mekugi mekugi.md false
mekugi-mentor 2 2 mekugi-agent mekugi mekugi.md true
EOF
    ;;
  esac
  diff -u "$fixture/want" "$calls"
  : >"$calls"
  started=true collected=false run_dir=/synthetic
  collect_router_metrics() { printf '%s %s %s\n' "$@" >>"$calls"; }
  collect_artifacts
  [[ $collected == true && $(wc -l <"$calls") -eq ${#run_arms[@]} ]]
  collect_artifacts
  [[ $(wc -l <"$calls") -eq ${#run_arms[@]} ]]
 )
done

# A failed arm must not suppress its paired sibling; cancellation must.
export BENCHMARK_MODE=paired MODEL=gpt-5.6-sol REPETITIONS=1 BENCHMARK_REPORT_ISSUES=false
configure_benchmark
calls="$fixture/status"
run_attempt() { printf '%s\n' "$1" >>"$calls"; return 1; }
status=0
(run_block 1) || status=$?
[[ $status == 1 && $(wc -l <"$calls") -eq 2 ]]
: >"$calls"
run_attempt() { printf '%s\n' "$1" >>"$calls"; cancel_pair 143; }
status=0
(run_block 1) || status=$?
[[ $status == 143 && $(wc -l <"$calls") -eq 1 ]]
printf '%s\n' 'Sourceable runner, arm plans, alternating order, failure and cancellation passed'
