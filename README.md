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

- Go 1.27.3, golangci-lint and govulncheck, pinned in `mise.toml`: run `mise install`.
  - A linter `go install`ed into `go env GOBIN` shadows the pinned one; delete it there.
- [MKVToolNix](https://mkvtoolnix.download/) (`mkvmerge`, `mkvpropedit`, `mkvextract`) — required by mkv-audit, mkv-movie, mkv-tvshow
- [ExifTool](https://exiftool.org/) (`exiftool` on PATH) — used by mkv-music's audio timestamp sync
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

Scan all modules for known vulnerabilities in their dependencies with
[govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) (needs network access):

```sh
make vuln
```

---

## cbz-fix

Normalises CBZ comic archives for consistent two-page-spread viewing:

1. **Backs up** each CBZ to `<file>.cbz.bak` alongside the original, with `--backup`.
2. **Reads `ComicInfo.xml`** (if present) to gather page metadata.
3. **Determines page order**: for calibre KFX/AZW3 exports (a root `.html` file
   next to a matching `*_files/` directory), the XHTML navigation chain is
   followed to get the correct reading order — image filenames in these archives
   are opaque hashes that sort incorrectly by name. The cover (`*_files/cover.*`)
   is promoted to the front; if it is also stored as the first reading page under
   a different name (a byte-identical duplicate, common in AZW3 exports), that
   duplicate page is dropped so the cover isn't repeated as `p001`. For all other
   CBZs, images are sorted by natural (numeric) order.
4. **Converts non-JPEG/PNG pages to PNG**: any page whose stored bytes are not
   already a JPEG or PNG (judged by content, not extension: JPEG XL, WebP,
   GIF, BMP, TIFF, AVIF, HEIC, JPEG 2000) is converted to a white-flattened PNG via
   ImageMagick and renamed with a `.png` extension. This requires the ImageMagick
   `magick` CLI on your `PATH`;
   it is run as `magick <format>:<in> -background white -flatten <out>.png`
   (transparency and layers are composited onto white). A page in any format
   outside that list, such as SVG, fails the file (in a dry run too) instead of
   reaching ImageMagick. Pages
   already in JPEG/PNG are copied through untouched.
5. **Renames the cover** (first image in reading order) to `cover.<ext>`.
6. **Inserts a blank page** — white, same dimensions as the first non-double-page
   after the cover — so that interior pages land on the correct side in a
   two-page-spread viewer.
7. **Renames all remaining pages** to `p001.<ext>`, `p002.<ext>`, … in reading order,
   moving every image to the root of the zip.
8. **Updates `ComicInfo.xml`** `PageCount` and `Pages` to reflect the new layout.
   Creates a minimal `ComicInfo.xml` if one did not exist.

A double-page spread is detected by checking `DoublePage="true"` or
`Type="DoublePage"` in `ComicInfo.xml` first, then falling back to image
dimensions (width > height = landscape = double spread).

Files whose first image is already named `cover.*` and second image `p001.*`
are treated as already processed and skipped. Pass `--force` to reprocess them
anyway (e.g. to convert leftover non-PNG pages from an earlier run). The page at
the insertion point is checked first: if it is already a blank (all-white) page
no second one is added, but an archive that is missing its blank — one first
processed with `--no-blank`, say — gets one inserted.

A `.cbz` whose contents are actually a different archive format is detected by
its file header and renamed to the correct comic extension instead of being
processed: RAR → `.cbr`, 7z → `.cb7`, ACE → `.cba`, tar → `.cbt`. In `--dry-run`
or `--check` mode the rename is reported but not performed; if the target name
already exists the file is left untouched and an error is reported.

### Usage

Run from (or pass as an argument) the directory containing your CBZ files:

```sh
cbz-fix              # process all CBZs under the current directory
cbz-fix /path/to/comics
cbz-fix --check      # list files that need fixing; exit 1 if any found
cbz-fix --dry-run    # preview without making changes
cbz-fix --verbose    # show per-file rename details
cbz-fix --no-blank   # rename and renumber without inserting a blank page
cbz-fix --force      # reprocess files that already look processed
cbz-fix --backup     # create a .bak file before overwriting
cbz-fix --update-cover  # replace in-archive cover with a higher-res cover.jpg sibling
```

#### Flags

| Flag | Description |
|------|-------------|
| `--check` | List files that need processing and exit 1 if any are found (implies `--dry-run`) |
| `--dry-run` | Preview all changes without modifying any files |
| `--backup` | Create a `.bak` file before overwriting the original |
| `--no-blank` | Skip blank page insertion (rename and renumber only) |
| `--force` | Reprocess files that already look processed (`cover.*` + `p001.*`). Useful for converting leftover non-PNG pages. Inserts the blank page only if the archive is missing one, so it never duplicates it. |
| `--verbose` | Print each filename mapping for every CBZ processed |
| `--update-cover` | For each CBZ, look for a `cover.jpg` in the same directory. If it is larger in both dimensions, replace the `cover.*` entry inside the CBZ. If only one dimension is greater, prompt interactively. |

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
- **Container conversion**: other video files (`.mp4`, `.m4v`, `.avi`, `.mpg`/`.mpeg`, `.mov`, `.ts`, `.m2ts`, `.webm`, `.flv`) are remuxed to `<name>.mkv`, which keeps the original's modification and creation times. The original is deleted once the MKV has every source track. A file is skipped with a warning when `<name>.mkv` already exists or `mkvmerge` can't read it (e.g. `.wmv`)
- **Track trimming** (with prompt): removes audio tracks not in the original or English language, removes non-English subtitles, and reorders tracks so that the best-quality track for each language is physically first (for Plex compatibility)

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

- `mkvmerge` — reads track metadata; also used for container conversion and track trimming remux
- `mkvpropedit` — applies property edits (language codes, flags)

#### Flags

| Flag | Description |
|------|-------------|
| `--library <name>` | Use a named library from config (overrides auto-detection) |
| `--verbose` | Print planned mkvpropedit edits and MKV conversions before applying |
| `--cutoff-date YYYY-MM-DD` | Only process files modified on or after this date (defaults to today) |
| `--all` | Process all files regardless of modification date |
| `--clean` | Remove stale `overrides` entries from `config.json` for directories that no longer exist on disk, then exit. Overrides on an offline drive are kept |

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
- **Audio**: retain tracks in the original language and in any language a Plex account is configured to watch in (see below); all others are removed. If the original language is unknown (no TMDB lookup), all audio is kept. Trim never removes the last audio track: when no track is in a kept language, all audio is kept with a warning to check the language tags. When that is the file's only audio track, it also offers to retag it as the original language (`y/n/a`).
- **Subtitles**: retain English and undetermined (`und`) tracks only. Forced/signs English subtitles are kept only when English audio is also kept — they exist to caption on-screen text for someone watching in English, so with nobody watching in English they are removed. A track named "Dialogue & Signs" is a full track, not a forced one, unless its name also says "forced".

#### Per-account audio languages

Trim reads the `language`, `alt-language`, and `alt2-language` keys from the
sibling tool's `manual_data.json` — `%APPDATA%\mkv-tvshow\` for a `"tv"` library,
`%APPDATA%\mkv-movie\` for a `"movie"` one (set by the library's `type`). These
are the same keys those tools use to set each Plex account's preferred audio
language, so trim keeps exactly the audio someone actually watches:

| Original language | Configured languages | Audio kept | English subtitles kept |
|---|---|---|---|
| `ja` | none | `ja` | main only |
| `ja` | `"alt-language": "en"` | `ja`, `en` | main + forced/signs |
| `ja` | `"alt-language": "fr"` | `ja`, `fr` | main only |
| `en` | none | `en` | main + forced/signs |

Without a `type` on the library, or with no `manual_data.json` entry for the
title, trim falls back to the original language alone — for foreign-language
content that means English audio is removed. Set `language`/`alt-language`/
`alt2-language` to `"en"` for any title whose English dub you want to keep.

Ordering rules (Plex uses the first track for a language, ignoring the default flag):
- **Audio order**: within each language group, the best-quality track (most channels, then codec priority) is placed first, with commentary/SDH tracks last. When the original language is non-English, the original-language group precedes English.
- **Subtitle order**: within each language group, the best main subtitle (by region score and codec priority) is placed first, with forced/signs tracks last.

Tracks with undetermined (`und`) language are always kept.

### Configuration

Config and state files are stored in `%APPDATA%\mkv-audit\` (Windows) or
`$XDG_CONFIG_HOME/mkv-audit/` (Linux/macOS).

#### `config.json` (optional)

Define named libraries with their root paths and per-directory overrides.
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
        },
        {
          "path": "Star Wars꞉ Visions (2021)",
          "origin_lang": "jpn"
        }
      ]
    },
    {
      "name": "Movies",
      "type": "movie",
      "path": "G:\\Movies",
      "und_lang": "en-US"
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

**`und_lang`** (optional, on library or override) — last-resort fallback language for
tracks tagged `und` (undefined). Accepts an ISO 639-2/B code (`"eng"`) or IETF tag
(`"en-US"`). Both `language` and `language-ietf` are updated. This is strictly a
fallback: per-file inference (all other subtitle tracks share one language) always wins.
Useful when a subtitle is the only track in the file — e.g. an anime movie with a
single English subtitle mislabelled as `und`.

Set it on the library for a library-wide default, or on an override entry for a
specific subdirectory:

```json
{ "path": "Some Show", "und_lang": "eng" }
```

**`overrides[].path`** — relative path to match (case-insensitive prefix at a directory
boundary). Use `"."` to match every file in the library.

**`overrides[].ietf`** (optional) — overrides the IETF/BCP-47 tag written for one or
more ISO 639-2 language codes within the matched path. Useful for regional English
variants (e.g. `"eng": "en-GB"` for a British show). Manual `ietf` overrides always take
precedence over TMDB-derived values.

**`overrides[].origin_lang`** (optional) — overrides the TMDB-reported original language
for a specific subdirectory. Use this when TMDB has the wrong original language for a
show (e.g. a Japanese anime incorrectly listed as English). Accepts any ISO 639-2/B code
(`"jpn"`, `"kor"`, `"fre"`, etc.). This affects `flag-original` and track trimming but
does not alter IETF tag mappings.

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
TMDB search on first run, then cached. When the search finds no match, the tool
prompts for the movie's TMDB ID rather than skipping the file:

```
No TMDB match for "Obscure Film (2019)".
Enter TMDB ID or themoviedb.org URL, or blank to skip:
```

Either a bare ID (`12345`) or a full themoviedb.org movie URL is accepted; the
ID is verified against TMDB before use and then cached, so it is asked for once.
Leaving the prompt blank skips the file as before.

The NZ rating is asked for once per movie and then cached. A rating found on
TMDB or by the Classification Office is shown for you to confirm (blank) or
override. With neither, you're prompted, with the rating Plex already has as the
blank-input default (the movie is found in Plex by TMDB ID, then by title and
release year).

### Usage

Set `TMDB_API_KEY` and run from the directory containing your MKV files:

```sh
TMDB_API_KEY=your_key mkv-movie
```

Flags:

| Flag | Description |
|------|-------------|
| `--cutoff-date YYYY-MM-DD` | Only process files modified on or after this date (defaults to today). Every file in the window is retagged, even if an earlier run tagged it |
| `--all` | Process all files regardless of modification date |
| `--overwrite-ratings` | Re-fetch all NZ ratings, ignoring cached values. A source rating that differs from the cached one is shown for confirmation; a movie no source rates keeps its cached rating |
| `--overwrite-streams` | Re-select Plex audio/subtitle streams even if already applied for a movie |
| `--clean` | Remove cache entries for movie files that no longer exist on disk, and the ratings, TMDB IDs and Plex state of movies no remaining file maps to (and lists them), then exit. Entries on an offline drive are kept |
| `--ratings-only` | Only write the NZ rating (`COUNTRY`/`LAW_RATING`) into each file's existing tags, restoring its timestamps, and push it to Plex. Every file is processed whatever its age; `--cutoff-date`/`--all` are ignored. A file the cache records as already holding the rating is left alone, as is Plex when it was the last rating pushed; `--overwrite-ratings` re-fetches the rating and rewrites the tags. TMDB is called only to look a rating up, or for the title and year Plex is searched by when one is pushed. Without MKVToolNix only Plex is updated; without Plex too it exits with an error. Prints `Done. Updated: N  Skipped: N  Errors: N` |
| `--language-only` | Only set Plex audio/subtitle streams, for every movie the cache maps a file to (run `--clean` first to drop deleted files), from any folder; `--cutoff-date`/`--all` are ignored. The files aren't read, so an offline drive doesn't matter. Streams are set only on accounts not yet recorded as having them; `--overwrite-streams` sets every account again. A movie with several files takes each of `language`, `alt-language` and `alt2-language` from the first file name, in name order, whose `manual_data.json` entry sets it. TMDB is called for the original language when no entry sets `language`, and for the title and year Plex is searched by when the streams are due. Needs `PLEX_URL`/`PLEX_TOKEN`, but not MKVToolNix. Prints `Done. Updated: N  Skipped: N  Errors: N` |
| `--reset-rating <query>` | Clear the cached NZ rating and the rating last pushed to Plex for a movie, then exit. Run `mkv-movie --ratings-only` in its folder to fetch the rating again, retag it and push it. `query` is a title substring or TMDB ID |
| `--set-rating <RATING>` | Set `RATING` as the NZ rating for **every** movie in the current directory, overriding any cached value, then exit (see below) |
| `--deband-only` | Update only the `DEBAND` MKV tag for movies configured in `manual_data.json`; preserves all other tags and timestamps |

`--clean`, `--ratings-only`, `--language-only`, `--reset-rating`, `--set-rating` and `--deband-only` each run on their own: setting two of them exits with an error before anything runs.

Output:

```
Done. Updated: 8  Skipped: 3  Skipped (old): 412  Errors: 0
```

### Bulk rating override (`--set-rating`)

Applies one NZ rating to every movie in the current directory instead of
answering the rating prompt title by title:

```sh
mkv-movie --set-rating R16
```

Each `.mkv` under the directory is resolved to its cached TMDB ID, the affected
movies are listed with their current rating, and nothing is written until you
confirm. Files with no cached TMDB ID (never processed) are listed as skipped —
run `mkv-movie` normally on them first.

Confirming replaces the cached rating and clears the recorded Plex rating. No
TMDB, Plex, or network calls are made: the next `mkv-movie --ratings-only` in the
same directory writes the new `LAW_RATING` tag and pushes it to Plex.

### Dependencies

- `TMDB_API_KEY` — TMDB v3 API key (required)
- `mkvpropedit` and `mkvextract` — write metadata to MKV files, reading the existing tags first so the rest are kept (optional; without either, a warning is shown and files are skipped with their timestamps untouched, so they stay in the `--cutoff-date` window; Plex updates still run, and a failed one makes the file an error)
- `PLEX_URL` / `PLEX_TOKEN` — optional (required by `--language-only`); used to read/write NZ ratings in Plex and set preferred audio language
- `PLEX_ALT_TOKEN` / `PLEX_ALT2_TOKEN` — optional tokens for additional Plex accounts; stream selection is applied for these accounts too

When Plex is configured, after processing each movie the tool pushes the resolved NZ content rating and selects the appropriate audio and subtitle streams: the audio track matching the TMDB original language (e.g. Japanese for anime) and the first non-forced English subtitle. Stream selection is cached in `movie_rating_cache.json` and only applied once per movie and account, so a selection you change in Plex afterwards is kept; use `--overwrite-streams` to force a re-apply.

Stream selection is per-Plex-account, so if `PLEX_ALT_TOKEN` and/or `PLEX_ALT2_TOKEN` are set the same selection is applied for those accounts — or a different audio language per account via `alt-language` / `alt2-language` in `manual_data.json`. Ratings, lookups, and library refreshes still use `PLEX_TOKEN` only. An alt account that fails (for example, a movie it cannot see) is reported alongside the result as `alt: ...` / `alt2: ...`, even when no stream changed, and does not fail the run. It isn't recorded as applied, so a later `mkv-movie --all` in its folder, or `mkv-movie --language-only`, retries that account alone, and reports it again if it still fails.

The cached "already applied" value records the language each account received (`ja|alt=ja|alt2=en`). An account missing from it, or whose language has since changed, is applied on the next run that reaches the file; the others are left alone. Adding a token applies only the new account.

A file whose tags couldn't be written counts as an error and keeps its timestamps, so it stays in the `--cutoff-date` window and a later run that still covers it retries. A failed poster download, Plex lookup or push, or timestamp change also counts as an error, named on the result line as `(failed: …)`; the file is backdated because its tags are right, so retry with `mkv-movie --all` in its folder (or `--language-only`, when only the stream push failed). A movie with no audio track in its Plex language fails its stream push on each run that reaches it until `language` in `manual_data.json` names one it has. A movie Plex doesn't have yet (its library refresh is asynchronous) isn't recorded as pushed; once Plex has it, `mkv-movie --all` in its folder pushes it (`--ratings-only` pushes just the rating, `--language-only` just the streams).

> **Note:** Filesystem timestamp modification is Windows-only. On other
> platforms the internal header is still updated if `mkvpropedit` and `mkvextract` are available.

### Configuration

The cache is stored in `%APPDATA%\mkv-movie\movie_rating_cache.json`
(Windows) or `$XDG_CONFIG_HOME/mkv-movie/movie_rating_cache.json` (Linux/macOS).
It persists TMDB IDs (keyed by `title:YEAR`), which TMDB ID each file is, NZ
ratings, the rating written to each file's tags, and Plex state. It is saved after each file (each movie, with `--language-only`), so an interrupted run keeps what it has
resolved so far.

#### manual_data.json

An optional `manual_data.json` in the config directory allows per-movie overrides.
Keys are filename stems (filename without `.mkv`):

```json
{
  "Coco (2017)": { "deband": true },
  "Spirited Away (2001)": { "deband": true, "language": "ja", "alt-language": "en" }
}
```

**`deband`** — when `true`, embeds a `DEBAND=true` tag in the MKV alongside the
other metadata. Use `--deband-only` to update only this tag without touching any
other metadata or timestamps.

**`language`** — ISO 639-1 code (e.g. `"ja"`, `"ko"`) that overrides the TMDB
`original_language` when selecting the Plex audio stream. Use this when TMDB
reports the wrong original language for a movie.

**`alt-language`** / **`alt2-language`** — ISO 639-1 code applied instead of
`language` for the `PLEX_ALT_TOKEN` and `PLEX_ALT2_TOKEN` accounts, for when
those users prefer a different audio language (e.g. `"language": "ja"` with
`"alt-language": "en"` for a dubbed viewer). An account with no override gets
`language`. Changing any of the three re-applies stream selection on the next
run.

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

When a video's name starts with a track number (`01 - …`) and an audio file in
the same folder has that track number, that audio file is the match even if the
titles differ. A number shared by every audio file in the folder (an artist such
as `311`, or a folder of `01 - …` singles) is matched on title instead.

Plain names are prompted first (`-video` before `-video (Second Version)`,
ignore patterns applied), then a known suffix (`-video`, `-live`, …) before
none. When another video has the standard name, or is a plain video for the
same track that was skipped, a video keeps its extra text:
`01 - Old - Old (Special Music Video).mkv` → `01 - New - New (Special Music Video)-video.mkv`,
and `01 - Old - Old-video (Second Version).mkv` → `01 - New - New-video (Second Version).mkv`.
A renamed MKV's title tag is built from its new name, so text the rename drops
(`(Second Version)` on a lone video, an ignored `[HD]`) is left out of the tag.

### Flags

| Flag | Description |
|------|-------------|
| `--cutoff-date YYYY-MM-DD` | Only process files modified on or after this date (defaults to today) |
| `--all` | Process all files regardless of modification date |
| `--clean` | Remove file-specific pattern entries from `ignore_patterns.json` for video files that no longer exist under the current directory, then exit. Entries on an offline drive, or that it can't check because a folder is unreadable, are kept |

### Dependencies

- `exiftool` on PATH — reads the embedded date from `.mka` files. If it is
  missing, the audio timestamp sync is skipped for every format, with a warning.

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
become a global pattern. If that file is renamed later (say the audio was
retitled), the new name keeps the text its patterns match, and the patterns
move to the new name.

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
| `--all` | false | Process all files regardless of modification date |
| `--prompt-each` | false | Prompt before processing each cached show (default: process all) |
| `--ratings-only` | false | Only update NZ content ratings; skip all other metadata |
| `--overwrite-ratings` | false | Re-fetch all ratings (prompting once per show), ignoring cached values; a cached rating is kept when no source has one |
| `--overwrite-language` | false | Re-apply Plex audio language preference even if already set for a show |
| `--language-only` | false | Apply Plex audio language preference for every cached show folder without scanning files, printing a line per show and a `Done.` summary. Needs `PLEX_URL`/`PLEX_TOKEN`. Skips shows already set (unless `--overwrite-language`), shows with no known language and shows not in Plex. A show cached under several folders takes each language key from the first folder, by name, whose manual_data.json entry sets it |
| `--missing` | false | Check for missing episodes (see below) |
| `--title-check` | false | Check episode titles in filenames against TMDB (see below) |
| `--clean` | false | Remove stale cache entries for show folders and episode files that no longer exist, then exit. Entries on an offline drive are kept |
| `--reset-rating <query>` | — | Clear the cached NZ rating for a show and reset the applied state on all its files so the next run re-fetches and re-applies it. `query` is a folder name substring or TMDB ID |
| `--set-rating <RATING>` | — | Set `RATING` as the NZ rating for **every** show in the current directory, overriding any cached value, then exit (see below) |

`--clean`, `--reset-rating`, `--set-rating`, `--deband-only`, `--missing`, `--language-only`, `--title-check` and `--ratings-only` each run on their own: setting two of them exits with an error before anything runs.

#### Output

```
Done. Success: 12  Failed: 0  Skipped (old): 5
```

### Bulk rating override (`--set-rating`)

Applies one NZ rating to every show in the current directory instead of
answering the rating prompt show by show:

```sh
mkv-tvshow --set-rating R16
```

Each top-level show folder containing `.mkv` files is resolved to its cached
TMDB ID, the affected shows are listed with their current rating, and nothing is
written until you confirm. Folders with no cached TMDB ID (never processed) are
listed as skipped — run `mkv-tvshow` normally on them first.

Confirming replaces the cached show rating, clears any episode-level rating
overrides for those shows (they would otherwise take priority), and clears the
applied state on their files. No TMDB, Plex, or network calls are made: the next
`mkv-tvshow --ratings-only --all` writes the new `LAW_RATING` tags and pushes the
rating to Plex.

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
- `mkvextract` — reads the existing tags so writing new ones keeps the rest; an episode whose tags it can't read isn't updated (part of MKVToolNix)
- `PLEX_URL` / `PLEX_TOKEN` — optional (required for `--language-only`); used to push NZ ratings and set per-show audio language in Plex
- `PLEX_ALT_TOKEN` / `PLEX_ALT2_TOKEN` — optional tokens for additional Plex accounts; the audio language is set for these accounts too

When Plex is configured, after processing each show the tool:
1. Pushes the resolved NZ content rating to every episode, season, and the show itself.
2. Sets the show's preferred audio language in Plex to the TMDB original language (e.g. `ja` for anime), sent as the region-qualified BCP-47 tag Plex Web uses (`ja` → `ja-JP`) so it displays correctly in the per-item Settings dialog. This is applied once per show and account and cached in `rating_cache.json` so you can override it inside Plex without it being reset on the next run. Use `--overwrite-language` to force a re-apply.

The audio language preference is per-Plex-account, so if `PLEX_ALT_TOKEN` and/or `PLEX_ALT2_TOKEN` are set the same language is applied for those accounts — or a different one per account via `alt-language` / `alt2-language` in `manual_data.json`. Ratings, lookups, and library refreshes still use `PLEX_TOKEN` only. An alt account that fails (for example, a show it cannot see) is reported alongside the result as `alt: ...` / `alt2: ...` and does not fail the run. It isn't recorded as applied, so the next run that reaches the show retries that account alone, and reports it again if it still fails.

The cached "already applied" value records the language each account received (`ja-JP|alt=ja-JP|alt2=en-US`). An account missing from it, or whose language has since changed, is applied on the next run that reaches the show; the others are left alone. Adding a token applies only the new account. The log line (`--language-only`: the `[UPDATED]` line) reports the language every account should have.

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
    "language": "ko",
    "overrides": {
      "S02": { "id": "246862", "map_season": 1 }
    },
    "mapping": {
      "S01E03": [1, 3],
      "S01E05": [1, 5],
      "S01E18": [1, 18, 20, 22]
    },
    "dates": {
      "S03E01": "2024-01-07",
      "S03E02": "2024-01-14"
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
- `dates` — explicit air dates (YYYY-MM-DD) for regular `S##E##` episodes keyed
  by episode code (e.g. `"S03E01": "2024-01-07"`). Overrides the TMDB air date
  — use this for episodes that are missing from TMDB or have an incorrect date.
  Both the MKV `DATE` tag and filesystem timestamps are set from this value.
  When Plex is configured, the `Originally Available` date for the matching
  episode is also set and locked in Plex.
- `title_overrides` — declare the correct filesystem-safe title for an episode
  when it cannot be derived from TMDB (e.g. DVD releases that combine two OVA
  episodes). `--title-check` compares against this value instead of fetching
  TMDB. Use the same lookalike characters as your filenames (`╱` for `/`, etc.).
  When Plex is configured, the `Title` and `Sort Title` are also set and locked
  in Plex (using the decoded ASCII title) for any episode that also has a
  `dates` entry.
- `extras` — metadata for non-episode files (bonus content, OPs/EDs, etc.)
  that have no `S##E##` in their name. The key is the filename **without the
  `.mkv` extension**.
  - `date` — YYYY-MM-DD release date written to MKV tags and filesystem
    timestamps. Omit when using `episode`.
  - `season` — selects the season poster to attach.
  - `episode` — optional. When set, fetches the air date, title, and summary
    from that TMDB episode (season × episode) instead of using a manual `date`.
- `language` — ISO 639-1 code (e.g. `"ko"`, `"zh"`) that overrides the TMDB
  `original_language` when setting the show's preferred audio language in Plex.
  Use this when TMDB reports the wrong original language for a show.
- `alt-language` / `alt2-language` — ISO 639-1 code applied instead of
  `language` for the `PLEX_ALT_TOKEN` and `PLEX_ALT2_TOKEN` accounts, for when
  those users prefer a different audio language (e.g. `"language": "ja"` with
  `"alt-language": "en"` for a dubbed viewer). An account with no override gets
  `language`. Changing any of the three re-applies the preference on the next
  run.
- `deband` — show-level boolean. When `true`, writes a `DEBAND=true` MKV tag
  to every episode in the show so that mpv (or another player) can apply
  debanding automatically. Omitting or setting to `false` removes the tag.
- `deband_seasons` — season-level map (e.g. `"S01": true`). Overrides the
  show-level `deband` flag for specific seasons.
- `deband_episodes` — episode-level map (e.g. `"S01E26": true`). Overrides
  both `deband` and `deband_seasons` for individual episodes. Priority order:
  episode → season → show.

  If the filename ends with an OP/ED/NCOP/NCED suffix (with optional number,
  e.g. `NCOP1`, `ED2`), it is automatically detected and the MKV title becomes
  the episode title (or filename base) with the suffix appended in parentheses,
  e.g. `"The Kick-Off (NCOP)"` or `"Show Name - S02 (ED)"`.

See [mkv-tvshow/manual_data.json.example](mkv-tvshow/manual_data.json.example) for a full example.

### Deband tag update (`--deband-only`)

Run from the media directory to update only the `DEBAND` MKV tag for files
that have an explicit deband configuration in `manual_data.json`:

```sh
mkv-tvshow --deband-only
```

- Only files whose show/season/episode has a `deband`, `deband_seasons`, or
  `deband_episodes` entry are touched — unconfigured files are skipped entirely.
- All other existing tags (DATE, YEAR, SUMMARY, TMDB_ID, LAW_RATING, etc.) are
  preserved by extracting the current tag XML with `mkvextract`, splicing in or
  out the DEBAND element, and writing the result back.
- File timestamps are restored after editing so the cutoff filter in normal mode
  is unaffected.
- Requires `mkvextract` to be in PATH (ships with MKVToolNix alongside
  `mkvpropedit`).

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
sw-update check -v           # Also list apps that are up to date (--verbose)
sw-update check <name>       # Check a specific app
sw-update list               # List all apps and their known versions
sw-update install <name>     # Download latest release and run/open installer
sw-update update <name>      # Alias for install; runs the app's update command if configured
sw-update add                # Add a new app interactively
sw-update remove <name>      # Remove an app from config
sw-update enable <name>      # Enable an app for checking
sw-update disable <name>     # Disable an app without removing it
sw-update mark <name>        # Mark current latest as seen (skip next update prompt)
sw-update import <file>      # Bootstrap config from a Netscape bookmarks HTML file
sw-update clean              # Remove state entries for apps no longer in the config
```

### Environment

| Variable | Description |
|----------|-------------|
| `GITHUB_PERSONAL_ACCESS_TOKEN` | Personal access token — raises GitHub API limit from 60 to 5000 req/hour |

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
| `version_regex` | Go regex; when set, the version is taken from the first matched asset's filename (capture group 1) instead of the tag — for projects that tag by date but version the asset |
| `version_date_layout` | `[parse-layout, format-layout]` Go time layouts; parses the version as a date and reformats it (e.g. normalise a build date to a sortable `20060102`) |
| `version_rewrite` | `[regex, replacement]` applied to the tag name (e.g. rewrite `gs10071` to `10.07.1`) |
| `version_compare` | `"hash"` to compare against the local detector's version as a git short-hash prefix instead of ordinal segments — for projects whose release/asset date drifts from the build date embedded in the binary (e.g. mpv, see below) |

**`html` checker fields:**

| Field | Description |
|-------|-------------|
| `url` | Page URL to fetch |
| `version_regex` | Go regex; capture group 1 is used as version if present |
| `asset_patterns` | Glob/regex patterns matched against `href` filenames on the page |
| `asset_page` | Separate page to scrape for assets, when `url` is a bare version endpoint with no download links (e.g. gyan.dev's `release-version`) |
| `asset_sort` | Sort matched assets `name`/`name_desc`, then take the first (e.g. pick the newest version when a page lists several) |

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
| `filename` | Globs a path pattern and extracts version from the matched filename |

#### Asset downloads

Downloads send a `sw-update/1.0` `User-Agent` and a `Referer` (the app's `url`
when it shares the asset's host, otherwise the asset's own origin) — Cloudflare
front ends 403 both Go's default user agent and a spoofed browser one.
Each asset is tried up to three times with a short backoff on transient
failures (403, 408, 429, 5xx, and connection errors); a 404 or 401 fails
immediately. A web page served in place of the file (SourceForge `/download`
links) is followed through its `<meta http-equiv="refresh">`, up to three
times; a page without one fails immediately rather than being saved as the asset.
Downloads are https only: an `http://` asset URL, redirect or refresh target
fails. So are checks: an `http://` checker `url` or `asset_page`, a redirect
to `http://`, or any `http://` asset in the result fails the app's check.
A GitHub asset with a published SHA-256 digest (assets uploaded since
mid-2025) is checked against it, and a mismatching download is discarded;
other assets are not checked.

At the "Run/open downloaded file(s)?" prompt, `.exe`/`.msi` assets launch
elevated; `.zip` and `.7z` assets are extracted next to the download and the
folder opened. A password-protected archive needs an app-level
`archive_password` (ZipCrypto zips and AES 7z; AES-encrypted zips are not
supported). If an asset fails to download, or running or extracting fails, the
"Mark version as installed/seen?" prompt defaults to No, and `install` exits
non-zero once the prompts are done. Prompts take `y`/`yes` or `n`/`no`; any
other answer, or the end of input, counts as No.

```json
{
    "name": "NirLauncher",
    "archive_password": "nirsoft9876$",
    ...
}
```

#### Custom update commands

By default `install`/`update` downloads a release asset and runs it. Apps that
self-update or are managed by a package manager can instead define an `update`
block, which `install`/`update` runs (after confirmation) in place of the
download flow:

| Field | Description |
|-------|-------------|
| `command` | Executable or script to run. `.bat`/`.cmd` scripts are automatically run through `cmd.exe` |
| `args` | Arguments passed to the command |
| `elevated` | `true` to launch with a UAC elevation prompt — needed when the tool writes to `Program Files` (e.g. `yt-dlp.exe -U`, an mpv `updater.bat`) |

Examples: `yt-dlp.exe -U` (elevated), `pip install --upgrade <pkg>`,
`cmd /c pipx upgrade <pkg>`. Non-elevated commands stream their output inline;
elevated ones run in a separate administrator window.

#### Local detector fields

The `exe`, `file-version`, and `filename` detectors accept `version_regex`
(capture group 1 is the version), then optionally `version_date_layout`
(`[parse-layout, format-layout]`) and `version_rewrite` (`[regex, replacement]`)
to normalise the result so it matches the upstream version string.

mpv is a case where the release date can't be used at all: shinchiro's
winbuild releases are dated by when they're packaged, not when mpv itself was
compiled, so the "built on" date `mpv.exe --version` reports routinely trails
the release tag by a day or two even when the local build is current. mpv's
`--version` output also includes its git short-hash (`v0.41.0-1050-ge76a35ec9`),
which is stable per-commit, so mpv's config compares on that instead:

```json
"checker": {
    "type": "github",
    "repo": "shinchiro/mpv-winbuild-cmake",
    "asset_patterns": ["mpv-x86_64-v3-.*\\.7z"],
    "version_regex": "git-([0-9a-f]+)\\.7z",
    "version_compare": "hash"
},
"local": {
    "type": "exe",
    "path": "C:\\Program Files\\mpv\\mpv.exe",
    "args": ["--version"],
    "version_regex": "\\bg([0-9a-f]{7,})\\b"
}
```

Since the asset filename's hash and the binary's `git describe` hash aren't
always abbreviated to the same length, `version_compare: "hash"` matches them
as equal when one is a prefix of the other, rather than requiring an exact
string match.

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

## License

[GPL-3.0-or-later](LICENSE)
