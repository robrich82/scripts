#!/bin/sh
# PreToolUse on Read: openapi.json is 1.3 MB; the plex-api skill queries it with jq instead.
# Plain grep, so a missing jq can't block every Read.
if grep -Eq '"file_path"[[:space:]]*:[[:space:]]*"([^"\\]|\\.)*([/]|\\\\)openapi\.json"'; then
  echo 'openapi.json is too large to read. Use the plex-api skill: its search.jq and detail.jq query the spec.' >&2
  exit 2
fi
exit 0
