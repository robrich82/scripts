.paths | to_entries[] | .key as $route | .value | to_entries[]
| select(.key | IN("get", "post", "put", "patch", "delete", "head", "options"))
| .key as $method | .value as $op
| select(([$route, $op.summary, $op.operationId] + ($op.tags // []) | map(. // "") | join(" ")) | test($re; "i"))
| "\($method | ascii_upcase) \($route) — \($op.summary // "")"
