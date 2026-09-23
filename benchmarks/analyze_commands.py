#!/usr/bin/env python3
"""Summarize identity-free command behavior from retained Codex JSONL events."""

from __future__ import annotations

import fnmatch
import json
import os
import re
import shlex
import sys
from collections import Counter
from pathlib import Path, PurePosixPath

from benchmark_jsonl import load_jsonl


CATEGORIES = (
    "file_read",
    "search",
    "discovery",
    "git_diff_content",
    "git_diff_check",
    "git_diff_metadata",
    "git_status",
    "test_or_build",
    "formatter",
    "upstream_fetch",
    "other",
)

SHELLS = {"bash", "dash", "sh", "zsh"}
CONTROL_WORDS = {"case", "do", "done", "else", "esac", "fi", "for", "function", "select", "then"}
PREFIX_WORDS = {"if", "elif", "until", "while", "!"}
SEARCH_TOOLS = {"grep", "rg", "search_code"}
DISCOVERY_TOOLS = {"fd", "find", "ls"}
FORMATTERS = {"black", "gofmt", "goimports", "prettier", "rustfmt"}
METADATA_DIFF_OPTIONS = {
    "--compact-summary",
    "--dirstat",
    "--name-only",
    "--name-status",
    "--numstat",
    "--shortstat",
    "--stat",
    "--summary",
}
SEARCH_OPTIONS_WITH_VALUES = {
    "-A",
    "-B",
    "-C",
    "-e",
    "-f",
    "-g",
    "-m",
    "--after-context",
    "--before-context",
    "--context",
    "--encoding",
    "--engine",
    "--file",
    "--glob",
    "--iglob",
    "--max-count",
    "--max-depth",
    "--regexp",
    "--sort",
    "--sortr",
    "--threads",
    "--type",
    "--type-add",
    "--type-not",
}
READ_OPTIONS_WITH_VALUES = {
    "-c",
    "-n",
    "-s",
    "-v",
    "-w",
    "--bytes",
    "--lines",
    "--separator",
    "--starting-line-number",
    "--width",
}
UNRESOLVED_OPERAND = re.compile(r"[$*?\[\]{}()`]")


def strip_heredoc_bodies(command: str) -> str:
    lines = command.splitlines(keepends=True)
    output: list[str] = []
    delimiter: str | None = None
    strip_tabs = False
    pattern = re.compile(r"<<(-?)[ \t]*(['\"]?)([A-Za-z_][A-Za-z0-9_]*)\2")
    for line in lines:
        if delimiter is not None:
            candidate = line.rstrip("\r\n")
            if strip_tabs:
                candidate = candidate.lstrip("\t")
            if candidate == delimiter:
                delimiter = None
                strip_tabs = False
            continue
        output.append(line)
        match = pattern.search(line)
        if match:
            strip_tabs = match.group(1) == "-"
            delimiter = match.group(3)
    return "".join(output)


def unwrap_shell(command: str) -> str:
    try:
        words = shlex.split(command, posix=True)
    except ValueError:
        return command
    if not words or os.path.basename(words[0]) not in SHELLS:
        return command
    for index, word in enumerate(words[1:], 1):
        if word in {"-c", "-lc", "-cl"} and index + 1 < len(words):
            return words[index + 1]
    return command


def split_segments(command: str) -> list[str]:
    command = strip_heredoc_bodies(unwrap_shell(command))
    segments: list[str] = []
    current: list[str] = []
    quote: str | None = None
    escaped = False
    index = 0
    while index < len(command):
        char = command[index]
        if escaped:
            current.append(char)
            escaped = False
            index += 1
            continue
        if char == "\\" and quote != "'":
            current.append(char)
            escaped = True
            index += 1
            continue
        if quote is not None:
            current.append(char)
            if char == quote:
                quote = None
            index += 1
            continue
        if char in {"'", '"', "`"}:
            quote = char
            current.append(char)
            index += 1
            continue
        if char in "\n;|&()":
            value = "".join(current).strip()
            if value:
                segments.append(value)
            current = []
            while index + 1 < len(command) and command[index + 1] == char and char in "|&":
                index += 1
            index += 1
            continue
        current.append(char)
        index += 1
    value = "".join(current).strip()
    if value:
        segments.append(value)
    return segments


def is_assignment(word: str) -> bool:
    return re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*=.*", word, re.DOTALL) is not None


