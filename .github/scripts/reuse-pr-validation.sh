#!/usr/bin/env bash
set -euo pipefail

validated_run() {
  local tree pr number head merged run receipt

  [[ "$FORCED" == false && "$(git rev-parse 'HEAD^1')" == "$BEFORE" ]] || return 1
  # Do not reuse results from a PR that changed how this workflow validates the tree.
  git diff --quiet "$BEFORE" "$GITHUB_SHA" -- \
    .github/workflows/release.yml .github/scripts/reuse-pr-validation.sh || return 1
  tree="$(git rev-parse "$GITHUB_SHA^{tree}")" || return 1
  pr="$(gh api "repos/$GITHUB_REPOSITORY/commits/$GITHUB_SHA/pulls" | jq -r \
    --arg sha "$GITHUB_SHA" --arg repo "$GITHUB_REPOSITORY" '
      first(.[] | select(.merge_commit_sha == $sha and .merged_at != null and
        .base.ref == "main" and .base.repo.full_name == $repo and .head.repo.full_name == $repo) |
        [.number, .head.sha, .merged_at] | @tsv)
    ')" || return 1
  IFS=$'\t' read -r number head merged <<< "$pr"
  [[ "$number" =~ ^[0-9]+$ && "$head" =~ ^[0-9a-f]{40}$ && -n "$merged" ]] || return 1

  run="$(gh api --method GET "repos/$GITHUB_REPOSITORY/actions/workflows/release.yml/runs" \
    -f event=pull_request -f head_sha="$head" -F per_page=100 | jq -r \
    --arg repo "$GITHUB_REPOSITORY" --arg head "$head" --arg merged "$merged" '
      first(.workflow_runs[] | select(.head_sha == $head and .head_repository.full_name == $repo)) |
      select(.status == "completed" and .conclusion == "success" and .run_attempt == 1 and .updated_at < $merged) | .id
    ')" || return 1
  [[ "$run" =~ ^[0-9]+$ ]] || return 1

  receipt="validated-pr-$number-$tree"
  [[ "$(gh api --method GET "repos/$GITHUB_REPOSITORY/actions/runs/$run/artifacts" \
    -f name="$receipt" | jq -r --arg name "$receipt" 'any(.artifacts[]; .name == $name and .expired == false)')" == true ]] || return 1
  printf '%s\n' "$run"
}

if run="$(validated_run)"; then
  printf '%s\n' "$run" > "$RUNNER_TEMP/native-approved-run"
  echo "run_id=$run" >> "$GITHUB_OUTPUT"
  printf 'This exact tree passed the full matrix before merge in [PR workflow run %s](%s/%s/actions/runs/%s).\n' \
    "$run" "$GITHUB_SERVER_URL" "$GITHUB_REPOSITORY" "$run" >> "$GITHUB_STEP_SUMMARY"
else
  echo "No reusable PR validation found; running the full main suite."
fi
