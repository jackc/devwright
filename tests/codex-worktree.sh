#!/usr/bin/env bash
# Run as the development user in a provisioned Linux VM. No model or login needed.
set -euo pipefail
fixture=$(mktemp -d "$HOME/devwright-codex-worktree.XXXXXX")
trap 'rm -rf -- "$fixture"' EXIT
worktree_fixture=$(mktemp -d "$HOME/.codex/devwright-worktree.XXXXXX")
trap 'rm -rf -- "$fixture" "$worktree_fixture"' EXIT
git init -q -b main "$fixture/repo"
git -C "$fixture/repo" config user.name 'Devwright test'
git -C "$fixture/repo" config user.email 'test@example.invalid'
git -C "$fixture/repo" config commit.gpgsign false
git -C "$fixture/repo" config core.hooksPath /dev/null
git -C "$fixture/repo" commit -qm seed --allow-empty
git -C "$fixture/repo" worktree add -q --detach "$worktree_fixture/worktree"
if codex sandbox --include-managed-config -P :workspace -C "$worktree_fixture/worktree" \
  git switch -c codex/regression > "$fixture/output" 2>&1; then
  echo "FAIL default sandbox unexpectedly allowed protected Git writes" >&2
  exit 1
fi
if ! grep -Eq 'Read-only file system|Permission denied|unable to create directory' "$fixture/output"; then
  cat "$fixture/output" >&2
  exit 1
fi
echo "PASS default sandbox denies protected Git writes; approval required"
