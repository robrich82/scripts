#!/bin/sh
# Usage, from the repo root: sh .claude/hooks/test.sh
dir="$(dirname "$0")"
fails=0
fail() { printf 'FAIL %s\n' "$1"; fails=$((fails + 1)); }

check() { # check <block|ok> <Bash|PowerShell> <command>
  err=$(jq -n --arg t "$2" --arg c "$3" '{tool_name: $t, tool_input: {command: $c}}' | sh "$dir/block-go-build.sh" 2>&1)
  rc=$?
  case "$1:$rc" in
    ok:0) ;;
    block:2) case "$err" in *'Use make'*) ;; *) fail "$2 blocked for the wrong reason: $3 ($err)" ;; esac ;;
    *) fail "$2 want $1 got rc $rc: $3" ;;
  esac
}

while IFS= read -r c; do check block Bash "$c"; done <<'BLOCKED'
go build
go build ./...
cd x && go build
go build; echo done
go build&&echo x
go build|tee log
go build>log
echo x | go build
(go build)
$(go build)
`go build`
{ go build; }
GOOS=linux go build
env GOOS=linux go build
env -i go build
A=1 env B=2 go build
go.exe build
GO build
go -C mkv-audit build
go -C=mkv-audit build
go -C "my dir" build
go -C 'x' build
go -v build
for m in a b; do go build; done
if true; then go build; fi
! go build
time go build ./...
xargs go build
/usr/local/go/bin/go build
C:/Go/bin/go.exe build
out="$(go build 2>&1)"
echo "$(cd a && go build)"
echo "`go build`"
echo it\'s; go build; echo it\'s
echo "fix"; go build
BLOCKED

while IFS= read -r c; do check ok Bash "$c"; done <<'ALLOWED'
make all
go test ./...
go vet ./...
go test -run build ./...
go help build
go builder
gobuild
mygo build
cargo build
git commit -m "a; go build"
git commit -m 'a; go build b'
gh pr create --body "a; go build b"
echo "fix; go build ok"
ls # ; go build
echo ""
"quoted"
ALLOWED

# Multi-line Bash cases.
check block Bash 'x
go build'
check block Bash "$(printf 'x\rgo build')"
check block Bash 'go \
  build'
check block Bash "# it's the build
go build
# what's next"
check block Bash 'cat <<EOF && go build
body
EOF'
check block Bash 'cat <<A
x
A
go build
cat <<B
y
B'
check block Bash 'cat <<EOF
$(go build)
EOF'
check block Bash 'cat <<EOF
EOF
go build
cat <<EOF
x
EOF'
check block Bash 'cat <<< word
go build
word'
check ok Bash 'git commit -m "first
go build
last"'
check ok Bash 'cat <<EOF
  go build
EOF'
check ok Bash "git commit -F - <<'EOF'
it's unbalanced
; go build
EOF"
check ok Bash "git commit -F - <<'END-MSG'
go build
END-MSG"
check ok Bash 'cat <<\EOF
go build
EOF'
check ok Bash 'cat <<A <<B
a
A
go build
B'
check ok Bash "gh pr create --body \"\$(cat <<'EOF'
say \"hi
; go build
EOF
)\""

# PowerShell quoting differs: backslash is literal, backtick escapes.
check block PowerShell 'go build'
check block PowerShell "\$env:GOOS='linux'; go build"
check block PowerShell 'cd "C:\Users\robri\code\"; go build; echo "done"'
check block PowerShell '$o = "$(go build ./...)"'
check block PowerShell 'go `
  build'
check block PowerShell "\$a = @'
x
'@
go build
\$b = @'
y
'@"
check block PowerShell "\$x = @\"
\$(go build)
\"@"
check ok PowerShell "git commit -m @'
it's a here-string
go build
'@"
check ok PowerShell "$(printf "git commit -m @'\r\nit's CRLF\r\ngo build\r\n'@")"
check ok PowerShell 'git commit -m "say `"hi; go build"'
check ok PowerShell 'git commit -m "say ""hi; go build"'
check ok PowerShell '<#
go build
#>'
check ok PowerShell "echo 'it''s; go build'"

# Anything unreadable must refuse, not pass.
for input in 'not json' '{}' '{"tool_input":{"command":null}}'; do
  printf '%s' "$input" | sh "$dir/block-go-build.sh" 2>/dev/null
  rc=$?
  [ "$rc" = 2 ] || fail "want rc 2 got $rc for input: $input"
done

# gofmt-on-edit: rewrites .go files to LF, leaves others alone, refuses bad input.
tmp=$(mktemp -d)
printf 'package a\r\nfunc  X() {}\r\n' > "$tmp/a b.go"
printf 'x\r\n' > "$tmp/a.txt"
for f in "$tmp/a b.go" "$tmp/a.txt"; do
  jq -n --arg f "$f" '{tool_input: {file_path: $f}}' | sh "$dir/gofmt-on-edit.sh" || fail "gofmt-on-edit rc $? for $f"
