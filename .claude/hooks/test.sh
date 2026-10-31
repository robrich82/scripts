#!/bin/sh
# Run from anywhere: sh .claude/hooks/test.sh
hook="$(dirname "$0")/block-go-build.sh"
fails=0

check() { # check <expected rc> <command>
  jq -n --arg c "$2" '{tool_input: {command: $c}}' | sh "$hook" 2>/dev/null
  rc=$?
  [ "$rc" = "$1" ] || { printf 'FAIL want %s got %s: %s\n' "$1" "$rc" "$2"; fails=$((fails + 1)); }
}

while IFS= read -r c; do check 2 "$c"; done <<'BLOCKED'
go build
go build ./...
cd x && go build
go build; echo done
go build&&echo x
go build|tee log
(go build)
$(go build)
`go build`
{ go build; }
GOOS=linux go build
env GOOS=linux go build
A=1 B=2 go build
go.exe build
GO build
go -C mkv-audit build
$env:GOOS='linux'; go build
echo "fix"; go build
BLOCKED

while IFS= read -r c; do check 0 "$c"; done <<'ALLOWED'
make all
go test ./...
go vet ./...
go builder
mygo build
git commit -m "a; go build"
git commit -m 'a; go build'
gh pr create --body "a; go build b"
echo "fix; go build ok"
echo ""
"quoted"
ALLOWED

check 2 'x
go build'
check 2 "$(printf 'x\rgo build')"
check 2 'cat <<EOF && go build
body
EOF'
check 0 'git commit -m "first
go build
last"'
check 0 'cat <<EOF
  go build
EOF'
check 0 "git commit -F - <<'EOF'
it's unbalanced
; go build
EOF"
check 0 "gh pr create --body \"\$(cat <<'EOF'
say \"hi
; go build
EOF
)\""
check 0 "git commit -m @'
it's a here-string
go build
'@"

# Anything unreadable must refuse, not pass.
for input in 'not json' '{}'; do
  printf '%s' "$input" | sh "$hook" 2>/dev/null
  rc=$?
  [ "$rc" = 2 ] || { printf 'FAIL want 2 got %s for input: %s\n' "$rc" "$input"; fails=$((fails + 1)); }
done

[ "$fails" = 0 ] && echo PASS
exit "$fails"
