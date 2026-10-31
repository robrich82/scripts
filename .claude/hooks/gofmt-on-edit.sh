#!/bin/sh
# PostToolUse: Write leaves CRLF on Windows, which make vet's gofmt -l rejects.
file=$(jq -r '.tool_input.file_path // empty' | tr -d '\r')
[ -n "$file" ] || { echo 'gofmt-on-edit: could not read file_path. Install jq, then restart Claude Code from a shell that has it on PATH.' >&2; exit 2; }
case "$file" in
  *.go) gofmt -w "$file" || exit 2 ;;
esac
