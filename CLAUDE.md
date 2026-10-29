# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Language
All code in this project should be written in Go where possible.

## Git Commits
After making any code changes, always create a git commit. Use a concise, conventional-commits-style message that reflects the change, with no `Co-Authored-By` trailer. Fix any issues reported by `make vet` or `gofmt` before committing.

## README
When adding a new tool, changing a tool's commands/flags/config, or adding new functionality, update README.md to reflect the change.

## Build, Test & Lint
Use the `/build`, `/check`, and `/finish` skills for these workflows. Never invoke `go build` directly — always use `make`.

- `make all` / `make <tool>` — build to `dist/<tool>.exe`. The make target always equals the module directory name.
- `make vet` — `go vet`, `gofmt -l`, `staticcheck`, `unparam` per module (fails on unformatted files).
- `make test` — `go test ./...` per module.
- `make vuln` — `govulncheck` per module (needs network).
- Single test while iterating: `cd <tool> && go test -run TestName ./...` — still run `make test` before committing.

CI (`.github/workflows/ci.yml`) runs on `windows-latest`, one job per module, and additionally runs `GOOS=linux go vet ./...` to type-check the non-Windows stubs. `make vet` does not, so after touching a `*_windows.go` file or its `*_stub.go`/`*_other.go` counterpart, run that command in the module too.

## Architecture

Six independent tools, each its own Go module (`package main`) with its own `go.mod` — there is no shared library module. Consequences:

- **mkv-movie and mkv-tvshow duplicate a lot of code** (`rating.go`, `rating_cache.go`, `classification.go`, `set_rating.go`, `reset_rating.go`, `plex*.go` multi-account logic, `filetime*.go`, `appdir.go`). The copies have drifted slightly; a bug fix in one usually needs porting to the other.
- Adding a new tool means updating every Makefile target (`all`, `test`, `vet`, `vuln`, `.PHONY`), the CI matrix, the README tools table, and possibly a `configs/` junction (see the `configs-symlink` skill).

Common shape of each tool:

- `main.go` parses flags; one-shot modes (`--clean`, `--reset-rating`, `--set-rating`, …) run and return before the main processing path.
- Every side effect — `mkvmerge`/`mkvpropedit`/`mkvextract`/`magick` subprocesses, TMDB and Plex HTTP clients, stdin prompts, file timestamps — sits behind an interface (`MKVEditor`, `TMDBClient`, `PlexClient`, `Prompter`, `FileTimer`, `Checker`, `LocalDetector`, …) so tests substitute fakes and use `t.TempDir()` for the filesystem.
- Config, caches and state live in `%APPDATA%\<tool>\` (`$XDG_CONFIG_HOME/<tool>/` elsewhere). `configs/` in the repo holds gitignored junctions to those dirs for viewing real config.
- Platform-specific code uses `//go:build windows` / `!windows` file pairs (Windows file creation time, process detection in sw-update).

Cross-tool coupling: mkv-audit's track trimming reads `manual_data.json` from `%APPDATA%\mkv-tvshow\` or `%APPDATA%\mkv-movie\` (by library `type`) to know which audio languages each Plex account uses. Changing that file's `language`/`alt-language`/`alt2-language` keys affects both tools.

Environment: `TMDB_API_KEY` (mkv-movie, mkv-tvshow; optional for mkv-audit), `PLEX_URL`, `PLEX_TOKEN`, `PLEX_ALT_TOKEN`, `PLEX_ALT2_TOKEN` (Plex rating/stream/language writes; alt tokens only get per-account stream/language prefs), `GITHUB_PERSONAL_ACCESS_TOKEN` (sw-update rate limit).

## Plex API
The Plex API OpenAPI spec is at `openapi.json` in the repo root. Refer to it when working with Plex API endpoints, request/response shapes, or authentication.
