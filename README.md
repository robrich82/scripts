# MKV Media Tools

A collection of Go command-line utilities for managing MKV media files.

## Tools

| Tool | Purpose |
|------|---------|
| [mkv-audit](#mkv-audit) | Normalize MKV track language codes and default/forced flags |
| [mkv-movie](#mkv-movie) | Sync MKV date headers and file timestamps from mediainfo |
| [mkv-musicvideo](#mkv-musicvideo) | Fuzzy-match video files to audio files for renaming |
| [mkv-tvshow](#mkv-tvshow) | Tag TV episode MKVs with metadata from TMDB |

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

## Project Structure

```
.
├── Makefile
├── dist/               # Built executables (git-ignored)
├── mkv-audit/
├── mkv-movie/
├── mkv-musicvideo/
└── mkv-tvshow/
```

Each subdirectory is an independent Go module.
