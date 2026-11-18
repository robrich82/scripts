---
name: finish
description: Full done-with-a-feature pipeline: vet, build affected tool(s), test, port and README checks, then commit. Use this skill whenever a feature or fix is ready to wrap up — it handles the vet/build/test/commit sequence so nothing gets skipped.
---

Run the complete end-of-feature pipeline for this repo.

## Steps

1. **Vet** — run `mise run vet` and `mise run vet-linux`. Fix any reported issues before continuing. Do not skip.
2. **Build** — run `git diff --name-only HEAD`, extract the top-level directory from each changed path (e.g. `mkv-music/audio_timestamps.go` → `mkv-music`), and run `mise run build <dirname>...` with those names. If you're unsure, run `mise run build` with no names to build every module.
3. **Test** — run `mise run test`. All tests must pass before committing.
4. **Port check** — if any module's Go code changed, run the `twin-port-checker` agent. Port anything it reports as NEEDS PORT, or tell the user why not.
5. **README** — if the changes add a new tool, change a tool's commands/flags/config, or add new functionality, update README.md to reflect the change. Then, if any module's Go code changed, run the `readme-drift-checker` agent and fix what it reports as MISSING, STALE or REMOVED. Mention its `Older:` lines to the user rather than fixing them.
6. **Commit** — stage changed files and create a git commit with a concise, descriptive message following conventional commits style. Do not add a `Co-Authored-By` trailer.

Stop and report if any step fails. Do not commit if vet, build, or tests fail.
