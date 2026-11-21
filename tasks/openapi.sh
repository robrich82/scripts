set -eu
if ! curl -fsSL https://developer.plex.tv/pms/ |
  sed -nE 's/^[[:space:]]*const __redoc_state = (.*);[[:space:]]*$/\1/p' |
  jq -en 'input | .spec.data | select(.openapi and (.paths | length) > 0)' > openapi.json.tmp; then
  rm -f openapi.json.tmp; echo 'openapi: no spec found on developer.plex.tv/pms/' >&2; exit 1
fi
mv -f openapi.json.tmp openapi.json
