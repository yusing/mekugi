"""Native runner routing checks, without a Rust build or dependency download."""

import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("native_build", Path(__file__).with_name("build.py"))
build = importlib.util.module_from_spec(spec)
spec.loader.exec_module(build)


class CargoEnvironmentTests(unittest.TestCase):
    def run_cargo(self, args, env):
        original = dict(env)
        with patch.object(build.subprocess, "run") as run:
            build.run(args, env)
        self.assertEqual(env, original)
        self.assertEqual(run.call_args.kwargs["cwd"], build.RUST)
        self.assertTrue(run.call_args.kwargs["check"])
        return run.call_args.args[0], run.call_args.kwargs["env"]

    def test_configured_defaults_do_not_add_overrides(self):
        args = ["cargo", "check", "--locked"]
        command, env = self.run_cargo(args, {"OPENSSL_STATIC": "1"})
        self.assertEqual(command, args)
        self.assertEqual(env, {"OPENSSL_STATIC": "1"})

    def test_environment_overrides_use_options_before_test_filter(self):
        args = ["cargo", "test", "--locked", "--", "--target-dir=filter"]
        command, env = self.run_cargo(args, {
            "CARGO_TARGET_DIR": "relative-target", "CARGO_INCREMENTAL": "0",
        })
        self.assertEqual(command, [
            "cargo", "test", "--locked", "--target-dir", "relative-target",
            "--config", "build.incremental=false", "--", "--target-dir=filter",
        ])
        self.assertEqual(env, {})

    def test_explicit_target_flag_wins_over_environment(self):
        for flag in (["--target-dir", "selected"], ["--target-dir=selected"]):
            with self.subTest(flag=flag):
                args = ["cargo", "check", *flag]
                command, env = self.run_cargo(args, {"CARGO_TARGET_DIR": "unused"})
                self.assertEqual(command, args)
                self.assertEqual(env, {})

    def test_incremental_environment_wins_over_caller_config(self):
        for value, setting in [("0", "false"), ("1", "true")]:
            with self.subTest(value=value):
                opposite = "true" if value == "0" else "false"
                args = ["cargo", "check", "--config", f"build.incremental={opposite}"]
                command, env = self.run_cargo(args, {"CARGO_INCREMENTAL": value})
                self.assertEqual(command, [*args, "--config", f"build.incremental={setting}"])
                self.assertEqual(env, {})

    def test_other_environment_values_remain_cargos_responsibility(self):
        args = ["cargo", "check"]
        command, env = self.run_cargo(args, {"CARGO_INCREMENTAL": "invalid"})
        self.assertEqual(command, args)
        self.assertEqual(env, {"CARGO_INCREMENTAL": "invalid"})


