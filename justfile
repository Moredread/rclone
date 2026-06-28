# Build a static rclone binary.
# Go isn't installed system-wide on this NixOS box, so recipes run go
# through `nix-shell -p go`. Caches are redirected to ~/.cache because the
# default GOPATH (~/go) is read-only under the sandbox.

# Version string baked into the binary (git describe, falls back to commit).
tag := `git describe --tags --always --dirty 2>/dev/null || echo unknown`

ldflags := "-s -w -X github.com/rclone/rclone/fs.Version=" + tag

export GOPATH := env_var('HOME') / ".cache/go"
export GOCACHE := env_var('HOME') / ".cache/go-build"
export GOMODCACHE := env_var('HOME') / ".cache/go/pkg/mod"

# Wrapper so every recipe gets go without a system install.
go := "nix-shell -p go --run"

# List available recipes.
default:
    @just --list

# Mainline that topic branches are developed on (and PR'd against).
mainline := "upstream/master"

# Regenerate everything from a fresh base.
#
# <base> (default = mainline, upstream/master) controls only what private/all is
# built on; it may be a divergent branch such as a stable release. Each topic
# branch's own commits are always taken relative to the mainline, so:
#   1. every feature/* and private/feature/* topic branch is rebased onto the
#      mainline, staying a clean single-topic branch (its PR home);
#   2. private/all is rebuilt as <base> + every topic branch's own commits
#      (feature/* first, then private/feature/*), cherry-picked in. Cherry-pick
#      is used because <base> need not share history with the topic branches.
#
# On conflict the in-progress rebase/cherry-pick is left for you to resolve;
# finish it with `git rebase --continue` / `git cherry-pick --continue` (do NOT
# re-run regen-all mid-operation — it would reset private/all). `--abort` backs
# out the current step.
regen-all base=mainline:
    #!/usr/bin/env bash
    set -euo pipefail
    base="{{base}}"
    mainline="{{mainline}}"

    if ! git diff --quiet || ! git diff --cached --quiet; then
        echo "error: working tree has uncommitted tracked changes; commit or stash first" >&2
        exit 1
    fi
    if [ -d .git/rebase-merge ] || [ -d .git/rebase-apply ] || [ -f .git/CHERRY_PICK_HEAD ]; then
        echo "error: a rebase or cherry-pick is already in progress; finish or abort it first" >&2
        exit 1
    fi

    # Refresh the remotes backing the mainline and <base>.
    for ref in "$mainline" "$base"; do
        if [[ "$ref" == */* ]]; then
            remote="${ref%%/*}"
            echo ">> fetching $remote"
            git fetch "$remote"
        fi
    done

    mainref="$(git rev-parse --verify "$mainline^{commit}")"
    baseref="$(git rev-parse --verify "$base^{commit}")"
    echo ">> mainline: $mainline ($(git rev-parse --short "$mainref")); base: $base ($(git rev-parse --short "$baseref"))"

    # feature/* topics first, then private/feature/*.
    mapfile -t branches < <(git for-each-ref --format='%(refname:short)' \
        'refs/heads/feature/*' 'refs/heads/private/feature/*')

    trap 'echo ">> halted (conflict?). Resolve and run git rebase/cherry-pick --continue (or --abort)." >&2' ERR

    # 1. Rebase each topic branch onto the mainline (keeps it a clean topic).
    for b in "${branches[@]}"; do
        fork="$(git merge-base "$b" "$mainref" 2>/dev/null || true)"
        if [ -z "$fork" ]; then
            echo ">> skipping $b: no common ancestor with $mainline" >&2
            continue
        fi
        echo ">> rebasing $b onto $mainline"
        git rebase --onto "$mainref" "$fork" "$b"
    done

    # 2. Rebuild private/all = base + every topic branch's own commits.
    #    Own commits are (merge-base with mainline)..branch, so they replay
    #    cleanly onto a <base> that need not share the topic branch's history.
    git checkout -B private/all "$baseref"
    for b in "${branches[@]}"; do
        fork="$(git merge-base "$b" "$mainref" 2>/dev/null || true)"
        [ -z "$fork" ] && continue
        if [ -n "$(git rev-list "$fork..$b")" ]; then
            echo ">> integrating $b into private/all"
            git cherry-pick "$fork..$b"
        fi
    done
    echo ">> private/all regenerated at $(git rev-parse --short private/all)"

# Build a fully static binary for the host platform.
build:
    {{go}} "CGO_ENABLED=0 go build -trimpath -ldflags '{{ldflags}}' -o rclone-static ."
    @echo "Built rclone-static ({{tag}})"

# Cross-compile a static binary. Usage: just cross linux arm64
cross goos goarch:
    {{go}} "GOOS={{goos}} GOARCH={{goarch}} CGO_ENABLED=0 go build -trimpath -ldflags '{{ldflags}}' -o rclone-static-{{goos}}-{{goarch}} ."
    @echo "Built rclone-static-{{goos}}-{{goarch}} ({{tag}})"

# Build static binaries for the common platforms.
release: (cross "linux" "amd64") (cross "linux" "arm64") (cross "darwin" "amd64") (cross "darwin" "arm64") (cross "windows" "amd64")

# Print version reported by the locally built binary.
version: build
    ./rclone-static version

# Remove built static binaries.
clean:
    rm -f rclone-static rclone-static-*
