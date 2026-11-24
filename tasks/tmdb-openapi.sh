set -eu
url=https://developer.themoviedb.org/openapi/tmdb-api.json
out="tmdb-openapi.json.$$.tmp" # per process, so parallel runs don't share it
# A failed download leaves jq no input, which -e turns into an error.
if ! curl -fsSL --proto =https --proto-redir =https "$url" |
  jq -e 'del(.. | .example?, .examples?) | select(.openapi and (.paths | length) > 0)' > "$out"; then
  rm -f "$out"; echo "tmdb-openapi: no spec at $url" >&2; exit 1
fi
mv -f "$out" tmdb-openapi.json # one-step rename, so a failed move keeps the old spec
