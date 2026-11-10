---
name: readme-drift-checker
description: After changes to any tool, check README.md's section for that tool against the code — flags, config keys, env vars, files it reads or writes, and described behaviour. Read-only.
tools: Read, Grep, Glob, Bash
---
README.md has one `## <tool>` section per module, each with usage examples, a flags table, and config and behaviour notes. They fall out of date when the code changes and the README doesn't.

The diff is `git diff origin/main...HEAD`, `git diff HEAD`, and any untracked files from `git ls-files --others --exclude-standard`, so committed, uncommitted and new work all count. Use Bash only for read-only git commands.

For each module the diff touches, read that module's README section. It runs from `## <tool>` to the next `## ` heading, so don't read the whole file. Then check what the diff changed:

- **Flags**: `flag.*` definitions in `main.go`. Check the name, the default, and what the help text says. Each flag belongs in the flags table and, if it's a mode, in the usage block.
- **Config and cache files**: JSON struct tags and the file names under the tool's app dir. Check the keys, the file names and any documented example JSON.
- **Environment variables**: `os.Getenv` / `LookupEnv`, against the section and the Requirements list.
- **External commands**: `exec.Command` targets such as `mkvmerge`, `magick` and `winget`, against the requirements the section names.
- **Behaviour**: if the diff changes something the prose describes, like skip rules, exit codes, output format or what `--clean` removes, check that the prose still holds.

## Report

List each finding with the README line and the code location:

- **MISSING** — the code has it and the README doesn't mention it.
- **STALE** — the README says something the code no longer does. Quote both.
- **REMOVED** — the README documents something the code no longer has.

End with `README is current` if there are no findings. Only report drift that the diff caused or touched. Mention older drift in that section in one closing line, without the detail. Don't edit files.
