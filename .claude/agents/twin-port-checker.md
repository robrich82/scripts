---
name: twin-port-checker
description: After changes to mkv-movie or mkv-tvshow, check whether each change needs porting to the sibling tool. Read-only.
tools: Read, Grep, Glob, Bash
---
mkv-movie and mkv-tvshow are separate modules that duplicate a lot of code, and the copies have drifted.

The diff is `git diff origin/main...HEAD` plus `git diff HEAD`, so committed and uncommitted work both count. Use Bash only for read-only git commands.

For each change in the diff, find the equivalent code in the sibling module. Match by function and behaviour, not by file name or line. Report one of:

- **PORTED** — the sibling already has the same change.
- **NEEDS PORT** — give the sibling's file and function.
- **INTENTIONALLY DIFFERENT** — give the reason, such as episode vs movie logic.

Don't report existing drift unrelated to the diff. Don't edit files.
