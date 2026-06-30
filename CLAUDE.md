# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

This is a personal fork of [rclone](https://github.com/rclone/rclone). The fork carries
its own topic branches and dev tooling on top of upstream; this file documents that
layout, since it is not obvious from the source.

## Branch topology

Three kinds of branch matter here (`upstream/master` is the rclone mainline; `mainline`
in the justfile defaults to it):

- `feature/*` — public, single-topic branches intended to be PR'd against upstream.
- `private/feature/*` — semi-private single-topic branches, **not** meant for upstream
  (e.g. `private/feature/devenv`, which carries the `justfile` and this `CLAUDE.md`).
- `private/all` — the integration branch that contains *everything*: a chosen base plus
  every topic branch's commits cherry-picked on top. This is what you actually run.

## Workflow

1. Branch `private/all` to a scratch branch `work/dev` and do the work there — you have
   the whole combined tree available while developing.
2. Split each finished commit onto the right topic branch: `feature/*` for changes
   destined for upstream, `private/feature/*` for fork-local ones.
3. Run `just regen-all` to reconcile. It rebuilds `private/all` as `<base>` + each
   topic's own commits, cherry-picked (`feature/*` first, then `private/feature/*`).
   It does **not** rebase the topic branches: each stays on the upstream commit it
   forked from, and its own commits are taken as `git merge-base <branch>
   upstream/master`..`<branch>`. That keeps the topic branches stable as upstream
   advances — no churn, no force-pushing just to track mainline.

   `just regen-all <base>` builds `private/all` on a different `<base>` (e.g. a stable
   release tag) while still taking each topic's commits relative to the mainline.

On a cherry-pick conflict, `regen-all` halts. Resolve and `git cherry-pick --continue`
(or `--abort`) — do **not** re-run `regen-all`, which would reset `private/all`.

Use `just push-features` to push the topic branches to `origin`
(`--force-with-lease`, for when you've amended or rebased one yourself).

## Build

Go is not installed system-wide (NixOS); recipes run it via the flake dev shell
(`nix develop`, which pins the toolchain through `flake.lock`) and redirect the Go caches to
`~/.cache` (the default `~/go` GOPATH is read-only under the sandbox). All builds produce a
fully static (`CGO_ENABLED=0`) binary with the git-describe version baked in.

- `just build` — static binary for the host → `rclone-static`
- `just cross <goos> <goarch>` — cross-compile → `rclone-static-<goos>-<goarch>`
- `just release` — static binaries for the common platforms (linux/darwin/windows)
- `just version` — build, then print the binary's reported version
- `just clean` — remove built static binaries

For normal Go work inside `nix-shell -p go`, the usual commands apply, e.g.
`go test ./fs/...` or a single package's tests with `go test ./backend/s3/`.
