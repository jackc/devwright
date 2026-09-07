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
codex sandbox --include-managed-config -P vm_dev -C "$worktree_fixture/worktree" \
  bash -euc '
    fixture=$1
    worktree=$2
    git switch -c codex/regression
    printf "feature\n" > feature.txt
    git add feature.txt
    git commit -qm feature
    cd "$fixture/repo"
    git merge --ff-only codex/regression
    git worktree remove "$worktree"
    test "$(cat feature.txt)" = feature
    test ! -e "$worktree"
    test "$(git worktree list --porcelain | grep -c "^worktree ")" = 1
    printf "PASS sandboxed branch, commit, merge, and worktree cleanup\n"
  ' bash "$fixture" "$worktree_fixture/worktree"
