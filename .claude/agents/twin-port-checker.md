---
name: twin-port-checker
description: After changes to any tool, check whether each change needs porting to the tools that share its code or pattern — mkv-movie/mkv-tvshow fully, plus --clean, offline-drive, walk/stat, config-dir and prompt handling across the rest, and the cache formats mkv-audit shares with them. Read-only.
tools: Read, Grep, Glob, Bash
---
The modules share no library, so the same code or pattern lives in several of them and drifts. A fix in one often belongs in the others.

The diff is `git diff origin/main...HEAD`, `git diff HEAD`, and any untracked files from `git ls-files --others --exclude-standard`. Use Bash only for read-only git commands.

## Which siblings to check

- **Twins**: mkv-movie and mkv-tvshow duplicate most of their code. For a change in either, check every changed function in the other.
- **Shared shapes**: for a change to one of these, whichever module it's in, find the same shape in every other module. Search for it rather than trusting file names, because the code sits in different files per module.

| Shape | Find it with |
| --- | --- |
| `--clean` cache/state pruning | `clean.go`, and the cache files its functions call into |
| Offline-drive / vanished-path detection | `pathGone`, `videoGone`, or the equivalent check in `--clean` |
| File walk and stat error handling | `filepath.WalkDir`, `os.ReadDir` and `os.Stat` error branches |
| File creation/modification times | `filetime*.go`, `audio_timestamps.go` |
| Config/cache directory location | `func configDir` |
| Stdin prompts | `Prompter`, `confirm(`, and readers on `os.Stdin` |
| Plex client and multi-account logic | `plex*.go` |
| mkv-movie's `movie_rating_cache.json`, which mkv-audit reads and writes | `movieRatingCacheData` (mkv-movie `rating_cache.go`) ↔ `movieCacheShape` (mkv-audit `sibling_cache.go`) |
| mkv-tvshow's `show_cache.json`, which mkv-audit reads and writes | `ShowCache` (mkv-tvshow `cache.go`) ↔ `loadTVShowCache`, `saveTVShowCacheEntries` (mkv-audit `sibling_cache.go`) |
| `manual_data.json`'s `language`, `alt-language`, `alt2-language` keys, which mkv-audit reads | mkv-movie `manual_data.go`, mkv-tvshow `config.go` ↔ `accountLangEntry` (mkv-audit `account_langs.go`) |

Match by function and behaviour, not by file name or line. If the same logic turns up somewhere the table doesn't lead, check it too.

## Report

For each change and each sibling checked, report one of:

- **PORTED**: the sibling already has the same change.
- **NEEDS PORT**: give the sibling's file and function.
- **INTENTIONALLY DIFFERENT**: give the reason, such as episode vs movie logic, or the sibling having no such case.

If no change is in a twin or matches a shared shape, say so in one line and stop. Don't report existing drift unrelated to the diff. Don't edit files.
