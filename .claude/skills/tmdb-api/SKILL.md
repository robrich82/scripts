---
name: tmdb-api
description: Look up TMDB API endpoints by path, summary or operation ID in a local copy of TMDB's OpenAPI spec, without reading the whole file. Use when working on TMDB calls in mkv-movie, mkv-tvshow or mkv-audit — request parameters, response fields, certifications, external IDs.
user-invocable: false
---

`tmdb-openapi.json` is gitignored and fetched on demand, without TMDB's examples. Run these with the Bash tool, not PowerShell, from the repo root or the worktree's root in a worktree. Without `MSYS_NO_PATHCONV=1`, Git Bash rewrites the `/3/…` paths into Windows paths.

1. Fetch the spec if it's missing or more than 30 days old (needs network):
   `[ -n "$(find tmdb-openapi.json -mtime -30 2>/dev/null)" ] || mise run tmdb-openapi`
2. Query it with the plex-api skill's filters, which work on any OpenAPI spec:
   - Search: `MSYS_NO_PATHCONV=1 jq -r --arg re '<regex>' -f .claude/skills/plex-api/search.jq tmdb-openapi.json` lists matching operations. The regex is case-insensitive.
   - Detail: `MSYS_NO_PATHCONV=1 jq --arg route '<path>' -f .claude/skills/plex-api/detail.jq tmdb-openapi.json` prints one path's operations. Paths start with `/3/`, e.g. `/3/movie/{movie_id}/release_dates`.

The spec documents Bearer-token auth. This repo's clients send the key as `api_key` in the query string instead, so their errors must pass through `redactAPIKey`.
