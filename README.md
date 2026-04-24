# Scripts

A collection of Go command-line utilities for managing MKV media files, CBZ comic archives, and software updates.

## Tools

| Tool | Purpose |
|------|---------|
| [cbz-fix](#cbz-fix) | Normalise CBZ archives: rename cover, insert blank page, renumber pages |
| [mkv-audit](#mkv-audit) | Normalize MKV track language codes and default/forced flags |
| [mkv-movie](#mkv-movie) | Tag movie MKVs with full metadata from TMDB and sync timestamps |
| [mkv-music](#mkv-music) | Fuzzy-match video files to audio files for renaming; sync audio file timestamps |
| [mkv-tvshow](#mkv-tvshow) | Tag TV episode MKVs with metadata from TMDB |
| [sw-update](#sw-update) | Track and install software updates from GitHub releases or web pages |

## Requirements

- Go 1.22+
- [MKVToolNix](https://mkvtoolnix.download/) (`mkvmerge`, `mkvpropedit`) — required by mkv-audit, mkv-movie, mkv-tvshow
- TMDB API key — required by mkv-movie, mkv-tvshow; optional for mkv-audit (enables automatic IETF and original-language detection)

## Building

Build all tools to the `dist/` directory:

```sh
make all
```

Build a specific tool:

```sh
make cbz-fix
make mkv-audit
make mkv-movie
make mkv-music
make mkv-tvshow
make sw-update
```

Run all tests:

```sh
make test
```

---

## cbz-fix

Normalises CBZ comic archives for consistent two-page-spread viewing:

1. **Backs up** each CBZ to `<file>.cbz.bak` alongside the original.
2. **Reads `ComicInfo.xml`** (if present) to gather page metadata.
3. **Renames the cover** (first image by natural sort order) to `cover.<ext>`.
4. **Inserts a blank page** — white, same dimensions as the first non-double-page
   after the cover — so that interior pages land on the correct side in a
   two-page-spread viewer.
5. **Renames all remaining pages** to `p001.<ext>`, `p002.<ext>`, … in sort order,
   moving every image to the root of the zip.
6. **Updates `ComicInfo.xml`** `PageCount` and `Pages` to reflect the new layout.
   Creates a minimal `ComicInfo.xml` if one did not exist.

A double-page spread is detected by checking `DoublePage="true"` or
`Type="DoublePage"` in `ComicInfo.xml` first, then falling back to image
dimensions (width > height = landscape = double spread).

Files whose first image is already named `cover.*` and second image `p001.*`
are treated as already processed and skipped.

### Usage

Run from (or pass as an argument) the directory containing your CBZ files:

```sh
cbz-fix              # process all CBZs under the current directory
cbz-fix /path/to/comics
cbz-fix --check      # list files that need fixing; exit 1 if any found
cbz-fix --dry-run    # preview without making changes
cbz-fix --verbose    # show per-file rename details
cbz-fix --no-blank   # rename and renumber without inserting a blank page
```

#### Flags

| Flag | Description |
|------|-------------|
| `--check` | List files that need processing and exit 1 if any are found (implies `--dry-run`) |
| `--dry-run` | Preview all changes without modifying any files |
| `--no-blank` | Skip blank page insertion (rename and renumber only) |
| `--verbose` | Print each filename mapping for every CBZ processed |

### Output

```
/comics/Batman Vol 1/Batman 001.cbz
/comics/Batman Vol 1/Batman 002.cbz
  [blank 1280x1988] → p001.png
  Batman_002_p001.jpg → p002.jpg
  Batman_002_p002.jpg → p003.jpg
  ...

Done — processed: 2  errors: 0
```

---

## mkv-audit

Normalizes MKV/MKA track metadata in bulk:

- Language codes to ISO 639-2/B (e.g. `fre`, `jpn`, `kor`)
- IETF/BCP-47 language tags (e.g. `en-US`, `ja-JP`), optionally derived from TMDB
- Default and forced flags based on language and codec
- `flag-hearing-impaired` on SDH/CC tracks; `flag-commentary` on commentary tracks
- `flag-original` on tracks whose language matches the content's original language (requires TMDB)
- Track ordering by codec quality (e.g. TrueHD before AC3)
- **Track trimming** (with prompt): removes audio tracks not in the original or English language, removes non-English subtitles, and reorders audio so original-language tracks come first

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

- `mkvmerge` — reads track metadata; also used for track trimming remux
- `mkvpropedit` — applies property edits (language codes, flags)

#### Flags

| Flag | Description |
|------|-------------|
| `--library <name>` | Use a named library from config (overrides auto-detection) |
| `--verbose` | Print planned mkvpropedit edits before applying |
| `--cutoff-date YYYY-MM-DD` | Only process files modified on or after this date (defaults to today) |
| `--all` | Process all files regardless of modification date |

### Track trimming

After applying property fixes, the tool identifies files where tracks should be
removed or reordered. For each such file it prints what will change and prompts:

```
[TRIM]   Movie.mkv
  Remove audio:  ger (A_AC3 6ch), fre (A_AC3 6ch)
  Remove subs:   ger (S_TEXT/UTF8), fre (S_TEXT/UTF8)
  Reorder audio: jpn (A_FLAC 6ch), eng (A_AC3 2ch)
  Proceed? (y/n/a=all):
```

- **`y`** — remux this file
- **`n`** — skip this file
- **`a`** — remux this and all remaining files without further prompting

Keep rules:
- **Audio**: retain tracks in the original language and English; all others are removed. If the original language is unknown (no TMDB lookup), all audio is kept.
- **Subtitles**: retain English and undetermined (`und`) tracks only.
- **Audio order**: original-language tracks first, then English.

Tracks with undetermined (`und`) language are always kept.

### Configuration

Config and state files are stored in `%APPDATA%\mkv-audit\` (Windows) or
`$XDG_CONFIG_HOME/mkv-audit/` (Linux/macOS).

#### `config.json` (optional)

Define named libraries with their root paths and per-directory IETF overrides.
The tool auto-detects which library to use based on the current working directory
(longest matching path wins).

```json
{
  "libraries": [
    {
      "name": "TV",
      "type": "tv",
      "path": "G:\\TV",
      "overrides": [
        {
          "path": "Agatha Christie's Poirot (1989)",
          "ietf": { "eng": "en-GB" }
        }
      ]
    },
    {
      "name": "Movies",
      "type": "movie",
      "path": "G:\\Movies"
    },
    {
      "name": "Anime",
      "path": "G:\\Anime"
    }
  ]
}
```

**`type`** (optional) — `"tv"` or `"movie"`. When set and `TMDB_API_KEY` is in the
environment, mkv-audit looks up each show/movie on TMDB to determine its
`original_language` and `origin_country`. This drives two things:

1. The IETF tag for the original language is set to the country-specific variant
   (e.g. a Canadian show with `original_language: "en"` and `origin_country: ["CA"]`
   gets `eng → en-CA`).
2. `flag-original=1` is set on all tracks whose language matches the original language.

Manual `overrides` entries always take precedence over TMDB-derived values.

Track names containing region keywords (e.g. `British`, `Australian`) also
trigger automatic IETF overrides and always win over TMDB.

#### Auto-generated state files

Stored per-library in `%APPDATA%\mkv-audit\<library-name>\`:

- `failed_files.json` — files that could not be analyzed (skipped on re-runs)
- `unknown_codecs.json` — codecs not present in the priority map

---

## mkv-movie

Fetches full movie metadata from TMDB by filename and writes it to each MKV
file: title, release date, plot summary, TMDB ID, poster attachment, and NZ
content rating. Sets filesystem timestamps to the TMDB release date.

Movie identity is derived from the filename (title + year) and resolved via
TMDB search on first run, then cached.

### Usage

Set `TMDB_API_KEY` and run from the directory containing your MKV files:

```sh
TMDB_API_KEY=your_key mkv-movie
```

Flags:

| Flag | Description |
|------|-------------|
| `--overwrite-ratings` | Re-fetch all NZ ratings, ignoring cached values |
| `--overwrite-language` | Re-apply Plex audio language preference even if already set for a movie |
| `--force` | Re-apply all metadata even if already cached |

Output:

```
Updated: 8  OK: 42  Skipped: 3  Errors: 0
```

### Dependencies

- `TMDB_API_KEY` — TMDB v3 API key (required)
- `mkvpropedit` — writes metadata to MKV files (optional; a warning is shown if missing)
- `PLEX_URL` / `PLEX_TOKEN` — optional; used to read/write NZ ratings in Plex and set preferred audio language

When Plex is configured, after processing each movie the tool pushes the resolved NZ content rating and sets the movie's preferred audio language to the TMDB original language. The language is cached in `movie_rating_cache.json` and only applied once per movie; use `--overwrite-language` to force a re-apply.

> **Note:** Filesystem timestamp modification is Windows-only. On other
> platforms the internal header is still updated if `mkvpropedit` is available.

### Configuration

The cache is stored in `%APPDATA%\mkv-movie\movie_rating_cache.json`
(Windows) or `$XDG_CONFIG_HOME/mkv-movie/movie_rating_cache.json` (Linux/macOS).
It persists TMDB IDs (keyed by `title:YEAR`), NZ ratings, and which files have
already had their full metadata written.

---

## mkv-music

Matches video files to their corresponding audio files using fuzzy filename
similarity (LCS-based), then renames them to a consistent scheme. Also sets
OS file timestamps on audio files (flac, mp3, m4a, mka, wma, dsf) from their
embedded date tags.

Supported video extensions: `.mkv`, `.mp4`, `.avi`, `.mov`, `.vob`
Supported audio extensions: `.flac`, `.mp3`, `.m4a`, `.wav`, `.ogg`, `.wma`, `.mka`, `.dsf`

### Usage

Run from the directory containing your files:

```sh
mkv-music
```

The tool prompts for manual confirmation when a match is ambiguous.

### Configuration

Config is stored in `%APPDATA%\mkv-music\` (Windows) or
`$XDG_CONFIG_HOME/mkv-music/` (Linux/macOS).

#### `ignore_patterns.json` (optional)

Substrings to strip from video filenames before fuzzy matching. Patterns can be
**global** (applied to every file) or **file-specific** (applied only when
matching a particular video filename).

```json
{
  "global": ["2023", "1080p", "Remastered"],
  "file_patterns": {
    "Artist - Song (Stone Version).mp4": ["(Stone Version)"],
    "Artist - Song (Live).mp4": ["(Live)"]
  }
}
```

When you choose `(i)gnore pattern` interactively, the pattern is saved under
`file_patterns` for the specific video file being matched — it does **not**
become a global pattern.

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
| `--cutoff-date YYYY-MM-DD` | today | Only process files modified on or after this date |
| `--prompt-each` | false | Prompt before processing each cached show (default: process all) |
| `--ratings-only` | false | Only update NZ content ratings; skip all other metadata |
| `--overwrite-ratings` | false | Re-fetch all ratings, ignoring cached values |
| `--overwrite-language` | false | Re-apply Plex audio language preference even if already set for a show |
| `--language-only` | false | Apply Plex audio language preference for all cached shows without scanning files |
| `--missing` | false | Check for missing episodes (see below) |
| `--title-check` | false | Check episode titles in filenames against TMDB (see below) |

#### Output

```
Success: 12  Failed: 0  SkippedOld: 5
```

### Missing episode check (`--missing`)

Run from the media directory to find episodes that have aired on TMDB but are
not present in your local collection:

```sh
mkv-tvshow --missing
```

The tool checks every show folder that is already tracked in `show_cache.json`.
For each show it:

1. Fetches the full season/episode list from TMDB.
2. Scans local MKV files to determine which episodes you already have.
3. Reports **whole missing seasons** first (every aired episode absent), then
   **individual missing episodes** within partially-present seasons.
4. Prints a TMDB link for each missing item so you can quickly review it.

After displaying the results for each show, you are prompted:

```
Ignore items? Enter numbers (e.g. "1,3"), "show" to ignore the whole show, or press Enter to skip:
```

Selected items are saved to `missing_ignore.json` and will not appear in future
runs. You can ignore a single episode, a whole season, or the entire show.

### Dependencies

- TMDB API (requires `TMDB_API_KEY` environment variable)
- `mkvpropedit` — applies metadata edits
- `PLEX_URL` / `PLEX_TOKEN` — optional; used to push NZ ratings and set per-show audio language in Plex

When Plex is configured, after processing each show the tool:
1. Pushes the resolved NZ content rating to every episode, season, and the show itself.
2. Sets the show's preferred audio language in Plex to the TMDB original language (e.g. `ja` for anime). This is applied once per show and cached in `rating_cache.json` so you can override it inside Plex without it being reset on the next run. Use `--overwrite-language` to force a re-apply.

### Configuration

Config and cache files are stored in `%APPDATA%\mkv-tvshow\` (Windows) or
`$XDG_CONFIG_HOME/mkv-tvshow/` (Linux/macOS).

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
      "S01E05": [1, 5],
      "S01E18": [1, 18, 20, 22]
    },
    "extras": {
      "Show Name - S02 - Drama CD": { "date": "2022-04-01", "season": 2 },
      "Show Name - S01 NCOP": { "season": 1, "episode": 1 }
    }
  }
}
```

- `overrides` — redirect a local season to a different TMDB show/season
- `mapping` — remap a local episode to a different TMDB `[season, episode]`.
  For range files (e.g. `S01E18-E20`) where local and TMDB numbering diverge
  non-linearly, use `[season, ep1, ep2, ...]` to list all TMDB episode numbers
  explicitly. The processor uses only the first two elements (for date/metadata);
  `--title-check` uses the full list for title comparison.
- `title_overrides` — declare the correct filesystem-safe title for an episode
  when it cannot be derived from TMDB (e.g. DVD releases that combine two OVA
  episodes). `--title-check` compares against this value instead of fetching
  TMDB. Use the same lookalike characters as your filenames (`╱` for `/`, etc.).
- `extras` — metadata for non-episode files (bonus content, OPs/EDs, etc.)
  that have no `S##E##` in their name. The key is the filename **without the
  `.mkv` extension**.
  - `date` — YYYY-MM-DD release date written to MKV tags and filesystem
    timestamps. Omit when using `episode`.
  - `season` — selects the season poster to attach.
  - `episode` — optional. When set, fetches the air date, title, and summary
    from that TMDB episode (season × episode) instead of using a manual `date`.

  If the filename ends with an OP/ED/NCOP/NCED suffix (with optional number,
  e.g. `NCOP1`, `ED2`), it is automatically detected and the MKV title becomes
  the episode title (or filename base) with the suffix appended in parentheses,
  e.g. `"The Kick-Off (NCOP)"` or `"Show Name - S02 (ED)"`.

See [mkv-tvshow/manual_data.json.example](mkv-tvshow/manual_data.json.example) for a full example.

### Episode title check (`--title-check`)

Run from the media directory to compare the episode title(s) in your filenames
against TMDB and optionally rename files that differ:

```sh
mkv-tvshow --title-check
```

The tool checks every show folder that is already tracked in `show_cache.json`.
For each MKV file it:

1. Parses the episode title(s) from the filename (restoring filesystem-safe
   lookalike characters such as `꞉` → `:`).
2. Fetches the matching episode title(s) from TMDB (respecting any
   `overrides`/`mapping` entries in `manual_data.json`).
3. Compares the titles case-insensitively with whitespace normalisation.
4. If they differ, prints both the filename and TMDB versions and prompts:

```
Rename to use TMDB title(s)? [y/n]:
```

Multi-episode files (e.g. `S01E01E02` or `S01E01-E03`) have their titles
compared individually against each corresponding TMDB episode and joined with
` ﹢ ` when renaming.

If the new path would exceed Windows MAX_PATH (260 characters), the title is
automatically truncated at a word boundary to fit.

After checking (and optionally renaming) each episode, `--title-check` also sets
the MKV `title` metadata to the **full, untruncated** TMDB title — MKV metadata
has no path-length limit. The result is cached in `title_cache.json` so that
normal mode (`mkv-tvshow`) does not overwrite the extended title with the
shorter filename-derived version on subsequent runs.

#### `title_cache.json` (auto-generated by `--title-check`)

Maps absolute file paths to the full MKV title last written by `--title-check`.
Normal mode reads this cache before deriving the title from the filename, so
cached entries take precedence.

```json
{
  "/media/TV/Breaking Bad/Season 1/Breaking Bad - S01E01 - Pilot.mkv": "Breaking Bad - S01E01 - Pilot"
}
```

#### `missing_ignore.json` (auto-generated by `--missing`)

Stores items suppressed from future missing-episode reports. Edit manually to
remove an entry and have it appear again.

```json
{
  "shows": ["1396"],
  "seasons": ["60059/S04"],
  "episodes": ["1396/S01E02"]
}
```

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
| `http-header` | Reads an HTTP response header from a URL (e.g. `Last-Modified`) as the version |

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

**`http-header` checker fields:**

| Field | Description |
|-------|-------------|
| `url` | URL to send a HEAD request to |
| `header` | Header to use as the version (default: `Last-Modified`, formatted as `YYYY-MM-DD`) |

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
├── cbz-fix/
├── mkv-audit/
├── mkv-movie/
├── mkv-music/
├── mkv-tvshow/
└── sw-update/
```

Each subdirectory is an independent Go module.