done
[ "$(cat "$tmp/a b.go")" = "$(printf 'package a\n\nfunc X() {}')" ] || fail 'gofmt-on-edit did not format a .go file'
[ "$(od -An -c "$tmp/a.txt" | tr -d ' \n')" = 'x\r\n' ] || fail 'gofmt-on-edit changed a non-Go file'
printf 'package\n' > "$tmp/bad.go"
for input in '{}' "$(jq -n --arg f "$tmp/bad.go" '{tool_input: {file_path: $f}}')"; do
  printf '%s' "$input" | sh "$dir/gofmt-on-edit.sh" 2>/dev/null
  rc=$?
  [ "$rc" = 2 ] || fail "gofmt-on-edit want rc 2 got $rc for: $input"
done
rm -rf "$tmp"

# block-openapi-read: refuses openapi.json by either separator, passes everything else.
readcheck() { # readcheck <want rc> <file_path>
  jq -n --arg f "$2" '{tool_input: {file_path: $f}}' | sh "$dir/block-openapi-read.sh" 2>/dev/null
  rc=$?
  [ "$rc" = "$1" ] || fail "block-openapi-read want rc $1 got $rc for: $2"
}
readcheck 2 'C:\Users\x\scripts\openapi.json'
readcheck 2 '/c/Users/x/scripts/openapi.json'
readcheck 0 'C:\Users\x\scripts\openapi.json.bak'
readcheck 0 'C:\Users\x\scripts\my-openapi.json'
readcheck 0 'C:\Users\x\scripts\README.md'

# backup-config: copies a module's live config aside, via the real path or a junction.
tmp=$(mktemp -d)
mkdir -p "$tmp/proj/mkv-movie" "$tmp/proj/configs" "$tmp/roam/mkv-movie/sub" "$tmp/roam/other"
touch "$tmp/proj/mkv-movie/go.mod"
printf 'v1' > "$tmp/roam/mkv-movie/sub/data.json"
printf 'x' > "$tmp/roam/other/data.json"
if command -v cygpath >/dev/null 2>&1; then
  MSYS_NO_PATHCONV=1 cmd /c mklink /J "$(cygpath -w "$tmp/proj/configs/mkv-movie")" "$(cygpath -w "$tmp/roam/mkv-movie")" >/dev/null
else
  ln -s "$tmp/roam/mkv-movie" "$tmp/proj/configs/mkv-movie"
fi
backup() { # backup <want rc> <file_path>
  out=$(jq -n --arg f "$2" '{tool_input: {file_path: $f}}' |
    APPDATA="$tmp/roam" LOCALAPPDATA="$tmp/local" CLAUDE_PROJECT_DIR="$tmp/proj" sh "$dir/backup-config.sh" 2>&1)
  rc=$?
  [ "$rc" = "$1" ] || fail "backup-config want rc $1 got $rc for $2: $out"
}
count() { ls -1 "$tmp/local/claude-config-backups/$1".[0-9]* 2>/dev/null | wc -l | tr -d ' '; }
backup 0 "$tmp/roam/mkv-movie/sub/data.json"
[ "$(count mkv-movie/sub/data.json)" = 1 ] || fail 'backup-config did not back up via the real path'
backup 0 "$tmp/proj/configs/mkv-movie/sub/data.json"
[ "$(count mkv-movie/sub/data.json)" = 2 ] || fail 'backup-config did not back up via the junction (same second must not overwrite)'
[ "$(cat "$(ls -1 "$tmp/local/claude-config-backups/mkv-movie/sub/data.json".[0-9]* | head -1)")" = v1 ] || fail 'backup-config copy has the wrong content'
backup 0 "$tmp/roam/other/data.json"
[ -e "$tmp/local/claude-config-backups/other" ] && fail 'backup-config backed up a dir that is not a module'
backup 0 "$tmp/roam/mkv-movie/new.json"
[ -e "$tmp/local/claude-config-backups/mkv-movie/new.json".[0-9]* ] 2>/dev/null && fail 'backup-config backed up a file that does not exist'
backup 0 "$tmp/proj/mkv-movie/go.mod"
i=0
while [ "$i" -lt 25 ]; do printf 'old' > "$tmp/local/claude-config-backups/mkv-movie/sub/data.json.20000101-0000$(printf '%02d' "$i")"; i=$((i + 1)); done
backup 0 "$tmp/roam/mkv-movie/sub/data.json"
[ "$(count mkv-movie/sub/data.json)" = 20 ] || fail "backup-config kept $(count mkv-movie/sub/data.json) backups, want 20"
[ -e "$tmp/local/claude-config-backups/mkv-movie/sub/data.json.20000101-000000" ] && fail 'backup-config pruned the newest instead of the oldest'
for input in 'not json'; do
  printf '%s' "$input" | sh "$dir/backup-config.sh" 2>/dev/null
  rc=$?
  [ "$rc" = 2 ] || fail "backup-config want rc 2 got $rc for input: $input"
done
rm -rf "$tmp"

[ "$fails" = 0 ] || { echo "$fails failed"; exit 1; }
echo PASS
