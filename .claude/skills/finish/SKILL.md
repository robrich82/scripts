---
name: finish
description: Full done-with-a-feature pipeline: vet, build affected tool(s), test, then commit. Use this skill whenever a feature or fix is ready to wrap up — it handles the vet/build/test/commit sequence so nothing gets skipped.
disable-model-invocation: true
allowed-tools: Bash(make *) Bash(git *) Read Edit Glob Grep
---

Run the complete end-of-feature pipeline for this repo.

## Steps

1. **Vet** — run `make vet`. Fix any reported issues before continuing. Do not skip.
2. **Build** — determine which tool directories changed (via `git diff --name-only HEAD`), map to make targets, and run them. Use the mapping:
   - cbz-fix/ → `make cbz-fix`
   - mkv-audit/ → `make mkv-audit`
   - mkv-movie/ → `make mkv-movie`
   - mkv-musicvideo/ → `make mkv-musicvideo`
   - mkv-tvshow/ → `make mkv-tvshow`
   - sw-update/ → `make sw-update`
   - multiple / unsure → `make all`
3. **Test** — run `make test`. All tests must pass before committing.
4. **README** — if the changes add a new tool, change a tool's commands/flags/config, or add new functionality, update README.md to reflect the change.
5. **Commit** — stage changed files and create a git commit with a concise, descriptive message following conventional commits style. Co-author line required:
   ```
   Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
   ```

Stop and report if any step fails. Do not commit if vet, build, or tests fail.
