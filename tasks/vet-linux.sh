set -eu
export GOOS=linux
if [ -n "${usage_module:-}" ]; then mods=$usage_module
else mods=$(for d in */; do if [ -f "${d}go.mod" ]; then printf '%s\n' "${d%/}"; fi; done); fi
[ -n "$mods" ] || { echo 'no modules found' >&2; exit 1; }
set -f
for m in $mods; do
  m=$(printf '%s' "$m" | sed "s#^[./\']*##; s#[./\']*\$##"); [ -n "$m" ] || { echo 'empty module name' >&2; exit 1; }
  echo "# $m"
  (cd "$m" && go vet ./... && golangci-lint run ./...)
done
