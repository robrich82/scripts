---
name: new-tool
description: Scaffold a new tool module in this repo and register it everywhere a tool is listed — README, CLAUDE.md and the configs/ junction. Run as /new-tool <name> <one-line purpose>.
disable-model-invocation: true
argument-hint: <name> <one-line purpose>
---

Add a new tool named `$0`. Everything after the name is its one-line purpose. If either is missing, ask for it before starting.

The Makefile and CI find modules by their `go.mod`, so none of them need editing. Everything below does.

## 1. Module

- Create `$0/go.mod` with `module $0` and the same `go` line as the other modules. Copy the line from `cbz-fix/go.mod`; don't type a version.
- Write `$0/main.go` in `package main`, following CLAUDE.md's *Common shape of each tool*. That means flags parsed in `main.go`, one-shot modes that return early, and every side effect behind an interface so tests can substitute fakes. If the tool keeps config, cache or state, put it in the app dir the way `mkv-movie/appdir.go` does.
- Add tests in the same piece of work, using `t.TempDir()` for the filesystem. Read an existing module's tests for the patterns first.

## 2. Registration

- **README.md**:
  - Add a row to the *Tools* table, in alphabetical order, linking `#$0`.
  - Add a `## $0` section in alphabetical position, before *Project Structure*. Match the other sections: what it does, a *Usage* block, a *Flags* table, and config files if it has any.
  - Add `make $0` to the list in *Building*.
  - Add the directory to the *Project Structure* tree.
  - List any new external command or env var under *Requirements*.
- **CLAUDE.md**: update the tool count in *Architecture* ("Six independent tools"), and add any new env var to the *Environment* line.
- **configs/ junction**: only if the tool reads `%APPDATA%\$0`. Follow the `configs-symlink` skill, and add the row to its table.

## 3. Finish

Run `/finish`. Its build and test steps pick the new module up from its directory name.
