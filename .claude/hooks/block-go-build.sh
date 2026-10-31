#!/bin/sh
# PreToolUse: CLAUDE.md requires make; direct go build skips the dist/ layout.
# Fails closed, so a missing jq can't silently disable the check.
cmd=$(jq -r '.tool_input.command // empty' | tr -d '\r') || cmd=
[ -n "$cmd" ] || { echo 'block-go-build: could not read the command (is jq installed?)' >&2; exit 2; }
if printf '%s\n' "$cmd" | grep -qE '(^|[;&|(])[[:space:]]*go[[:space:]]+build([[:space:]]|$)'; then
  echo 'Use make (e.g. make mkv-audit) instead of go build. See CLAUDE.md.' >&2
  exit 2
fi
