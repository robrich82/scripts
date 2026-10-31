---
name: finish
description: Full done-with-a-feature pipeline: vet, build affected tool(s), test, then commit. Use this skill whenever a feature or fix is ready to wrap up — it handles the vet/build/test/commit sequence so nothing gets skipped.
---

Run the complete end-of-feature pipeline for this repo.

## Steps

1. **Vet** — run `make vet` and `make vet-linux`. Fix any reported issues before continuing. Do not skip.
2. **Build** — run `git diff --name-only HEAD`, extract the top-level directory from each changed path (e.g. `mkv-music/audio_timestamps.go` → `mkv-music`), and run `make <dirname>` for each. Directory name equals make target — this holds for any future modules. If changes span multiple modules or you're unsure, use `make all`.
3. **Test** — run `make test`. All tests must pass before committing.
4. **Twin port** — if `mkv-movie` or `mkv-tvshow` changed, run the `twin-port-checker` agent. Port anything it reports as NEEDS PORT, or tell the user why not.
5. **README** — if the changes add a new tool, change a tool's commands/flags/config, or add new functionality, update README.md to reflect the change.
6. **Commit** — stage changed files and create a git commit with a concise, descriptive message following conventional commits style. Do not add a `Co-Authored-By` trailer.

Stop and report if any step fails. Do not commit if vet, build, or tests fail.
