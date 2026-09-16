# Automated live tests

Live coverage for plaintext collaboration, journal assignment association, child completion,
and the real shell/launcher path. The [code-test fixture](CONTEXT-TESTS.md) uses installed Codex
with a deterministic local provider; this test also exercises OpenAI acceptance.

## Model and cost

The script uses **`gpt-5.6-sol` with `medium` reasoning** for both parent and child, with
`fork_turns="none"` for the initial child. Unlike the deterministic fixture, it consumes live
model usage.

## Shell, assignment, and restart test

Run [context_smoke.py](contrib/codex/context_smoke.py) from the repository root:

```sh
python3 contrib/codex/context_smoke.py --dangerously-bypass-approvals-and-sandbox
```

The script builds a temporary router and helper outside the repository, uses installed Codex
and its existing authentication without Grok, and keeps one isolated workspace for the test.
It checks real shell output, a plaintext child assignment, and a follow-up to that same child
after restarting the router and resuming the exact session.

Requirements: Go, Python 3, installed Codex, `mktemp`, and GNU `timeout`.
`--dangerously-bypass-approvals-and-sandbox` avoids the host's
`bwrap` network-namespace initialization failure.
`--help` displays options without starting builds or model calls.

Do not smoke-test the fixed `shell` helper without arguments. That opens an internal control
channel and may dispatch to the active router instead of the temporary build. Run a real script
through `functions.shell` in the new session. A version-only launch does not prove script execution.

## Evidence and limits

- Require actual shell output with exit status zero and the native child-result body with
  Question/Answer association for the current assignment. A marker appearing only in a prompt,
  an older journal item, or the parent's final prose is not success.
- Confirm child start metadata reports `gpt-5.6-sol` and `medium`; requested settings alone do
  not prove what the child actually used.
- Confirm the resumed run follows up the original child rather than creating a replacement.
  Keep the exact session ID and workspace; do not use `resume --last`.
- The script retains JSONL, stderr, and build logs in its printed temporary directory, alongside
  the launcher. Each Codex invocation has a 180-second timeout and a 15-second termination grace.
- This covers new plaintext assignments and follow-ups, not decryption of existing assignments.
  Historical encrypted-call replay, native send-message, model switching, and other transports
  need their own checks when affected.

The deterministic native fixtures live in `internal/router/journal_codex_e2e_test.go`.
They cover direct finish and Bash/POSIX finish through the real shell helper, including parent
and child terminal acceptance without a final-answer provider request. Plaintext projection and
history tests live in `internal/router/subagent_bridge_test.go`; journal source and restart tests
are in `internal/router/journal_question_source_test.go`.
