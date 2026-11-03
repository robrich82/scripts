def refs: [.. | objects | ."$ref" | strings];
def resolve($spec): ltrimstr("#/") | split("/") as $keys | $spec | getpath($keys);

# Walks references transitively; $seen stops cycles between schemas.
def closure($spec; $todo; $seen):
  if ($todo | length) == 0 then $seen
  elif $seen | has($todo[0]) then closure($spec; $todo[1:]; $seen)
  else
    ($todo[0] | resolve($spec)) as $node
    | closure($spec; $todo[1:] + ($node | refs); $seen + {($todo[0]): $node})
  end;

. as $spec
| ("/" + ($route | ltrimstr("/"))) as $path
| ($spec.paths[$path] // error("No such path: \($path)")) as $item
| {($path): $item, refs: closure($spec; $item | refs; {})}
