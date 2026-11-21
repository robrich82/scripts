#!/bin/sh
# PreToolUse on Read: this repo's openapi.json and tmdb-openapi.json are too large to Read; the plex-api
# and tmdb-api skills query them with jq. Plain sed, so a missing jq can't block every Read.
# Another project's files of the same name are left alone.
path=$(sed -n 's/.*"file_path"[[:space:]]*:[[:space:]]*"\(\([^"\\]\|\\.\)*\)".*/\1/p' | sed 's/\\\\/\\/g')
case "$path" in
  */[Oo][Pp][Ee][Nn][Aa][Pp][Ii].[Jj][Ss][Oo][Nn] | *\\[Oo][Pp][Ee][Nn][Aa][Pp][Ii].[Jj][Ss][Oo][Nn])
    name=openapi.json skill=plex-api ;;
  */[Tt][Mm][Dd][Bb]-[Oo][Pp][Ee][Nn][Aa][Pp][Ii].[Jj][Ss][Oo][Nn] | *\\[Tt][Mm][Dd][Bb]-[Oo][Pp][Ee][Nn][Aa][Pp][Ii].[Jj][Ss][Oo][Nn])
    name=tmdb-openapi.json skill=tmdb-api ;;
  *) exit 0 ;;
esac
command -v cygpath >/dev/null 2>&1 && path=$(cygpath -u "$path")
[ -d "$(dirname "$path")/.claude/skills/$skill" ] || exit 0
echo "$name is too large to read. Use the $skill skill: its jq filters query the spec." >&2
exit 2
