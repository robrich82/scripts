# Prints "block" or "ok", or nothing when there is no command to check.
# Quoted text is removed first so commit messages and PR bodies can mention go build.
# Heredocs go first because their bodies can hold unbalanced quotes; text after the opener stays.
def strip_quoted:
  gsub("<<-?[ \\t]*[\"']?(?<tag>\\w+)[\"']?(?<rest>[^\\n]*)\\n[\\s\\S]*?\\n[ \\t]*\\k<tag>(?=\\s|$)"; "\(.rest)\n")
  | gsub("@(?<q>[\"'])\\r?\\n[\\s\\S]*?\\n\\k<q>@"; "")
  | gsub("\"(?:[^\"\\\\]|\\\\[\\s\\S])*\"|'[^']*'"; "");

.tool_input.command // empty
| strip_quoted
| if test("(?:^|[\\n\\r;&|(`{])\\s*(?:env\\s+)?(?:\\w+=\\S*\\s+)*go(?:\\.exe)?\\s+(?:-C[=\\s]\\s*\\S+\\s+)?build(?:[\\s;&|)}`]|$)"; "i")
  then "block" else "ok" end
