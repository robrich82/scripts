---
name: build
description: Build one or more tools in this repo using make after code changes. Use this skill whenever code has been edited and a build is needed — don't invoke `go build` directly, always go through this skill.
---

Build the tool(s) affected by recent changes using the correct make target(s).

## Naming convention

Every module directory name equals its make target. `mkv-music/` → `make mkv-music`, `sw-update/` → `make sw-update`, etc. This holds for any future modules too — no table to maintain.

To discover all valid targets, look at top-level subdirectories that contain a `go.mod` — each one is a buildable module with a matching make target.

## Steps

1. Check which tool directories contain recent changes: `git diff --name-only HEAD`.
2. Extract the top-level directory name from each changed path (e.g. `mkv-music/audio_timestamps.go` → `mkv-music`). That name is the make target.
3. If changes span multiple modules or you're unsure, use `make all`.
4. Run the target(s). Never invoke `go build` directly — always use make.
5. Report the result. If the build fails, show the error output and stop.

If $ARGUMENTS is provided, treat it as the tool name to build (e.g. `mkv-audit` → `make mkv-audit`).
