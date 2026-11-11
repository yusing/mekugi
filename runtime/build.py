#!/usr/bin/env python3
"""Build, test and package the pinned local native runtime without installing it."""

import argparse
import io
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
    subprocess.run(args, cwd=RUST, env=env, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("build", "test", "package"))
    parser.add_argument("--profile", default="release")
    parser.add_argument("--output", type=Path, default=ROOT / "bin/native")
    parser.add_argument("--archive", type=Path, default=ROOT / "bin/mekugi-native.tar.gz")
    args = parser.parse_args()
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
    target_dir = Path(os.environ.get("CARGO_TARGET_DIR", str(RUST / "target")))
    if not target_dir.is_absolute():
        target_dir = RUST / target_dir
    env = dict(os.environ)
    env["CARGO_TARGET_DIR"] = str(target_dir)
    # Cargo's test loader can find a host-local shared OpenSSL that an extracted
    # package cannot. Link its static archives into both runtime executables.
    env["OPENSSL_STATIC"] = "1"
    # Package Cargo outputs directly; never strip bwrap after an integrity hash
    # may have been compiled into the server.
    env.setdefault("CARGO_PROFILE_RELEASE_STRIP", "symbols")
    env.update(resolve_codex_v8_cargo_env(spec, cache_root=target_dir / "v8-artifacts"))
    cargo = ["cargo", "build", "--locked", "--profile", args.profile]
    binaries = [("codex-app-server", "mekugi-app-server"), ("codex-code-mode-host", "codex-code-mode-host")]
    if spec.is_linux:
        binaries.append(("codex-bwrap", "bwrap"))
    for package, binary in binaries:
        cargo.extend(["-p", package, "--bin", binary])
    run(cargo, env)

    profile_dir = "debug" if args.profile == "dev" else args.profile
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    for _, binary in binaries:
        shutil.copy2(target_dir / profile_dir / binary, output / binary)
    shutil.copy2(RUST / "models-manager/models.json", output / "mekugi-models.json")
    if args.action == "test":
        env.pop("BASH_ENV", None)
        env.pop("MEKUGI_EXEC_TRACK", None)
        run(["cargo", "test", "--locked", "--profile", args.profile,
             "-p", "codex-app-server", "--bin", "mekugi-app-server"], env)
        run(["cargo", "test", "--locked", "--profile", args.profile,
             "-p", "codex-app-server", "--test", "all", "--",
             "suite::v2::command_exec", "suite::v2::connection_handling",
             "suite::v2::code_mode_host", "suite::strict_config"], env)
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