def command_words(segment: str) -> list[str]:
    try:
        words = shlex.split(segment, posix=True)
    except ValueError:
        return []
    while words and words[0] in PREFIX_WORDS:
        words.pop(0)
    if not words or words[0] in CONTROL_WORDS:
        return []
    while words and is_assignment(words[0]):
        words.pop(0)
    while words and words[0] in {"command", "env", "sudo"}:
        words.pop(0)
        while words and (words[0].startswith("-") or is_assignment(words[0])):
            words.pop(0)
    if words and words[0] == "timeout":
        words.pop(0)
        while words and words[0].startswith("-"):
            words.pop(0)
        if words:
            words.pop(0)
    return words


def decode_ansi_c_word(word: str) -> str:
    if not (word.startswith("$'") and word.endswith("'")):
        return word
    escapes = {
        "a": "\a",
        "b": "\b",
        "e": "\x1b",
        "f": "\f",
        "n": "\n",
        "r": "\r",
        "t": "\t",
        "v": "\v",
        "\\": "\\",
        "'": "'",
        '"': '"',
    }
    source = word[2:-1]
    output: list[str] = []
    index = 0
    while index < len(source):
        if source[index] != "\\" or index + 1 == len(source):
            output.append(source[index])
            index += 1
            continue
        escaped = source[index + 1]
        replacement = escapes.get(escaped)
        if replacement is not None:
            output.append(replacement)
            index += 2
            continue
        if escaped in "01234567":
            end = index + 2
            while end < min(index + 4, len(source)) and source[end] in "01234567":
                end += 1
            output.append(chr(int(source[index + 1 : end], 8) & 0xFF))
            index = end
            continue
        widths = {"x": 2, "u": 4, "U": 8}
        if escaped in widths:
            end = index + 2
            limit = min(end + widths[escaped], len(source))
            while end < limit and source[end] in "0123456789abcdefABCDEF":
                end += 1
            if end != index + 2:
                encoded = source[index + 2 : end]
                try:
                    value = int(encoded, 16)
                    if escaped in "uU" and (value > 0x10FFFF or 0xD800 <= value <= 0xDFFF):
                        raise ValueError
                    output.append(chr(value))
                except ValueError:
                    output.append(source[index:end])
                index = end
                continue
        if escaped == "c" and index + 2 < len(source) and ord(source[index + 2]) < 128:
            output.append(chr(ord(source[index + 2].upper()) ^ 64))
            index += 3
            continue
        output.extend(("\\", escaped))
        index += 2
    return "".join(output)


def shell_helper_body(segment: str) -> str | None:
    words = command_words(segment)
    if (
        len(words) < 3
        or os.path.basename(words[0]) != "shell"
        or not words[1]
    ):
        return None

    # Codex retains the fixed helper carrier, while command behavior belongs to the
    # program it carries. A failed executor-style call may retain that program as its
    # original JSON request, so recover only the established string cmd field.
    body = words[-1]
    ansi_body = re.search(r"(\$'(?:\\.|[^'])*')\s*$", segment, re.DOTALL)
    raw_body = ansi_body.group(1) if ansi_body is not None else body
    decoded_body = decode_ansi_c_word(raw_body)
    if decoded_body != raw_body:
        body = decoded_body
    try:
        request = json.loads(body)
    except json.JSONDecodeError:
        return body
    if isinstance(request, dict) and isinstance(request.get("cmd"), str):
        return request["cmd"]
    return body


def git_subcommand(words: list[str]) -> tuple[str, list[str]]:
    index = 1
    while index < len(words):
        word = words[index]
        if word in {"-C", "--git-dir", "--work-tree"} and index + 1 < len(words):
            index += 2
            continue
        if word.startswith("-"):
            index += 1
            continue
        return word, words[index + 1 :]
    return "", []


def has_explicit_file_operand(name: str, words: list[str]) -> bool:
    args = words[1:]
    if name in {"mcat", "inspect_file"}:
        return bool(args)
    if name == "cat":
        return any(not arg.startswith(("-", ">", "<")) for arg in args)
    if name in {"head", "tail", "nl"}:
        return any(not arg.startswith(("-", ">", "<")) and not arg.isdigit() for arg in args)
    if name != "sed":
        return False
    operands = [arg for arg in args if not arg.startswith(("-", ">", "<"))]
    return len(operands) >= 2


