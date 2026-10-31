#!/bin/sh
# PostToolUse: Write leaves CRLF on Windows, which make vet's gofmt -l rejects.
file=$(jq -r '.tool_input.file_path // empty' | tr -d '\r')
case "$file" in
  *.go) gofmt -w "$file" >/dev/null || exit 2 ;;
esac
