#!/usr/bin/env python3
"""Check, build, test and package the pinned native runtime without installing it."""

import argparse
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile


ROOT = Path(__file__).resolve().parent.parent
CODEX = ROOT / "runtime/codex"
RUST = CODEX / "codex-rs"


def run(args, env):
    args = list(args)
    env = dict(env)
    separator = args.index("--") if "--" in args else len(args)
    options = []
    # sccache hashes CARGO_* variables, even when their values merely restate
    # Cargo configuration. Pass these overrides as options instead.
    target_dir = env.pop("CARGO_TARGET_DIR", None)
    if target_dir is not None and not any(
        arg == "--target-dir" or arg.startswith("--target-dir=")
        for arg in args[:separator]
    ):
        options.extend(["--target-dir", target_dir])
    incremental = env.get("CARGO_INCREMENTAL")
    if incremental in {"0", "1"}:
        env.pop("CARGO_INCREMENTAL")
        # This environment override wins over config, including caller --config.
        options.extend(["--config", f"build.incremental={'true' if incremental == '1' else 'false'}"])
    args[separator:separator] = options
    subprocess.run(args, cwd=RUST, env=env, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("check", "build", "test", "package"))
    parser.add_argument("--profile", help="default: dev-small for check/test, release without LTO for build, release for package")
    parser.add_argument("--output", type=Path, default=ROOT / "bin/native")
    parser.add_argument("--archive", type=Path, default=ROOT / "bin/mekugi-native.tar.gz")
    parser.add_argument("--test-filter", help="run matching app-server integration tests against the staged runtime")
    argv = sys.argv[1:]
    separator = argv.index("--") if "--" in argv else len(argv)
    cargo_args = argv[separator + 1:]
    args = parser.parse_args(argv[:separator])
    if cargo_args and args.action not in {"check", "test"}:
        parser.error("Cargo selectors after -- are supported only by check and test")
    if args.test_filter is not None and (
        args.action != "test" or cargo_args or not args.test_filter or args.test_filter.startswith("-")
    ):
        parser.error("--test-filter requires a test name or substring for test without Cargo selectors")
    profile = args.profile or ("dev-small" if args.action in {"check", "test"} else "release")
    if not (RUST / "Cargo.lock").is_file():
        parser.error("initialize the pinned submodule: git submodule update --init runtime/codex")

    os.environ["CODEX_REPO_ROOT"] = str(CODEX)
    sys.path.insert(0, str(CODEX / "scripts"))
    from codex_package.targets import TARGET_SPECS
    from codex_package.v8 import resolve_codex_v8_cargo_env

    version = subprocess.check_output(["rustc", "-vV"], cwd=RUST, text=True)
    target = next(line.removeprefix("host: ") for line in version.splitlines() if line.startswith("host: "))
    if target not in {"x86_64-unknown-linux-gnu", "aarch64-unknown-linux-gnu", "aarch64-apple-darwin"}:
        parser.error(f"unsupported native package host: {target}")
    spec = TARGET_SPECS[target]
    env = dict(os.environ)
    metadata = json.loads(subprocess.check_output(
        ["cargo", "metadata", "--locked", "--no-deps", "--format-version", "1"],
        cwd=RUST, env=env, text=True,
    ))
    target_dir = Path(metadata["target_directory"])
    # Cargo's test loader can find a host-local shared OpenSSL that an extracted
    # package cannot. Link its static archives into both runtime executables.
    env["OPENSSL_STATIC"] = "1"
    # Package Cargo outputs directly; never strip bwrap after an integrity hash
    # may have been compiled into the server.
    env.setdefault("CARGO_PROFILE_RELEASE_STRIP", "symbols")
    if args.action == "build" and not args.profile:
        env.setdefault("CARGO_PROFILE_RELEASE_LTO", "off")
    env.update(resolve_codex_v8_cargo_env(spec, cache_root=target_dir / "v8-artifacts"))
    if args.action in {"check", "test"}:
        env.pop("BASH_ENV", None)
        env.pop("MEKUGI_EXEC_TRACK", None)
        # Match the fork's test runner: large async test futures need this stack.
        env.setdefault("RUST_MIN_STACK", "8388608")
    subprocess_test_package = "codex-app-server-subprocess-tests"
    if args.action == "check":
        run(["cargo", "check", "--locked", "--profile", profile,
             *(cargo_args or ["-p", "codex-app-server", "-p", subprocess_test_package, "--tests"])], env)
        return
    if args.action == "test" and cargo_args:
        # Focused library checks do not need the app-server or packaged helpers.
        run(["cargo", "test", "--locked", "--profile", profile, *cargo_args], env)
        return
    binaries = [("codex-app-server", "mekugi-app-server"), ("codex-code-mode-host", "codex-code-mode-host")]
    if spec.is_linux:
        binaries.append(("codex-bwrap", "bwrap"))
    selection = ["--locked", "--profile", profile]
    for package, _ in binaries:
        selection.extend(["-p", package])
    suite_targets = []
    if args.action == "test":
        selection.extend(["-p", subprocess_test_package])
        suite_targets.extend(["--test", "subprocess"])
        if args.test_filter is not None:
            suite_targets.extend(["--test", "all"])
    cargo = ["cargo", "build", *selection]
    for _, binary in binaries:
        cargo.extend(["--bin", binary])
    if args.action == "test":
        # The subprocess suite starts a separate WebSocket exec-server fixture.
        # It belongs to codex-app-server but is not part of the shipped runtime.
        cargo.extend(["--bin", "exec-server"])
    # Resolve the selected test dependencies alongside the staged runtime.
    cargo.extend(suite_targets)
    run(cargo, env)

    profile_dir = "debug" if profile == "dev" else profile
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    for _, binary in binaries:
        shutil.copy2(target_dir / profile_dir / binary, output / binary)
    shutil.copy2(RUST / "models-manager/models.json", output / "mekugi-models.json")
    if args.action == "test":
        filters = ["--", args.test_filter] if args.test_filter is not None else []
        if args.test_filter is None:
            run(["cargo", "test", *selection, "--bin", "mekugi-app-server"], env)
        # Give the suites the staged sibling layout without changing compiler inputs.
        suite_env = env | {
            f"CARGO_BIN_EXE_{name}": str(output / name)
            for name in ("mekugi-app-server", "codex-code-mode-host")
        }
        suite_env["CARGO_BIN_EXE_exec-server"] = str(
            target_dir / profile_dir / "exec-server"
        )
        run(["cargo", "test", *selection, *suite_targets, *filters], suite_env)
        return
    if args.action == "build":
        print(output)
        return

    archive = args.archive.resolve()
    archive.parent.mkdir(parents=True, exist_ok=True)
    revision = subprocess.check_output(
        ["git", "describe", "--always", "--dirty", "--abbrev=40"], cwd=CODEX
    ).strip() + b"\n"
    with tarfile.open(archive, "w:gz") as bundle:
        for _, binary in binaries:
            bundle.add(output / binary, arcname=binary)
        bundle.add(output / "mekugi-models.json", arcname="mekugi-models.json")
        for name in ("LICENSE", "NOTICE"):
            bundle.add(CODEX / name, arcname=name)
        if spec.is_linux:
            bundle.add(RUST / "vendor/bubblewrap/COPYING", arcname="licenses/bubblewrap.txt")
        bundle.add(RUST / "app-server-protocol/schema", arcname="protocol")
        source = tarfile.TarInfo("SOURCE_REVISION")
        source.size = len(revision)
        source.mode = 0o644
        bundle.addfile(source, io.BytesIO(revision))
    print(archive)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
