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
base=$(basename "$file")
[ -f "$(dirname "$dir")/$twin/$base" ] || exit 0
jq -n --arg t "$twin/$base" '{hookSpecificOutput: {hookEventName: "PostToolUse",
  additionalContext: "\($t) is this file'"'"'s twin. Check whether this change belongs there too; twin-port-checker checks it again at /finish."}}'
