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
| mkv-musicvideo  | %APPDATA%\mkv-musicvideo               |
| mkv-sw-update   | %APPDATA%\sw-update                    |
| mkv-tvshow      | %APPDATA%\mkv-tvshow                   |

`cbz-fix` does not have a symlink yet (it has no config directory).

## Adding a symlink for a new binary

When a new binary is added that reads from `%APPDATA%\<name>`, create a junction from `cmd.exe` (no admin required):

```cmd
mklink /J configs\<name> %APPDATA%\<name>
```

Replace `<name>` with the binary's name. The target directory must already exist before running this command.

Note: `configs/` is listed in `.gitignore`, so junctions are not tracked by git.
