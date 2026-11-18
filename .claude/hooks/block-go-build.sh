#!/bin/sh
# PreToolUse: block direct go build; see CLAUDE.md.
# Anything but an explicit ok or block refuses, so a missing jq can't disable the check.
filter="$(dirname "$0")/block-go-build.jq"
[ -f "$filter" ] || { echo "block-go-build: $filter is missing; shell calls are refused until it is restored." >&2; exit 2; }
verdict=$(jq -jf "$filter")
case "$verdict" in
  ok) ;;
  block)
    echo 'Use mise run build (e.g. mise run build mkv-audit) instead of go build. See CLAUDE.md.' >&2
    exit 2
    ;;
  *)
    echo 'block-go-build: could not check the command. If jq is missing, install it and restart Claude Code from a shell that has it on PATH.' >&2
    exit 2
    ;;
esac
