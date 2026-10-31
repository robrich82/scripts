---
name: plex-api
description: Look up Plex endpoints in openapi.json by path, summary, tag or operation ID, without reading the whole file. Use when working on Plex API calls, request/response shapes, or authentication.
user-invocable: false
---

`openapi.json` is about 32,000 lines, so query it with the bundled script rather than reading it.

Run from the repo root:

- Search: `node .claude/skills/plex-api/lookup.js <regex>` lists matching operations. The regex is case-insensitive.
- Detail: `node .claude/skills/plex-api/lookup.js --detail <path>` prints one path's operations and every schema they reference. Omit the leading slash, because Git Bash rewrites it into a Windows path: `--detail library/sections/prefs`.
