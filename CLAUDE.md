# Project Instructions

## Language
All code in this project should be written in Go where possible.

## Git Commits
After making any code changes, always create a git commit. Use a concise, descriptive commit message that reflects the change. Fix any issues reported by `make vet` or `gofmt` before committing.

## README
When adding a new tool, changing a tool's commands/flags/config, or adding new functionality, update README.md to reflect the change.

## Build, Test & Lint
Use the `/build`, `/check`, and `/finish` skills for these workflows. Never invoke `go build` directly — always use `make`.

## Plex API
The Plex API OpenAPI spec is at `openapi.json` in the repo root. Refer to it when working with Plex API endpoints, request/response shapes, or authentication.