class NativeRunnerTests(unittest.TestCase):
    def run_action(self, *arguments, stack=None, target_config="shared-target", target_env=None, release_lto=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            rust = root / "codex-rs"
            rust.mkdir()
            (rust / "Cargo.toml").write_text('[package]\nname = "runner-test"\nversion = "0.1.0"\nedition = "2021"\n')
            (rust / "src").mkdir()
            (rust / "src/lib.rs").touch()
            (rust / "Cargo.lock").write_text('version = 4\n\n[[package]]\nname = "runner-test"\nversion = "0.1.0"\n')
            cargo_home = root / "cargo-home"
            cargo_home.mkdir()
            if target_config is not None:
                (cargo_home / "config.toml").write_text(f'[build]\ntarget-dir = {json.dumps(target_config)}\n')
            expected_target = Path(target_env) if target_env is not None else (
                Path(target_config) if target_config is not None else rust / "target"
            )
            if not expected_target.is_absolute():
                # Environment paths are cwd-relative; config paths are relative
                # to the directory containing CARGO_HOME.
                expected_target = (rust if target_env is not None else root) / expected_target
            targets = ModuleType("codex_package.targets")
            targets.TARGET_SPECS = {"x86_64-unknown-linux-gnu": SimpleNamespace(is_linux=True)}
            v8 = ModuleType("codex_package.v8")
            environment = {"PATH": os.environ["PATH"], "CARGO_HOME": str(cargo_home), "BASH_ENV": "hook", "MEKUGI_EXEC_TRACK": "1"}
            if target_env is not None:
                environment["CARGO_TARGET_DIR"] = target_env
            if stack is not None:
                environment["RUST_MIN_STACK"] = stack
            if release_lto is not None:
                environment["CARGO_PROFILE_RELEASE_LTO"] = release_lto
            output = root / "output"
            # Wrapper flags precede the separator; Cargo arguments remain untouched.
            argv = ["build.py", arguments[0], "--output", str(output), *arguments[1:]]
            if arguments[0] == "package":
                argv.extend(["--archive", str(root / "native.tar.gz")])
            check_output = build.subprocess.check_output

            def compiler_or_metadata(command, **kwargs):
                if command[0] == "rustc":
                    return "host: x86_64-unknown-linux-gnu\n"
                if command[0] == "git":
                    return b"test-revision"
                return check_output(command, **kwargs)

            with (
                patch.object(build, "RUST", rust),
                patch.object(build, "CODEX", root),
                patch.object(sys, "argv", argv),
                patch.object(sys, "path", sys.path.copy()),
                patch.dict(os.environ, environment, clear=True),
                patch.dict(sys.modules, {"codex_package.targets": targets, "codex_package.v8": v8}),
                patch.object(build.subprocess, "check_output", side_effect=compiler_or_metadata),
                patch.object(v8, "resolve_codex_v8_cargo_env", create=True, return_value={"RUSTY_V8_ARCHIVE": "pinned-v8"}) as resolve_v8,
                patch.object(build, "run") as run,
                patch.object(build.shutil, "copy2") as copy,
                patch.object(build.tarfile, "open"),
            ):
                build.main()
                self.assertEqual(resolve_v8.call_args.kwargs["cache_root"], expected_target / "v8-artifacts")
                for call in run.call_args_list:
                    self.assertEqual(call.args[1].get("CARGO_TARGET_DIR"), target_env)
                for call in copy.call_args_list:
                    if call.args[0].name != "models.json":
                        profile = run.call_args_list[0].args[0][4]
                        profile_dir = "debug" if profile == "dev" else profile
                        self.assertEqual(call.args[0].parent, expected_target / profile_dir)
                if expected_target != rust / "target":
                    self.assertFalse((rust / "target").exists())
                return run.call_args_list, copy.call_count, output if output.exists() else None

    def test_focused_test_skips_runtime_build_and_preserves_filter(self):
        calls, copies, output = self.run_action("test", "--", "-p", "codex-core", "--lib", "--", "capture_delivery")
        self.assertEqual(len(calls), 1)
        command, env = calls[0].args
        self.assertEqual(command, ["cargo", "test", "--locked", "--profile", "dev-small", "-p", "codex-core", "--lib", "--", "capture_delivery"])
        self.assertEqual(env["RUST_MIN_STACK"], "8388608")
        self.assertEqual(env["OPENSSL_STATIC"], "1")
        self.assertEqual(env["RUSTY_V8_ARCHIVE"], "pinned-v8")
        self.assertNotIn("BASH_ENV", env)
        self.assertNotIn("MEKUGI_EXEC_TRACK", env)
        self.assertEqual(copies, 0)
        self.assertFalse(output)

    def test_check_uses_metadata_only_command_without_packaging(self):
        for args, selectors in [
            ([], ["-p", "codex-app-server", "-p", "codex-app-server-subprocess-tests", "--tests"]),
            (["--", "-p", "codex-core", "--tests"], ["-p", "codex-core", "--tests"]),
        ]:
            with self.subTest(args=args):
                calls, copies, output = self.run_action("check", *args)
                self.assertEqual(calls[0].args[0], ["cargo", "check", "--locked", "--profile", "dev-small", *selectors])
                self.assertEqual(len(calls), 1)
                self.assertEqual(copies, 0)
                self.assertFalse(output)

    def test_default_regression_suite_keeps_runtime_helpers(self):
        calls, copies, output = self.run_action("test", "--profile", "")
        selection = ["--locked", "--profile", "dev-small", "-p", "codex-app-server",
                     "-p", "codex-code-mode-host", "-p", "codex-bwrap",
                     "-p", "codex-app-server-subprocess-tests"]
        build_cmd, compile_env = calls[0].args
        self.assertEqual(build_cmd, ["cargo", "build", *selection, "--bin", "mekugi-app-server",
                                     "--bin", "codex-code-mode-host", "--bin", "bwrap",
                                     "--bin", "exec-server", "--test", "subprocess"])
        self.assertEqual(calls[1].args[0], ["cargo", "test", *selection, "--bin", "mekugi-app-server"])
        self.assertEqual(copies, 4)
        self.assertEqual(len(calls), 3)
        run_suite, suite_env = calls[2].args
        self.assertEqual(run_suite, ["cargo", "test", *selection, "--test", "subprocess"])
        for name in ("mekugi-app-server", "codex-code-mode-host"):
            key = f"CARGO_BIN_EXE_{name}"
            self.assertNotIn(key, compile_env)
            self.assertEqual(suite_env[key], str(output / name))
        self.assertNotIn("CARGO_BIN_EXE_exec-server", compile_env)
        self.assertEqual(Path(suite_env["CARGO_BIN_EXE_exec-server"]),
                         output.parent / "shared-target/dev-small/exec-server")

    def test_filtered_integration_uses_staged_pair_only_during_execution(self):
        test_filter = "websocket"
        calls, copies, output = self.run_action("test", "--test-filter", test_filter)
        self.assertEqual([call.args[0][1] for call in calls], ["build", "test"])
        self.assertEqual(copies, 4)
        compile_cmd, compile_env = calls[0].args
        run_cmd, run_env = calls[1].args
        targets = ["--test", "subprocess", "--test", "all"]
        self.assertEqual(compile_cmd[-4:], targets)
        self.assertEqual(run_cmd, ["cargo", "test", *compile_cmd[2:compile_cmd.index("--bin")],
                                   *targets, "--", test_filter])
        for name in ("mekugi-app-server", "codex-code-mode-host"):
            key = f"CARGO_BIN_EXE_{name}"
            self.assertNotIn(key, compile_env)
            self.assertEqual(run_env[key], str(output / name))

    def test_explicit_profile_and_stack_override_defaults(self):
        calls, _, _ = self.run_action("test", "--profile", "release", "--", "-p", "codex-core", "--lib", stack="16777216")
        self.assertEqual(calls[0].args[0][4], "release")
        self.assertEqual(calls[0].args[1]["RUST_MIN_STACK"], "16777216")

    def test_local_build_and_release_packaging_lto(self):
        for arguments, override, expected_lto in [
            (("build", "--profile", ""), None, "off"),
            (("build", "--profile", ""), "thin", "thin"),
            (("build", "--profile", "release"), None, None),
            (("package", "--profile", ""), None, None),
        ]:
            with self.subTest(arguments=arguments, override=override):
                calls, copies, _ = self.run_action(*arguments, release_lto=override)
                self.assertEqual(len(calls), 1)
                command, env = calls[0].args
                self.assertEqual(command[:5], ["cargo", "build", "--locked", "--profile", "release"])
                self.assertNotIn("exec-server", command)
                self.assertEqual(env.get("CARGO_PROFILE_RELEASE_LTO"), expected_lto)
                self.assertEqual(copies, 4)

    def test_cargo_target_directory_configuration(self):
        for config, environment in [
            ("shared-target", None),
            ("/configured-target", None),
            ("shared-target", "relative-override"),
            ("shared-target", "/environment-target"),
            (None, None),
        ]:
            with self.subTest(config=config, environment=environment):
                self.run_action("build", target_config=config, target_env=environment)


if __name__ == "__main__":
    unittest.main()
