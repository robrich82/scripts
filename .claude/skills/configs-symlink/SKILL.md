---
name: configs-symlink
description: Explains the configs/ directory symlink pattern in this repo and guides creating a new symlink when a binary is added. Use this skill whenever the user asks about config files, the configs/ directory, where configs are stored, or adds a new binary that reads from AppData/Roaming.
---

Each binary that reads config files stores them in `%APPDATA%\<binary-name>` at runtime.
The `configs/` directory at the repo root contains Windows directory symlinks pointing to those locations, so config files are accessible alongside the source code.

## Current symlinks

| configs/ entry  | Points to                              |
|-----------------|----------------------------------------|
| mkv-audit       | %APPDATA%\mkv-audit                    |
| mkv-movie       | %APPDATA%\mkv-movie                    |
| mkv-music       | %APPDATA%\mkv-music                    |
| sw-update       | %APPDATA%\sw-update                    |
| mkv-tvshow      | %APPDATA%\mkv-tvshow                   |

`cbz-fix` and `sw-update` do not have symlinks (neither uses a config directory).

## Adding a symlink for a new binary

When a new binary is added that reads from `%APPDATA%\<name>`, create a junction from `cmd.exe` (no admin required). Run this from inside the `configs/` directory:

```cmd
mklink /J <name> %APPDATA%\<name>
```

The target directory must already exist before running this command.

Note: `%APPDATA%` does not expand when `cmd.exe` is invoked from bash (e.g. `cmd.exe /c "mklink ..."`). Run this directly in a native `cmd.exe` prompt, or first resolve the path with `cmd.exe /c "echo %APPDATA%"` and substitute it in.

Note: `configs/` is listed in `.gitignore`, so junctions are not tracked by git.
