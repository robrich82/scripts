# Project Instructions

## Language
All code in this project should be written in Go where possible.

## Git Commits
After making any code changes, always create a git commit. Use a concise, descriptive commit message that reflects the change.

## README
When adding a new tool, changing a tool's commands/flags/config, or adding new functionality, update README.md to reflect the change.

## Linting and Formatting
`make vet` runs automatically after every Go file edit via a PostToolUse hook. Fix any issues it reports before committing. When writing new Go code, ensure it is `gofmt`-formatted (use tabs for indentation).

## Build
After completing a feature or bug fix, build the affected tool(s), do not build without using the make command:
- `make mkv-audit` — after changes to mkv-audit/
- `make mkv-movie` — after changes to mkv-movie/
- `make mkv-musicvideo` — after changes to mkv-musicvideo/
- `make mkv-tvshow` — after changes to mkv-tvshow/
- `make sw-update` - after changes to sw-update/
- `make all` — to rebuild everything
