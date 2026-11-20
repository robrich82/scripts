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
    block:2) case "$err" in *'Use mise run build'*) ;; *) fail "$2 blocked for the wrong reason: $3 ($err)" ;; esac ;;
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
mise run build
mise run build mkv-audit
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

# block-openapi-read: refuses this repo's openapi.json in any case or separator, passes other files.
readcheck() { # readcheck <want rc> <file_path>
  jq -n --arg f "$2" '{tool_input: {file_path: $f}}' | sh "$dir/block-openapi-read.sh" 2>/dev/null
  rc=$?
  [ "$rc" = "$1" ] || fail "block-openapi-read want rc $1 got $rc for: $2"
}
root=$(cd "$dir/../.." && pwd)
readcheck 2 "$root/openapi.json"
readcheck 2 "$root/OpenAPI.JSON"
if command -v cygpath >/dev/null 2>&1; then readcheck 2 "$(cygpath -w "$root")\openapi.json"; fi
readcheck 0 "$root/openapi.json.bak"
readcheck 0 "$root/README.md"
printf 'not json' | sh "$dir/block-openapi-read.sh" 2>/dev/null || fail 'block-openapi-read refused unreadable input'

# settings.json wires each hook to its tool.
for pair in 'Bash|PowerShell block-go-build.sh' 'Edit|Write backup-config.sh' 'Read block-openapi-read.sh'; do
  jq -e --arg m "${pair% *}" --arg s "${pair#* }" '.hooks.PreToolUse[] | select(.matcher == $m) | .hooks[].command | contains($s)' \
    "$dir/../settings.json" >/dev/null || fail "settings.json does not run ${pair#* } on ${pair% *}"
  [ -f "$dir/${pair#* }" ] || fail "missing hook ${pair#* }"
done
tmp=$(mktemp -d)
readcheck 0 "$tmp/openapi.json"
rm -rf "$tmp"

# backup-config: copies a module's live config aside, via the real path or a junction.
tmp="$(mktemp -d)/sp ace é"
roam="$tmp/roam"; store="$tmp/local/claude-config-backups"
mkdir -p "$tmp/proj/mkv-movie" "$tmp/proj/configs" "$roam/mkv-movie/sub" "$roam/other"
touch "$tmp/proj/mkv-movie/go.mod"
printf 'v1' > "$roam/mkv-movie/sub/data.json"
printf 'x' > "$roam/other/data.json"
if command -v cygpath >/dev/null 2>&1; then
  MSYS_NO_PATHCONV=1 cmd /c mklink /J "$(cygpath -w "$tmp/proj/configs/mkv-movie")" "$(cygpath -w "$roam/mkv-movie")" >/dev/null
else
  ln -s "$roam/mkv-movie" "$tmp/proj/configs/mkv-movie"
fi
backup() { # backup <want rc> <file_path>
  out=$(jq -n --arg f "$2" '{tool_input: {file_path: $f}}' |
    APPDATA="$roam" LOCALAPPDATA="$tmp/local" CLAUDE_PROJECT_DIR="$tmp/proj" sh "$dir/backup-config.sh" 2>&1)
  rc=$?
  [ "$rc" = "$1" ] || fail "backup-config want rc $1 got $rc for $2: $out"
}
count() { ls -1d "$store/$1".* 2>/dev/null | awk -v p="$store/$1." 'index($0, p) == 1 && substr($0, length(p) + 1) ~ /^[0-9]{8}-[0-9]{6}Z(-[0-9]{3})?$/' | wc -l | tr -d ' '; }
backup 0 "$roam/mkv-movie/sub/data.json"
[ "$(count mkv-movie/sub/data.json)" = 1 ] || fail 'backup-config did not back up via the real path'
case "$out" in *systemMessage*) ;; *) fail "backup-config printed no systemMessage: $out" ;; esac
backup 0 "$tmp/proj/configs/mkv-movie/sub/data.json"
[ "$(count mkv-movie/sub/data.json)" = 1 ] || fail 'backup-config copied an unchanged file again'
printf 'v2' > "$roam/mkv-movie/sub/data.json"
backup 0 "$tmp/proj/configs/mkv-movie/sub/data.json"
[ "$(count mkv-movie/sub/data.json)" = 2 ] || fail 'backup-config did not back up via the junction (same second must not overwrite)'
[ "$(cat "$(ls -1 "$store/mkv-movie/sub/data.json".* | sort | head -1)")" = v1 ] || fail 'backup-config oldest copy has the wrong content'
if command -v cygpath >/dev/null 2>&1; then
  printf 'v3' > "$roam/mkv-movie/sub/data.json"
  backup 0 "$roam/MKV-MOVIE/SUB/DATA.JSON"
  [ "$(count mkv-movie/sub/data.json)" = 3 ] || fail 'backup-config split backups by path case'
fi
backup 0 "$roam/other/data.json"
[ -e "$store/other" ] && fail 'backup-config backed up a dir that is not a module'
backup 0 "$roam/mkv-movie/new.json"
[ "$(count mkv-movie/new.json)" = 0 ] || fail 'backup-config backed up a file that does not exist'
backup 0 "$tmp/proj/mkv-movie/go.mod"

