#!/usr/bin/env bash
# Opt-in, real Docker checks. No model request or credential is needed.
# shellcheck source-path=SCRIPTDIR
set -euo pipefail
: "${BENCH_TEST_IMAGE:?Set BENCH_TEST_IMAGE to an existing benchmark image}"
benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=bench.sh
source "$benchmark_root/bench.sh"
fixture=$(mktemp -d /tmp/mekugi-isolation-test-XXXXXX)
export COMPOSE_PROJECT_NAME="mekugi_isolation_${fixture##*-}"
export COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME,,}
export BENCH_IMAGE=$BENCH_TEST_IMAGE BENCH_RUN_DIR=$fixture
export BENCH_DEPENDENCY_CACHE="$fixture/cache" CODEX_AUTH_PATH="$fixture/auth.json"
compose=(docker compose -f "$benchmark_root/compose.yaml")
cleanup_probe() {
    "${compose[@]}" --profile '*' down --remove-orphans >/dev/null 2>&1 || true
    # Containers can create root-owned replay state and private namespaces' files.
    docker run --rm --network none --volume "$fixture:/probe" "$BENCH_TEST_IMAGE" \
        chown -R "$(id -u):$(id -g)" /probe >/dev/null
    rm -rf -- "$fixture"
}
trap cleanup_probe EXIT
mkdir -p "$fixture"/{instructions,mekugi-config,agent-issue-reports,cache,artifacts/probe,runtime,candidate}
chmod 0770 "$fixture/candidate"
install -d -m 0770 "$fixture/codex-home"
install -d -m 0770 "$fixture/candidate/.git"
install -m 0660 /dev/null "$fixture/candidate/.git/config"
install -m 0755 "$benchmark_root/isolated-codex.sh" "$fixture/isolated-codex.sh"
install -m 0755 "$benchmark_root/agent-mounts.sh" "$fixture/agent-mounts.sh"
: >"$fixture/auth.json"
chmod 0600 "$fixture/auth.json"
printf 'host-only\n' >"$fixture/host-marker"
# Current launch scripts plus the real router and real Codex --version exercise
# read-only root startup, replay creation, listener health and executor restrictions.
"${compose[@]}" run --interactive=false --no-tty --rm --no-deps \
    --env MEKUGI_RUNTIME_DIR=/runtime --volume "$fixture/runtime:/runtime" \
    --env BENCH_ARTIFACT_DIR=/artifacts --volume "$fixture/artifacts/probe:/artifacts" \
    --env MEKUGI_BENCH_MODE=mekugi \
    --env CODEX_HOME=/benchmark-codex-home \
    --volume "$fixture/codex-home:/benchmark-codex-home" \
    --volume "$fixture/auth.json:/benchmark-codex-home/auth.json:ro" \
    --volume "$benchmark_root/session-entry.sh:/usr/local/bin/mekugi-benchmark-session:ro" \
    --volume "$benchmark_root/agent-check.py:/usr/local/libexec/mekugi-agent-check.py:ro" \
    --volume "$fixture/isolated-codex.sh:/usr/local/bin/codex:ro" \
    --volume "$fixture/candidate:$fixture/candidate" --workdir "$fixture/candidate" \
    --volume "$fixture/agent-mounts.sh:/usr/local/libexec/mekugi-agent-mounts:ro" \
    mekugi-agent bash /usr/local/bin/mekugi-benchmark-session --version >"$fixture/startup.stdout" 2>"$fixture/startup.stderr" || {
        cat "$fixture/startup.stderr" >&2; exit 1;
    }
[[ -d $fixture/runtime/state/mekugi ]]
grep -Fq 'codex-cli' "$fixture/startup.stdout"
# The real grading wrapper loads candidate shell code, but only inside its container.
dependency_cache=$BENCH_DEPENDENCY_CACHE dependency_kind=none grader_timeout=20
verify_task_contract() { :; }
cat >"$fixture/candidate/candidate.sh" <<'CANDIDATE'
[[ ! -e ../host-marker && ! -e /root/.codex/auth.json ]]
[[ ! -e /var/run/docker.sock ]]
! printf tampered >../host-marker 2>/dev/null
! printf leaked >/go/pkg/mod/oracle-artifact 2>/dev/null
[[ $(awk '/CapEff:/ {print $2}' /proc/self/status) == 0000000000000000 ]]
[[ -z $(ls /sys/class/net | sed '/^lo$/d') ]]
printf 'private build cache\n' >"$GOCACHE-marker"
exit 7
CANDIDATE
grader_command=(bash -eu -c 'source candidate.sh')
status=0
grade "$fixture/candidate" "$fixture/grader.stdout" "$fixture/grader.stderr" || status=$?
[[ $status == 7 ]] || { cat "$fixture/grader.stdout" "$fixture/grader.stderr" >&2; exit 1; }
[[ $(cat "$fixture/host-marker") == host-only ]]
[[ ! -e $fixture/cache/oracle-artifact && ! -e $fixture/cache/go-build ]]
# A new container must not see the previous grader's private compilation output.
# The expansion belongs to the grader container, not this host shell.
# shellcheck disable=SC2016
grader_command=(bash -eu -c 'test ! -e "$GOCACHE-marker"')
grade "$fixture/candidate" "$fixture/second.stdout" "$fixture/second.stderr"
# Exercise a real cold Go compilation as well as filesystem probes.
printf 'package main\nfunc main() {}\n' >"$fixture/candidate/main.go"
grader_timeout=90
grader_command=(go build -o /tmp/qualification-binary main.go)
grade "$fixture/candidate" "$fixture/go.stdout" "$fixture/go.stderr" || {
    cat "$fixture/go.stdout" "$fixture/go.stderr" >&2; exit 1;
}
[[ ! -e $fixture/cache/go-build ]]
printf 'Real router startup, executor state isolation, grader isolation and private caches passed\n'
