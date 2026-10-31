---
name: plex-api
description: Look up Plex endpoints in openapi.json by path, summary, tag or operation ID, without reading the whole file. Use when working on Plex API calls, request/response shapes, or authentication.
user-invocable: false
---

`openapi.json` is about 32,000 lines, so query it with the bundled `jq` filters rather than reading it.

Run from the repo root. These are the Git Bash forms; in PowerShell, drop `MSYS_NO_PATHCONV=1`. Without it Git Bash rewrites `/` paths into Windows paths, which can return nothing with no error.

- Search: `MSYS_NO_PATHCONV=1 jq -r --arg re '<regex>' -f .claude/skills/plex-api/search.jq openapi.json` lists matching operations. The regex is case-insensitive.
- Detail: `MSYS_NO_PATHCONV=1 jq --arg route '<path>' -f .claude/skills/plex-api/detail.jq openapi.json` prints one path's operations and every component they reference.