def classify(words: list[str]) -> str:
    if not words:
        return "other"
    name = os.path.basename(words[0])
    if name in {"mcat", "inspect_file", "cat", "head", "tail", "nl", "sed"} and has_explicit_file_operand(name, words):
        return "file_read"
    if name in SEARCH_TOOLS:
        return "search"
    if name in DISCOVERY_TOOLS:
        return "discovery"
    if name == "git":
        subcommand, args = git_subcommand(words)
        if subcommand == "diff":
            if "--check" in args:
                return "git_diff_check"
            if any(arg.split("=", 1)[0] in METADATA_DIFF_OPTIONS for arg in args):
                return "git_diff_metadata"
            return "git_diff_content"
        if subcommand == "status":
            return "git_status"
        if subcommand in {"clone", "fetch", "ls-remote", "pull"}:
            return "upstream_fetch"
    if name in {"curl", "wget"}:
        return "upstream_fetch"
    if name in FORMATTERS:
        return "formatter"
    if name == "ruff" and len(words) > 1 and words[1] in {"check", "format"}:
        return "formatter"
    if name == "just" and len(words) > 1 and words[1] in {"fmt", "format"}:
        return "formatter"
    if name == "go" and len(words) > 1 and words[1] in {"build", "generate", "test", "vet"}:
        return "test_or_build"
    if name in {"pytest", "make", "cmake", "ctest"}:
        return "test_or_build"
    if name in {"bun", "cargo", "npm", "npx", "pnpm", "yarn"} and any(
        word in {"build", "check", "test", "tsc", "typecheck"} for word in words[1:3]
    ):
        return "test_or_build"
    if name == "python" or name == "python3":
        if len(words) > 2 and words[1] == "-m" and words[2] in {"pytest", "unittest"}:
            return "test_or_build"
    return "other"


def normalize_changed_path(path: str) -> str:
    path = path.replace("\\", "/")
    if "/repo/" in path:
        path = path.rsplit("/repo/", 1)[1]
    return path.removeprefix("./")


def normalize_path_operand(operand: str) -> tuple[str | None, bool]:
    if not operand or operand == "-":
        return None, False
    if UNRESOLVED_OPERAND.search(operand):
        return None, True
    return normalize_changed_path(operand), False


def positional_arguments(args: list[str], options_with_values: set[str]) -> tuple[list[str], bool]:
    positional: list[str] = []
    ambiguous = False
    index = 0
    options = True
    while index < len(args):
        argument = args[index]
        if options and argument == "--":
            options = False
            index += 1
            continue
        option = argument.split("=", 1)[0]
        if options and option in options_with_values:
            if "=" not in argument:
                index += 1
            index += 1
            continue
        if options and argument.startswith("-"):
            index += 1
            continue
        normalized, unresolved = normalize_path_operand(argument)
        ambiguous = ambiguous or unresolved
        if normalized is not None:
            positional.append(normalized)
        index += 1
    return positional, ambiguous


def file_read_path_operands(words: list[str]) -> tuple[set[str], bool]:
    name = os.path.basename(words[0])
    args = words[1:]
    if name in {"mcat", "inspect_file"}:
        operands: set[str] = set()
        ambiguous = False
        for argument in args:
            for target in argument.splitlines():
                try:
                    target_words = shlex.split(target, posix=True)
                except ValueError:
                    ambiguous = True
                    continue
                if not target_words:
                    continue
                normalized, unresolved = normalize_path_operand(target_words[0])
                ambiguous = ambiguous or unresolved
                if normalized is not None:
                    operands.add(normalized)
        return operands, ambiguous
    if name == "cat":
        operands, ambiguous = positional_arguments(args, set())
        return set(operands), ambiguous
    if name in {"head", "tail", "nl"}:
        operands, ambiguous = positional_arguments(args, READ_OPTIONS_WITH_VALUES)
        return {operand for operand in operands if not operand.isdigit()}, ambiguous
    if name != "sed":
        return set(), False

    operands, ambiguous = positional_arguments(args, {"-e", "--expression", "-f", "--file"})
    if not any(argument == "-e" or argument == "--expression" or argument.startswith("--expression=") for argument in args):
        operands = operands[1:]
    return set(operands), ambiguous


