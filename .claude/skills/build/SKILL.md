---
name: build
description: Build one or more tools in this repo with `mise run build` after code changes. Use this skill whenever code has been edited and a build is needed — don't invoke `go build` directly, always go through this skill.
---

Build the tool(s) affected by recent changes with `mise run build`.

## Naming convention

`mise run build` takes module directory names: `mkv-music/` → `mise run build mkv-music`. With no names it builds every top-level directory that has a `go.mod`, so future modules need no change here.

## Steps

1. Check which tool directories contain recent changes: `git diff --name-only HEAD`.
2. Extract the top-level directory name from each changed path (e.g. `mkv-music/audio_timestamps.go` → `mkv-music`).
3. Run `mise run build <dir> [<dir>...]` with those names. If you're unsure which modules changed, run `mise run build` with no names to build them all. Never invoke `go build` directly.
4. Report the result. If the build fails, show the error output and stop.

If $ARGUMENTS is provided, treat it as the tool name(s) to build (e.g. `mkv-audit` → `mise run build mkv-audit`).
