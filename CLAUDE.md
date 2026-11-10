# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Language
All code in this project should be written in Go where possible.

## Git Commits
After making any code changes, always create a git commit. Use a concise, conventional-commits-style message that reflects the change, with no `Co-Authored-By` trailer. Fix any issues reported by `make vet` or `make vet-linux` before committing.

## README
When adding a new tool, changing a tool's commands/flags/config, or adding new functionality, update README.md to reflect the change. The `readme-drift-checker` agent compares a tool's README section with the code; `/finish` runs it.

## Build, Test & Lint
Use the `/build`, `/check`, and `/finish` skills for these workflows. Never invoke `go build` directly — always use `make`.
In a worktree, run `make` and these skills from the worktree directory, and give `twin-port-checker` the worktree path. From the main checkout they build, test and diff `main` instead.

- `make all` / `make <tool>` — build to `dist/<tool>.exe`. The make target always equals the module directory name.
- `make vet` — `go vet`, `go mod tidy -diff`, `golangci-lint run` and `golangci-lint fmt --diff` per module (fails on unformatted files or an untidy `go.mod`/`go.sum`). The linters and formatter are configured in `.golangci.yml` and pinned in `mise.toml`; `mise install` provides them. Suppress a finding with `//nolint:<linter> // reason`, not staticcheck's `//lint:ignore`.
- `make vet-linux` — `GOOS=linux go vet ./...` and `GOOS=linux golangci-lint run ./...` per module, to type-check and lint the non-Windows stubs.
- `make test` — `go test ./...` per module.
- `make vuln` — `govulncheck` per module (needs network).
- `make clean` — delete `dist/`.
- Single test while iterating: `cd <tool> && go test -run TestName ./...` — still run `make test` before committing.

CI (`.github/workflows/ci.yml`) runs on `windows-2025`, one job per changed module (every module on the weekly run or a `ci.yml`, `mise.toml` or `.golangci.yml` change). Each job runs the equivalent of `make vet`, `make vet-linux` and `make test`, plus govulncheck, which only fails the weekly run.

Hooks in `.claude/settings.json` run `gofmt -w` on every edited `.go` file and block direct `go build`, in sessions started at the repo root.
- The block is best-effort: `sh -c "go build"`, a quoted `"go" build` and `$cmd build` get through.
- They need `jq` on PATH. Without it every Bash and PowerShell call is refused until Claude Code restarts from a shell that has it.
- After changing a hook, run `sh .claude/hooks/test.sh` from the repo root.

## Architecture

Six independent tools, each its own Go module (`package main`) with its own `go.mod` — there is no shared library module. Consequences:

- **mkv-movie and mkv-tvshow duplicate a lot of code** (`rating.go`, `rating_cache.go`, `classification.go`, `set_rating.go`, `reset_rating.go`, `plex*.go` multi-account logic, `filetime*.go`, `appdir.go`). The copies have drifted; a bug fix in one usually needs porting to the other.
- **Other code is shared across tools, not just the twins**: `--clean` pruning, offline-drive detection and walk/stat error handling recur, and bugs in them have been fixed one tool at a time. Run the `twin-port-checker` agent after changing any tool; it checks both the twins and these shared shapes.
- Adding a new tool means updating every Makefile target (`all`, `test`, `vet`, `vet-linux`, `vuln`, `.PHONY`), the README tools table, and possibly a `configs/` junction (see the `configs-symlink` skill).

Common shape of each tool:

- `main.go` parses flags; one-shot modes (`--clean`, `--reset-rating`, `--set-rating`, …) run and return before the main processing path.
- Every side effect — `mkvmerge`/`mkvpropedit`/`mkvextract`/`magick` subprocesses, TMDB and Plex HTTP clients, stdin prompts, file timestamps — sits behind an interface (`MKVEditor`, `TMDBClient`, `PlexClient`, `Prompter`, `FileTimer`, `Checker`, `LocalDetector`, …) so tests substitute fakes and use `t.TempDir()` for the filesystem.
- Config, caches and state live in `%APPDATA%\<tool>\` (`$XDG_CONFIG_HOME/<tool>/` elsewhere). `configs/` in the repo holds gitignored junctions to those dirs for viewing real config.
- Platform-specific code uses `//go:build windows` / `!windows` file pairs (Windows file creation time, process detection in sw-update).

Cross-tool coupling: mkv-audit's track trimming reads `manual_data.json` from `%APPDATA%\mkv-tvshow\` or `%APPDATA%\mkv-movie\` (by library `type`) to know which audio languages each Plex account uses. Changing that file's `language`/`alt-language`/`alt2-language` keys affects both tools.

Environment: `TMDB_API_KEY` (mkv-movie, mkv-tvshow; optional for mkv-audit), `PLEX_URL`, `PLEX_TOKEN` (optional Plex integration in mkv-movie, mkv-tvshow, mkv-music), `PLEX_ALT_TOKEN`, `PLEX_ALT2_TOKEN` (mkv-movie/mkv-tvshow: only per-account stream/language prefs use these), `GITHUB_PERSONAL_ACCESS_TOKEN` (sw-update rate limit).

## Plex API
The Plex API OpenAPI spec is at `openapi.json` in the repo root. Refer to it through the `plex-api` skill when working with Plex API endpoints, request/response shapes, or authentication.
