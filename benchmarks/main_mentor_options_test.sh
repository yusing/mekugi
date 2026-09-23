#!/usr/bin/env bash
set -euo pipefail
benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
fixture=$(mktemp -d)
trap 'rm -r -- "$fixture"' EXIT
# Exercise retained main-Mentor option validation without containers/auth.
cat >"$fixture/main-mentor.sh" <<'SH'
source "$1/bench.sh"
configure_benchmark
printf '%s %s\n' "$main_mentor" "${arm_mentor[mekugi]}"
SH
[[ $(BENCHMARK_MODE=mekugi-diagnostic MODEL=gpt-5.6-sol BENCHMARK_MAIN_MENTOR=true bash "$fixture/main-mentor.sh" "$benchmark_root") == 'true false' ]]
[[ $(BENCHMARK_MODE=mekugi-diagnostic MODEL=gpt-5.6-sol bash "$fixture/main-mentor.sh" "$benchmark_root") == 'false false' ]]
for invalid in 'BENCHMARK_MODE=paired' 'MODEL=gpt-6-astra' 'BENCHMARK_MAIN_MENTOR=invalid'; do
 if env BENCHMARK_MODE=mekugi-diagnostic MODEL=gpt-5.6-sol BENCHMARK_MAIN_MENTOR=true "$invalid" bash "$fixture/main-mentor.sh" "$benchmark_root" >/dev/null 2>&1; then
  printf 'invalid main mentor option accepted: %s\n' "$invalid" >&2; exit 1
 fi
done
printf 'Main Mentor option selection and unchanged paired defaults passed\n'
