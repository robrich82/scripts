#!/bin/sh
# PostToolUse on Edit|Write: after an edit to a .go file that mkv-movie and mkv-tvshow both have,
# remind Claude of the twin copy. Advisory only, so unreadable input passes silently.
file=$(jq -j '.tool_input.file_path // empty' 2>/dev/null) || exit 0
case "$file" in *.go) ;; *) exit 0 ;; esac
command -v cygpath >/dev/null 2>&1 && file=$(cygpath -u "$file")
dir=$(dirname "$file")
case "$(basename "$dir" | tr '[:upper:]' '[:lower:]')" in
  mkv-movie) twin=mkv-tvshow ;;
  mkv-tvshow) twin=mkv-movie ;;
  *) exit 0 ;;
esac
path=$(dirname "$dir")/$twin/$(basename "$file")
[ -f "$path" ] || exit 0
# Absolute, so a session whose cwd is another checkout doesn't resolve it there.
command -v cygpath >/dev/null 2>&1 && path=$(cygpath -w "$path")
jq -n --arg t "$path" '{hookSpecificOutput: {hookEventName: "PostToolUse",
  additionalContext: "\($t) is this file'"'"'s twin. Check whether this change belongs there too; twin-port-checker checks it again at /finish."}}'
