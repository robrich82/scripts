#!/bin/sh
# PreToolUse: CLAUDE.md requires make; direct go build skips the dist/ layout.
# Refuses when jq fails, so a missing jq can't silently disable the check.
verdict=$(jq -rf "$(dirname "$0")/block-go-build.jq" | tr -d '\r')
case "$verdict" in
  ok) ;;
  block)
    echo 'Use make (e.g. make mkv-audit) instead of go build. See CLAUDE.md.' >&2
    exit 2
    ;;
  *)
    echo 'block-go-build: could not read the command. Install jq, then restart Claude Code from a shell that has it on PATH.' >&2
    exit 2
    ;;
esac
