#!/bin/sh
# PreToolUse on Edit|Write: copy an existing file under %APPDATA%\<tool> aside before it changes,
# for tools with a go.mod here. configs/<tool> junctions resolve to the same files, which git can't restore.
# Shell writes bypass it.
fail() { echo "backup-config: $1; the edit is refused." >&2; exit 2; }
win=; command -v cygpath >/dev/null 2>&1 && win=1
topath() { if [ -n "$win" ]; then cygpath -u "$1"; else printf '%s' "$1"; fi; }
lower() { if [ -n "$win" ]; then printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; else printf '%s' "$1"; fi; }

file=$(jq -j '.tool_input.file_path // empty') || fail 'could not read the hook input. If jq is missing, install it and restart Claude Code from a shell that has it on PATH'
[ -n "$file" ] || exit 0
file=$(topath "$file") || fail "could not convert $file"
[ -f "$file" ] || exit 0 # a new file has nothing to lose
real=$(realpath "$file") || fail "could not resolve $file"

if [ -n "$win" ]; then # cygpath -F: the shell folders, should the env vars be missing
  conf=$(if [ -n "$APPDATA" ]; then cygpath -u "$APPDATA"; else cygpath -u -F 26; fi) || fail 'could not find APPDATA'
  store=$(if [ -n "$LOCALAPPDATA" ]; then cygpath -u "$LOCALAPPDATA"; else cygpath -u -F 28; fi) || fail 'could not find LOCALAPPDATA'
else
  conf=${XDG_CONFIG_HOME:-$HOME/.config}
  store=${XDG_STATE_HOME:-$HOME/.local/state}
fi
store=$store/claude-config-backups

# Find the tool whose config dir holds the file. Resolve each dir, as it may itself be a junction.
# Windows paths are case-insensitive: compare and name backups lowercased.
tool=
for mod in "${CLAUDE_PROJECT_DIR:-.}"/*/go.mod; do
  t=$(basename "$(dirname "$mod")")
  td=$(realpath "$conf/$t" 2>/dev/null) || continue
  case "$(lower "$real")" in
    "$(lower "$td")"/*) tool=$t; break ;;
  esac
done
[ -n "$tool" ] || exit 0
n=$(printf '%s' "$td" | wc -c) # bytes, as cut -b counts; ${#td} counts characters
sub=$(printf '%s' "$real" | cut -b$((n + 2))-)

dest="$store/$tool/$(lower "$sub")"
mkdir -p "$(dirname "$dest")" || fail "could not create $(dirname "$dest")"
# Only "$dest.<stamp>", not the backups of a sibling such as "$dest.1".
backups() {
  ls -1d "$dest".* 2>/dev/null |
    p="$dest." awk 'index($0, ENVIRON["p"]) == 1 && substr($0, length(ENVIRON["p"]) + 1) ~ /^[0-9]{8}-[0-9]{6}Z(-[0-9]{3})?$/' |
    LC_ALL=C sort
}
newest=$(backups | tail -n 1)
if [ -n "$newest" ] && cmp -s "$real" "$newest"; then
  out=$newest # unchanged since the last backup, e.g. a retried edit
else
  stamp=$(date -u +%Y%m%d-%H%M%SZ)
  out="$dest.$stamp"
  i=0 # claim the name atomically, so parallel edits can't share it
  until (set -C; : > "$out") 2>/dev/null; do
    i=$((i + 1))
    [ "$i" -lt 1000 ] || fail "no free backup name for $dest.$stamp"
    out=$(printf '%s.%s-%03d' "$dest" "$stamp" "$i")
  done
  cp -p "$real" "$out" || { rm -f "$out"; fail "could not copy $real to $out"; }
fi

# Keep the oldest (the state before Claude first touched it) and the newest 20.
backups | sed '1d' | LC_ALL=C sort -r | tail -n +21 | while IFS= read -r old; do [ "$old" = "$out" ] || rm -f "$old"; done
[ -f "$out" ] || fail "backup $out disappeared while pruning"

shown=$out
[ -n "$win" ] && shown=$(cygpath -w "$out")
jq -n --arg m "Backed up $tool/$sub to $shown" '{systemMessage: $m}' || exit 0
