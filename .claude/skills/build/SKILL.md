---
name: build
description: Build one or more tools in this repo using make after code changes. Use this skill whenever code has been edited and a build is needed — don't invoke `go build` directly, always go through this skill.
disable-model-invocation: true
allowed-tools: Bash(make *) Bash(git *)
---

Build the tool(s) affected by recent changes using the correct make target(s).

## Directory → target mapping

| Changed directory | Make target          |
|-------------------|----------------------|
| cbz-fix/          | `make cbz-fix`       |
| mkv-audit/        | `make mkv-audit`     |
| mkv-movie/        | `make mkv-movie`     |
| mkv-musicvideo/   | `make mkv-musicvideo`|
| mkv-tvshow/       | `make mkv-tvshow`    |
| sw-update/        | `make sw-update`     |
| multiple / unsure | `make all`           |

## Steps

1. Check which tool directories contain recent changes: `git diff --name-only HEAD`.
2. Map each changed directory to its make target using the table above.
3. Run the target(s). Never invoke `go build` directly — always use make.
4. Report the result. If the build fails, show the error output and stop.

If $ARGUMENTS is provided, treat it as the tool name to build (e.g. `mkv-audit` → `make mkv-audit`).
