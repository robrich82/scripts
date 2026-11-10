#!/bin/sh
# PreToolUse on Read: this repo's openapi.json is too large to Read; the plex-api skill queries it with jq.
# Plain sed, so a missing jq can't block every Read. Another project's openapi.json is left alone.
path=$(sed -n 's/.*"file_path"[[:space:]]*:[[:space:]]*"\(\([^"\\]\|\\.\)*\)".*/\1/p' | sed 's/\\\\/\\/g')
case "$path" in
  */[Oo][Pp][Ee][Nn][Aa][Pp][Ii].[Jj][Ss][Oo][Nn] | *\\[Oo][Pp][Ee][Nn][Aa][Pp][Ii].[Jj][Ss][Oo][Nn]) ;;
  *) exit 0 ;;
esac
command -v cygpath >/dev/null 2>&1 && path=$(cygpath -u "$path")
[ -d "$(dirname "$path")/.claude/skills/plex-api" ] || exit 0
echo 'openapi.json is too large to read. Use the plex-api skill: its search.jq and detail.jq query the spec.' >&2
exit 2