# Pruning keeps the oldest and the newest 20, and leaves a sibling file's backups alone.
printf 'p' > "$roam/mkv-movie/p.json"
for i in $(seq -w 0 24); do printf 'old%s' "$i" > "$store/mkv-movie/p.json.20000101-0000${i}Z"; done
for i in 1 2 3; do printf 's' > "$store/mkv-movie/p.json.1.20000101-00000${i}Z"; done
backup 0 "$roam/mkv-movie/p.json"
[ "$(count mkv-movie/p.json)" = 21 ] || fail "backup-config kept $(count mkv-movie/p.json) backups, want 21"
[ -e "$store/mkv-movie/p.json.20000101-000000Z" ] || fail 'backup-config pruned the oldest backup'
[ -e "$store/mkv-movie/p.json.20000101-000001Z" ] && fail 'backup-config kept a backup it should prune'
[ "$(count mkv-movie/p.json.1)" = 3 ] || fail 'backup-config pruned a sibling file'"'"'s backups'

# A clock behind the newest backups must not prune the copy just made.
printf 'f' > "$roam/mkv-movie/f.json"
for i in $(seq -w 0 24); do printf 'future' > "$store/mkv-movie/f.json.20990101-0000${i}Z"; done
printf 'oldest' > "$store/mkv-movie/f.json.20000101-000000Z"
backup 0 "$roam/mkv-movie/f.json"
[ "$(grep -l '^f$' "$store/mkv-movie/f.json".* 2>/dev/null | wc -l)" = 1 ] || fail 'backup-config pruned the copy it just made'

# Same-stamp edits get a suffix instead of overwriting.
mkdir "$tmp/bin"
printf '#!/bin/sh\necho 20010101-000000Z\n' > "$tmp/bin/date"; chmod +x "$tmp/bin/date"
printf 'c1' > "$roam/mkv-movie/c.json"
( PATH="$tmp/bin:$PATH"; export PATH
  backup 0 "$roam/mkv-movie/c.json"; printf 'c2' > "$roam/mkv-movie/c.json"; backup 0 "$roam/mkv-movie/c.json" )
[ "$(cat "$store/mkv-movie/c.json.20010101-000000Z" 2>/dev/null)" = c1 ] &&
  [ "$(cat "$store/mkv-movie/c.json.20010101-000000Z-001" 2>/dev/null)" = c2 ] || fail 'backup-config overwrote a same-stamp backup'
rm "$tmp/bin/date"

# A Windows-style path is matched, and the message uses it.
if command -v cygpath >/dev/null 2>&1; then
  printf 'w' > "$roam/mkv-movie/w.json"
  backup 0 "$(cygpath -w "$roam/mkv-movie/w.json")"
  [ "$(count mkv-movie/w.json)" = 1 ] || fail 'backup-config ignored a C:\ path'
  case "$out" in *'"Backed up mkv-movie/w.json to C:\'*) ;; *) fail "backup-config message is not a Windows path: $out" ;; esac
fi

# A tool dir that is itself a junction is still recognised.
mkdir -p "$tmp/proj/mkv-tvshow" "$tmp/sync/mkv-tvshow"; touch "$tmp/proj/mkv-tvshow/go.mod"
printf 'j' > "$tmp/sync/mkv-tvshow/j.json"
if command -v cygpath >/dev/null 2>&1; then
  MSYS_NO_PATHCONV=1 cmd /c mklink /J "$(cygpath -w "$roam/mkv-tvshow")" "$(cygpath -w "$tmp/sync/mkv-tvshow")" >/dev/null
else
  ln -s "$tmp/sync/mkv-tvshow" "$roam/mkv-tvshow"
fi
backup 0 "$roam/mkv-tvshow/j.json"
[ "$(count mkv-tvshow/j.json)" = 1 ] || fail 'backup-config skipped a tool dir that is a junction'

# Parallel edits in one second each get their own backup.
printf '#!/bin/sh\necho 20020101-000000Z\n' > "$tmp/bin/date"; chmod +x "$tmp/bin/date"
for i in 1 2 3 4; do
  printf 'q%s' "$i" > "$roam/mkv-movie/q$i.json"; cp "$roam/mkv-movie/q$i.json" "$roam/mkv-movie/q.json"
  ( PATH="$tmp/bin:$PATH"; export PATH; backup 0 "$roam/mkv-movie/q.json" ) &
done
wait
rm "$tmp/bin/date"

# A failed copy or store refuses the edit.
printf '#!/bin/sh\nexit 1\n' > "$tmp/bin/cp"; chmod +x "$tmp/bin/cp"
printf 'k' > "$roam/mkv-movie/k.json"
( PATH="$tmp/bin:$PATH"; export PATH; backup 2 "$roam/mkv-movie/k.json" )
rm "$tmp/bin/cp"
printf 'x' > "$tmp/notadir"
out=$(jq -n --arg f "$roam/mkv-movie/k.json" '{tool_input: {file_path: $f}}' |
  APPDATA="$roam" LOCALAPPDATA="$tmp/notadir" CLAUDE_PROJECT_DIR="$tmp/proj" sh "$dir/backup-config.sh" 2>&1)
[ $? = 2 ] || fail "backup-config did not refuse when the store can't be created: $out"

# Unreadable input refuses the edit; a missing APPDATA falls back to the shell folder.
printf 'not json' | sh "$dir/backup-config.sh" 2>/dev/null
[ $? = 2 ] || fail 'backup-config did not refuse unreadable input'
if command -v cygpath >/dev/null 2>&1; then
  jq -n --arg f "$tmp/proj/mkv-movie/go.mod" '{tool_input: {file_path: $f}}' |
    env -u APPDATA -u LOCALAPPDATA CLAUDE_PROJECT_DIR="$tmp/proj" sh "$dir/backup-config.sh" >/dev/null 2>&1
  [ $? = 0 ] || fail 'backup-config refused a non-config edit with APPDATA unset'
fi
rm -rf "$(dirname "$tmp")"

[ "$fails" = 0 ] || { echo "$fails failed"; exit 1; }
echo PASS
