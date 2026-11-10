#!/bin/sh
# PreToolUse on Edit|Write: copy a tool's live config file aside before it changes.
# configs/<tool> are junctions into %APPDATA%\<tool>, which git can't restore.
# Covers either path, and any module dir with a go.mod. Shell writes bypass it.
fail() { echo "backup-config: $1; the edit is refused until a backup can be made." >&2; exit 2; }

file=$(jq -j '.tool_input.file_path // empty') || fail 'could not read the hook input. If jq is missing, install it and restart Claude Code from a shell that has it on PATH'
[ -n "$file" ] || exit 0
command -v cygpath >/dev/null 2>&1 && file=$(cygpath -u "$file")
[ -f "$file" ] || exit 0 # a new file has nothing to lose

conf=${APPDATA:-${XDG_CONFIG_HOME:-$HOME/.config}}
store=${LOCALAPPDATA:-${XDG_STATE_HOME:-$HOME/.local/state}}/claude-config-backups
command -v cygpath >/dev/null 2>&1 && { conf=$(cygpath -u "$conf"); store=$(cygpath -u "$store"); }
real=$(realpath "$file") || fail "could not resolve $file"
conf=$(realpath "$conf" 2>/dev/null) || exit 0

# Windows paths are case-insensitive, so compare lowercased.
lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
case "$(lower "$real")" in
  "$(lower "$conf")"/*/*) ;;
  *) exit 0 ;;
esac
rel=$(printf '%s' "$real" | cut -c$((${#conf} + 2))-)
tool=${rel%%/*}
[ -f "${CLAUDE_PROJECT_DIR:-.}/$tool/go.mod" ] || exit 0

dest="$store/$rel"
mkdir -p "$(dirname "$dest")" || fail "could not create $(dirname "$dest")"
stamp=$(date +%Y%m%d-%H%M%S)
out="$dest.$stamp"
n=0
while [ -e "$out" ]; do n=$((n + 1)); out="$dest.$stamp-$n"; done
cp -p "$real" "$out" || fail "could not copy $real to $out"

# Keep the newest 20 per file.
ls -1 "$dest".[0-9]* 2>/dev/null | sort -r | tail -n +21 | while IFS= read -r old; do rm -f "$old"; done

msg="Backed up $tool/${rel#*/} to $out"
command -v cygpath >/dev/null 2>&1 && msg="Backed up $tool/${rel#*/} to $(cygpath -w "$out")"
jq -n --arg m "$msg" '{systemMessage: $m}'