def search_path_operands(words: list[str]) -> tuple[set[str], bool]:
    args = words[1:]
    operands, ambiguous = positional_arguments(args, SEARCH_OPTIONS_WITH_VALUES)
    has_explicit_pattern = any(
        argument in {"-e", "--regexp"}
        or argument.startswith("--regexp=")
        or (argument.startswith("-e") and argument != "-e")
        for argument in args
    )
    if not has_explicit_pattern:
        operands = operands[1:]
    return set(operands), ambiguous


def supported_search_include_globs(words: list[str]) -> list[str] | None:
    if os.path.basename(words[0]) != "rg":
        return []
    patterns: list[str] = []
    args = words[1:]
    index = 0
    while index < len(args):
        argument = args[index]
        if argument == "--glob":
            index += 1
            if index >= len(args):
                return None
            pattern = args[index]
        elif argument.startswith("--glob="):
            pattern = argument.removeprefix("--glob=")
        elif argument in {"-g", "--iglob"} or argument.startswith(("-g", "--iglob=")):
            return None
        else:
            index += 1
            continue
        if (
            not pattern
            or pattern.startswith("!")
            or any(character in pattern for character in "/\\[]{}")
        ):
            return None
        patterns.append(pattern)
        index += 1
    return patterns


def git_diff_path_operands(words: list[str]) -> tuple[set[str], bool]:
    subcommand, args = git_subcommand(words)
    if subcommand != "diff" or "--" not in args:
        return set(), False
    operands: set[str] = set()
    ambiguous = False
    for argument in args[args.index("--") + 1 :]:
        normalized, unresolved = normalize_path_operand(argument)
        ambiguous = ambiguous or unresolved
        if normalized is not None:
            operands.add(normalized)
    return operands, ambiguous


def is_bare_worktree_diff(words: list[str]) -> bool:
    subcommand, args = git_subcommand(words)
    return subcommand == "diff" and not args


def concrete_path_operands(category: str, words: list[str]) -> tuple[set[str], bool]:
    if category == "file_read":
        return file_read_path_operands(words)
    if category == "search":
        return search_path_operands(words)
    if category == "git_diff_content":
        return git_diff_path_operands(words)
    return set(), False


def intersects_path(segment: str, paths: set[str]) -> bool:
    return any(path and path in segment for path in paths)


def path_operand_contains(scope: str, path: str) -> bool:
    scope = scope.rstrip("/")
    if scope in {"", "."}:
        return bool(path) and not path.startswith("/")
    return path == scope or path.startswith(scope + "/")


def paths_covered_by_operands(operands: set[str], paths: set[str]) -> set[str]:
    return {
        path
        for path in paths
        if any(path_operand_contains(operand, path) for operand in operands)
    }


