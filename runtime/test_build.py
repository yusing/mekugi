"""Native runner routing checks, without a Rust build or dependency download."""

import importlib.util
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


class NativeRunnerTests(unittest.TestCase):
    def run_action(self, *arguments, stack=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            rust = root / "codex-rs"
            rust.mkdir()
            (rust / "Cargo.lock").touch()
            targets = ModuleType("codex_package.targets")
            targets.TARGET_SPECS = {"x86_64-unknown-linux-gnu": SimpleNamespace(is_linux=True)}
            v8 = ModuleType("codex_package.v8")
            v8.resolve_codex_v8_cargo_env = lambda *_args, **_kwargs: {"RUSTY_V8_ARCHIVE": "pinned-v8"}
            environment = {"CARGO_TARGET_DIR": str(root / "target"), "BASH_ENV": "hook", "MEKUGI_EXEC_TRACK": "1"}
            if stack is not None:
                environment["RUST_MIN_STACK"] = stack
            output = root / "output"
            # Wrapper flags precede the separator; Cargo arguments remain untouched.
            argv = ["build.py", arguments[0], "--output", str(output), *arguments[1:]]
            with (
                patch.object(build, "RUST", rust),
                patch.object(build, "CODEX", root),
                patch.object(sys, "argv", argv),
                patch.object(sys, "path", sys.path.copy()),
                patch.dict(os.environ, environment, clear=True),
                patch.dict(sys.modules, {"codex_package.targets": targets, "codex_package.v8": v8}),
                patch.object(build.subprocess, "check_output", return_value="host: x86_64-unknown-linux-gnu\n"),
                patch.object(build, "run") as run,
                patch.object(build.shutil, "copy2") as copy,
            ):
                build.main()
                return run.call_args_list, copy.call_count, output.exists()

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
            ([], ["-p", "codex-app-server", "--tests"]),
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
        self.assertEqual([call.args[0][1] for call in calls], ["build", "test", "test"])
        self.assertTrue(all(call.args[0][4] == "dev-small" for call in calls))
        self.assertIn("codex-code-mode-host", calls[0].args[0])
        self.assertIn("bwrap", calls[0].args[0])
        self.assertEqual(copies, 4)
        self.assertTrue(output)

    def test_explicit_profile_and_stack_override_defaults(self):
        calls, _, _ = self.run_action("test", "--profile", "release", "--", "-p", "codex-core", "--lib", stack="16777216")
        self.assertEqual(calls[0].args[0][4], "release")
        self.assertEqual(calls[0].args[1]["RUST_MIN_STACK"], "16777216")

    def test_production_build_remains_release(self):
        calls, copies, _ = self.run_action("build")
        self.assertEqual(calls[0].args[0][:5], ["cargo", "build", "--locked", "--profile", "release"])
        self.assertEqual(copies, 4)


if __name__ == "__main__":
    unittest.main()
