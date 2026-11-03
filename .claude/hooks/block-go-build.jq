# Prints "block" or "ok", or nothing when there is no command to check.
# Pure data (single quotes, quoted-tag heredocs, comments) is dropped. Double quotes and
# unquoted heredocs keep only their $(...) and `...`, which still run. Then go build is matched anywhere.

def subs: [scan("\\$\\((?:[^()]|\\([^()]*\\))*\\)|`[^`\\n]*`")] | join(" ; ");

# One heredoc per pass, so a second opener on the same line is still found.
def heredocs:
  sub("(?<!<)<<(?!<)-?[ \\t]*(?<q>[\"'\\\\]?)(?<tag>[A-Za-z_][\\w.-]*)[\"']?(?<rest>[^\\n]*)\\n(?<body>(?:[\\s\\S]*?\\n)??)[ \\t]*\\k<tag>(?=\\s|$)";
      "\(.rest)\n" + (if .q == "" then .body | subs else "" end)) as $s
  | if $s == . then . else $s | heredocs end;

# Bash: backslash escapes, # comments after whitespace or a separator.
def bash_tokens:
  gsub("'[^']*'(?<sq>)|\"(?<dq>(?:[^\"\\\\]|\\\\[\\s\\S])*)\"|(?<c>^|[\\s;&|(])#[^\\n]*|\\\\(?<e>[\\s\\S])";
       if .dq != null then "Q" + (.dq | subs)
       elif .e != null then (if .e == "\n" then " " else "Q" end)
       elif .c != null then .c
       else "Q" end);

# PowerShell: backtick escapes, doubled quotes, here-strings and <# #> comments.
def pwsh_tokens:
  gsub("@'[ \\t]*\\r?\\n(?:[\\s\\S]*?\\n)??'@(?<sq>)|@\"[ \\t]*\\r?\\n(?<hs>(?:[\\s\\S]*?\\n)??)\"@|'(?:[^']|'')*'(?<sq2>)|\"(?<dq>(?:[^\"`]|`[\\s\\S]|\"\")*)\"|<#[\\s\\S]*?#>(?<bc>)|(?<c>^|[\\s;&|(])#[^\\n]*|`(?<e>[\\s\\S])";
       if .dq != null then "Q" + (.dq | subs)
       elif .hs != null then "Q" + (.hs | subs)
       elif .e != null then (if .e == "\n" or .e == "\r" then " " else "Q" end)
       elif .c != null then .c
       elif .bc != null then ""
       else "Q" end);

.tool_name as $tool
| .tool_input.command // empty
| if $tool == "PowerShell" then pwsh_tokens else heredocs | bash_tokens end
| if test("(?<!\\w)(?<![.-])go(?:\\.exe)?[ \\t]+(?:-[^\\s=]+(?:=\\S*)?[ \\t]+(?:[^-\\s]\\S*[ \\t]+)?)*build(?![\\w.-])"; "i")
  then "block" else "ok" end
