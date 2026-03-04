# Scripts

A collection of Go command-line utilities for managing MKV media files and software updates.

## Tools

| Tool | Purpose |
|------|---------|
| [mkv-audit](#mkv-audit) | Normalize MKV track language codes and default/forced flags |
| [mkv-movie](#mkv-movie) | Sync MKV date headers and file timestamps from mediainfo |
| [mkv-musicvideo](#mkv-musicvideo) | Fuzzy-match video files to audio files for renaming |
| [mkv-tvshow](#mkv-tvshow) | Tag TV episode MKVs with metadata from TMDB |
| [sw-update](#sw-update) | Track and install software updates from GitHub releases or web pages |

## Requirements

- Go 1.22+
- [MKVToolNix](https://mkvtoolnix.download/) (`mkvmerge`, `mkvpropedit`) — required by mkv-audit, mkv-movie, mkv-tvshow
- [MediaInfo](https://mediaarea.net/en/MediaInfo) — required by mkv-movie
- TMDB API key — required by mkv-tvshow

## Building

Build all tools to the `dist/` directory:

```sh
make all
```

Build a specific tool:

```sh
make mkv-audit
make mkv-movie
make mkv-musicvideo
make mkv-tvshow
make sw-update
```

Run all tests:

```sh
make test
```

---

## mkv-audit

Normalizes MKV/MKA track metadata in bulk:

- Language codes to ISO 639-2/B (e.g. `fre`, `jpn`, `kor`)
- IETF/BCP-47 language tags (e.g. `en-US`, `ja-JP`)
- Default and forced flags based on language and codec
- Track ordering by codec quality (e.g. TrueHD before AC3)

### Usage

Run from the directory containing your MKV files:

```sh
mkv-audit
```

The tool scans recursively and prints a summary:

```
Scanned: 120  Fixed: 14  Failed: 0  LangFixes: 9  FlagFixes: 8
```

### Dependencies

- `mkvmerge` — reads track metadata
- `mkvpropedit` — applies edits

### Configuration

#### `mkv-audit.json` (optional)

Place in the scan root to override IETF language tags per directory:

```json
{
  "overrides": [
    {
      "path": "Agatha Christie's Poirot (1989)",
      "ietf": { "eng": "en-GB" }
    }
  ]
}
```

Track names containing region keywords (e.g. `British`, `Australian`) also
trigger automatic IETF overrides.

#### Auto-generated files

- `failed_files.json` — files that could not be analyzed (skipped on re-runs)
- `unknown_codecs.json` — codecs not present in the priority map

---

## mkv-movie

Syncs each MKV file's filesystem timestamps and internal header date to the
date recorded in its own metadata tags.

**Date priority:** `DATE` tag → `YEAR` tag → `Encoded_Date` header

### Usage

Run from the directory containing your MKV files:

```sh
mkv-movie
```

Output:

```
Updated: 8  OK: 42  Skipped: 3  Errors: 0
```

### Dependencies

- `mediainfo` — reads date tags (required)
- `mkvpropedit` — updates internal header date (optional; a warning is shown if missing)

> **Note:** Filesystem timestamp modification is Windows-only. On other
> platforms the internal header is still updated if `mkvpropedit` is available.

---

## mkv-musicvideo

Matches video files to their corresponding audio files using fuzzy filename
similarity (LCS-based), then renames them to a consistent scheme.

Supported video extensions: `.mkv`, `.mp4`, `.avi`, `.mov`, `.vob`
Supported audio extensions: `.flac`, `.mp3`, `.m4a`, `.wav`, `.ogg`, `.wma`, `.mka`, `.dsf`

### Usage

Run from the directory containing your files:

```sh
mkv-musicvideo
```

The tool prompts for manual confirmation when a match is ambiguous.

### Configuration

#### `ignore_patterns.txt` (optional)

List substrings to strip from filenames before matching, one per line:

```
2023
1080p
Remastered
```

---

## mkv-tvshow

Tags TV episode MKV files with metadata fetched from
[The Movie Database (TMDB)](https://www.themoviedb.org/).

Episode files must contain a season/episode pattern in their name, e.g.
`Show Name S01E03.mkv`.

### Usage

Set your TMDB API key, then run from your TV library root:

```sh
export TMDB_API_KEY=your_api_key_here
mkv-tvshow
```

On first run for each show, the tool searches TMDB and prompts you to confirm
the match. The confirmed ID is saved to `show_cache.json` for future runs.

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-cutoff-date YYYY-MM-DD` | today | Only process files modified on or after this date |
| `-all` | false | Process all cached shows without prompting |

#### Output

```
Success: 12  Failed: 0  SkippedOld: 5
```

### Dependencies

- TMDB API (requires `TMDB_API_KEY` environment variable)
- `mkvpropedit` — applies metadata edits

### Configuration

#### `show_cache.json` (auto-generated)

Maps folder names to TMDB show IDs. Edit manually to correct a wrong match.

```json
{
  "Breaking Bad": "1396",
  "The Wire": "1438"
}
```

#### `manual_data.json` (optional)

Override or remap episode data for shows where TMDB numbering differs from
your files:

```json
{
  "Show Name": {
    "overrides": {
      "S02": { "id": "246862", "map_season": 1 }
    },
    "mapping": {
      "S01E03": [1, 3],
      "S01E05": [1, 5]
    }
  }
}
```

- `overrides` — redirect a local season to a different TMDB show/season
- `mapping` — remap a local `S##E##` to a different TMDB `[season, episode]`

See [mkv-tvshow/manual_data.json.example](mkv-tvshow/manual_data.json.example) for a full example.

---

## sw-update

Tracks installed software versions and checks for updates via GitHub releases,
SourceForge file releases, or web page scraping. Downloads and runs installers on demand.

### Usage

```sh
sw-update check              # Check all enabled apps for updates
sw-update check <name>       # Check a specific app
sw-update list               # List all apps and their known versions
sw-update install <name>     # Download latest release and run/open installer
sw-update add                # Add a new app interactively
sw-update remove <name>      # Remove an app from config
sw-update enable <name>      # Enable an app for checking
sw-update disable <name>     # Disable an app without removing it
sw-update mark <name>        # Mark current latest as seen (skip next update prompt)
sw-update import <file>      # Bootstrap config from a Netscape bookmarks HTML file
```

### Environment

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | Personal access token — raises GitHub API limit from 60 to 5000 req/hour |

### Configuration

Config and state are stored in `%APPDATA%\sw-update\` (Windows) or
`$XDG_CONFIG_HOME/sw-update/` (Linux/macOS):

- `sw-update.json` — app definitions (checkers, local detectors, download dir)
- `sw-update-state.json` — auto-updated; tracks latest/installed/seen versions and last-checked timestamps

#### Checker types

| Type | Description |
|------|-------------|
| `github` | Fetches latest release from a GitHub repo (`owner/repo`) |
| `html` | Scrapes a version string from a web page using a regex |
| `sourceforge` | Scans a SourceForge project's RSS feed for new file releases |

**`github` checker fields:**

| Field | Description |
|-------|-------------|
| `repo` | Repository in `owner/repo` form |
| `prerelease` | `true` to include pre-releases (default: `false`) |
| `asset_patterns` | Glob patterns to select specific release assets |

**`html` checker fields:**

| Field | Description |
|-------|-------------|
| `url` | Page URL to fetch |
| `version_regex` | Go regex; capture group 1 is used as version if present |

**`sourceforge` checker fields:**

| Field | Description |
|-------|-------------|
| `project` | SourceForge project slug (e.g. `munt`) |
| `sf_path` | Path within the project's files tree to filter RSS results (e.g. `/munt`) |
| `version_regex` | Go regex applied to each RSS file path; capture group 1 is used as version |
| `asset_patterns` | Glob patterns to select specific files as download assets |

> The SourceForge checker uses the project's public RSS feed, which is not
> subject to bot-protection. It scans all entries for filenames matching
> `version_regex` and returns the highest version found.

#### Local detector types

| Type | Description |
|------|-------------|
| `none` | No local detection; update prompt is shown whenever a new version appears |
| `exe` | Runs an executable with given args and extracts version from output |
| `file-version` | Reads the Windows file-version resource from an exe/dll |

---

## Project Structure

```
.
├── Makefile
├── dist/               # Built executables (git-ignored)
├── mkv-audit/
├── mkv-movie/
├── mkv-musicvideo/
├── mkv-tvshow/
└── sw-update/
```

Each subdirectory is an independent Go module.