def analyze(paths: list[Path]) -> dict[str, object]:
    categories = {
        category: {
            "invocations": 0,
            "post_edit": 0,
            "path_intersecting_post_edit": 0,
            "path_scope_operand_post_edit": 0,
            "same_path_edit_read_edit": 0,
            "path_scope_operand_without_later_change": 0,
            "changed_path_in_non_path_operand_only": 0,
            "ambiguous_path_operand": 0,
            "workspace_wide_post_edit": 0,
            "failed_items": 0,
        }
        for category in CATEGORIES
    }
    command_items = 0
    failed_items = 0
    file_change_events = 0
    changed_path_entries = 0
    all_changed_paths: set[str] = set()
    repeated_changed_paths: set[str] = set()
    edited_paths: set[str] = set()
    invocations = 0
    same_path_loop_invocations: list[dict[str, str]] = []

    for path in paths:
        events: list[dict[str, object]] = []
        for event in load_jsonl(path):
            if event.get("type") == "item.completed":
                events.append(event)

        change_indexes: dict[str, list[int]] = {}
        for event_index, event in enumerate(events):
            item = event.get("item") or {}
            if item.get("type") != "file_change" or item.get("status", "completed") != "completed":
                continue
            for change in item.get("changes") or []:
                changed = normalize_changed_path(str(change.get("path", "")))
                if changed:
                    change_indexes.setdefault(changed, []).append(event_index)

        edited_paths = set()
        for event_index, event in enumerate(events):
            item = event.get("item") or {}
            item_type = item.get("type")
            if item_type == "file_change" and item.get("status", "completed") == "completed":
                file_change_events += 1
                for change in item.get("changes") or []:
                    changed = normalize_changed_path(str(change.get("path", "")))
                    if not changed:
                        continue
                    changed_path_entries += 1
                    if changed in all_changed_paths:
                        repeated_changed_paths.add(changed)
                    all_changed_paths.add(changed)
                    edited_paths.add(changed)
                continue
            if item_type != "command_execution":
                continue
            command_items += 1
            failed = int(item.get("exit_code") or 0) != 0 or item.get("status") == "failed"
            if failed:
                failed_items += 1
            item_categories: set[str] = set()
            for retained_segment in split_segments(str(item.get("command") or "")):
                body = shell_helper_body(retained_segment)
                segments = split_segments(body) if body is not None else [retained_segment]
                for segment in segments:
                    words = command_words(segment)
                    if not words:
                        continue
                    category = classify(words)
                    same_path_loop = False
                    invocations += 1
                    categories[category]["invocations"] += 1
                    if edited_paths:
                        categories[category]["post_edit"] += 1
                        text_intersection = intersects_path(segment, edited_paths)
                        if text_intersection:
                            categories[category]["path_intersecting_post_edit"] += 1
                        operands, ambiguous = concrete_path_operands(category, words)
                        if ambiguous:
                            categories[category]["ambiguous_path_operand"] += 1
                        prior_paths = paths_covered_by_operands(operands, edited_paths)
                        if category == "search":
                            include_globs = supported_search_include_globs(words)
                            if include_globs:
                                prior_paths = {
                                    path
                                    for path in prior_paths
                                    if any(
                                        fnmatch.fnmatchcase(PurePosixPath(path).name, pattern)
                                        for pattern in include_globs
                                    )
                                }
                        if category == "git_diff_content" and is_bare_worktree_diff(words):
                            categories[category]["workspace_wide_post_edit"] += 1
                            later_operands = {
                                operand
                                for operand in edited_paths
                                if any(index > event_index for index in change_indexes.get(operand, []))
                            }
                            if later_operands:
                                categories[category]["same_path_edit_read_edit"] += 1
                                same_path_loop = True
                        elif prior_paths:
                            categories[category]["path_scope_operand_post_edit"] += 1
                            later_paths = {
                                path
                                for path in prior_paths
                                if any(index > event_index for index in change_indexes.get(path, []))
                            }
                            if later_paths:
                                categories[category]["same_path_edit_read_edit"] += 1
                                same_path_loop = True
                            else:
                                categories[category]["path_scope_operand_without_later_change"] += 1
                        elif text_intersection and category in {"file_read", "search"}:
                            categories[category]["changed_path_in_non_path_operand_only"] += 1
                    if same_path_loop:
                        tool_call_id = item.get("tool_call_id")
                        same_path_loop_invocations.append(
                            {
                                "category": category,
                                "tool_call_id": tool_call_id if isinstance(tool_call_id, str) else "",
                            }
                        )
                    item_categories.add(category)
            if failed:
                for category in item_categories:
                    categories[category]["failed_items"] += 1

    return {
        "command_execution_items": command_items,
        "failed_command_execution_items": failed_items,
        "parsed_command_invocations": invocations,
        "file_change_events": file_change_events,
        "changed_path_entries": changed_path_entries,
        "unique_changed_paths": len(all_changed_paths),
        "repeated_changed_paths": len(repeated_changed_paths),
        "same_path_edit_read_edit_invocations": (
            categories["file_read"]["same_path_edit_read_edit"]
            + categories["search"]["same_path_edit_read_edit"]
            + categories["git_diff_content"]["same_path_edit_read_edit"]
        ),
        "same_path_loop_invocations": same_path_loop_invocations,
        "categories": categories,
    }


def main() -> int:
    if len(sys.argv) < 2:
        print(f"usage: {Path(sys.argv[0]).name} CODEX_JSONL...", file=sys.stderr)
        return 2
    paths = [Path(argument) for argument in sys.argv[1:]]
    missing = [str(path) for path in paths if not path.is_file()]
    if missing:
        print(f"analyze_commands.py: missing input: {', '.join(missing)}", file=sys.stderr)
        return 1
    try:
        result = analyze(paths)
    except (OSError, ValueError) as error:
        print(f"analyze_commands.py: {error}", file=sys.stderr)
        return 1
    json.dump(result, sys.stdout, sort_keys=True, separators=(",", ":"))
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
