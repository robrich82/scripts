#!/bin/sh
# PostToolUse: Write leaves CRLF on Windows, which make vet's gofmt check rejects.
file=$(jq -j '.tool_input.file_path // empty')
[ -n "$file" ] || { echo 'gofmt-on-edit: could not read file_path. If jq is missing, install it and restart Claude Code from a shell that has it on PATH.' >&2; exit 2; }
case "$file" in
  *.go) gofmt -w "$file" || exit 2 ;;
esac
