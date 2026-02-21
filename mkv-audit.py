import subprocess
import json
import os
import sys

# Encoding safety for Windows terminal (handles JPN/KOR/Special characters)
if sys.stdout.encoding != 'utf-8':
    import io
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding='utf-8')

TARGET_LANGS = {'eng', 'jpn', 'kor'}

# Keywords that suggest a track is NOT the primary dialogue track
FORBIDDEN_KEYWORDS = [
    'commentary', 'sdh', 'description', 'cc', 
    'british', 'canadian', 'australian', 'regional'
]

# Keywords specifically indicating the Signs/Songs/Forced track
FORCED_KEYWORDS = ['signs', 'songs', 'forced']

# --- NEW PRIORITY MAPS ---
# Audio: Bitstream Lossless > PCM Lossless > High-Bitrate Lossy
CODEC_PRIORITY_AUDIO = {
    'A_TRUEHD': 10,          # Potential Dolby Atmos
    'A_DTS/LOSSLESS': 10,    # DTS-HD Master Audio (Potential DTS:X)
    'A_FLAC': 9,             # Pure Lossless PCM
    'A_PCM/INT/LIT': 8,      # Uncompressed LPCM (Newly Optimized)
    'A_OPUS': 6,             # Highly efficient modern lossy (Newly Optimized)
    'A_DTS': 5,              # Standard DTS
    'A_EAC3': 4,             # Dolby Digital Plus (E-AC3)
    'A_AC3': 3,              # Dolby Digital (AC3)
    'A_AAC': 2,              # AAC
    'A_VORBIS': 1            # Vorbis
}

# Subtitles: Stylized (Anime) > HD Image > Universal Text
SUB_PRIORITY = {
    'S_TEXT/ASS': 10,        # Advanced Substation Alpha
    'S_TEXT/SSA': 10,        # Substation Alpha
    'S_HDMV/PGS': 8,         # Blu-ray quality images
    'S_TEXT/WEBVTT': 6,      # Web Video Text Track (modern text format)
    'S_TEXT/UTF8': 5,        # Standard SRT (SubRip)
    'S_TEXT/ASCII': 5,       # Plain text
    'S_DVBSUB': 3,           # DVB Subtitles (broadcast quality images)
    'S_VOBSUB': 3,           # DVD Subtitles (image-based)
}

stats = {
    "scanned": 0,
    "fixed_files": 0,
    "failed_files": 0,
    "lang_fixes": 0,
    "video_inherits": 0,
    "flag_fixes": 0,
    "names_added": 0,
    "failed_list": []
}

unknown_codecs = set()

def normalize_language(code):
    """Aggressively maps regional codes (en-US, ja_JP) to 3-letter ISO codes."""
    if not code: return 'und'
    c = code.lower().replace('_', '-')
    if c.startswith('en') or c == 'eng': return 'eng'
    if c.startswith('ja') or c.startswith('jp') or c in ['jap', 'japn']: return 'jpn'
    if c.startswith('ko') or c.startswith('kr') or c == 'kor': return 'kor'
    return c

def get_ietf_language(iso_code):
    """Convert ISO 639-2 code to IETF/BCP-47 code."""
    mapping = {'eng': 'en', 'jpn': 'ja', 'kor': 'ko', 'und': 'und'}
    return mapping.get(iso_code, iso_code)

def get_mkv_metadata(file_path):
    try:
        result = subprocess.run(
            ['mkvmerge', '--identify', '--identification-format', 'json', file_path],
            capture_output=True, text=True, check=True, encoding='utf-8'
        )
        return json.loads(result.stdout)
    except Exception:
        return None

def process_file(file_path):
    data = get_mkv_metadata(file_path)
    if not data: return []
    
    all_tracks = data.get('tracks', [])
    edits = []
    und_report = []
    local_fixes = {"lang": 0, "video": 0, "flags": 0, "names": 0}
    file_failed = False
    fail_reasons = []

    # --- 1. LANGUAGE NORMALIZATION ---
    for t in all_tracks:
        props = t['properties']
        curr_lang = props.get('language', 'und').lower()
        norm = normalize_language(curr_lang)
        
        # Also check the IETF language code and normalize it
        curr_ietf = props.get('language_ietf', '').lower()
        norm_ietf = normalize_language(curr_ietf) if curr_ietf else norm
        
        # Set both to canonical values if either is incorrect
        if curr_lang != norm or curr_ietf != get_ietf_language(norm):
            # Set both the legacy language (ISO 639-2) and the modern IETF tag
            edits.extend([
                '--edit', f"track:{t['id']+1}", 
                '--set', f"language={norm}",
                '--set', f"language-ietf={get_ietf_language(norm)}"
            ])
            props['language'] = norm 
            local_fixes["lang"] += 1

    # --- 2. VIDEO INHERITANCE ---
    video_tracks = [t for t in all_tracks if t['type'] == 'video']
    audio_tracks = [t for t in all_tracks if t['type'] == 'audio']
    if video_tracks and audio_tracks:
        first_audio_lang = audio_tracks[0]['properties'].get('language', 'und')
        for v in video_tracks:
            if v['properties'].get('language', 'und') == 'und' and first_audio_lang != 'und':
                edits.extend(['--edit', f"track:{v['id']+1}", '--set', f"language={first_audio_lang}"])
                v['properties']['language'] = first_audio_lang
                local_fixes["video"] += 1

    # --- 3. FLAG & NAMING LOGIC ---
    for lang in TARGET_LANGS:
        # Audio Tie-Breaker (Channels > Codec Quality)
        lang_audios = [t for t in audio_tracks if normalize_language(t['properties'].get('language', 'und')) == lang]
        if lang_audios:
            for t in lang_audios:
                cid = t['properties'].get('codec_id', '')
                if cid not in CODEC_PRIORITY_AUDIO: unknown_codecs.add(f"Audio: {cid}")

            clean_audio = [t for t in lang_audios if not any(k in t['properties'].get('track_name', '').lower() for k in FORBIDDEN_KEYWORDS)]
            candidates = clean_audio if clean_audio else lang_audios
            
            winner = max(candidates, key=lambda x: (
                x['properties'].get('audio_channels', 0),
                CODEC_PRIORITY_AUDIO.get(x['properties'].get('codec_id', ''), 0)
            ))

            for t in lang_audios:
                target_def = 1 if t['id'] == winner['id'] else 0
                if t['properties'].get('default_track', 0) != target_def:
                    edits.extend(['--edit', f"track:{t['id']+1}", '--set', f"flag-default={target_def}"])
                    local_fixes["flags"] += 1

        # Subtitle Logic (Main vs Forced + Format Quality)
        sub_tracks = [t for t in all_tracks if t['type'] == 'subtitles' and normalize_language(t['properties'].get('language', 'und')) == lang]
        if sub_tracks:
            for t in sub_tracks:
                cid = t['properties'].get('codec_id', '')
                if cid not in SUB_PRIORITY: unknown_codecs.add(f"Subtitle: {cid}")

            main_cands = []
            forced_cands = []

            for s in sub_tracks:
                name = s['properties'].get('track_name', '').lower()
                if any(k in name for k in FORCED_KEYWORDS):
                    forced_cands.append(s)
                elif not any(k in name for k in FORBIDDEN_KEYWORDS):
                    main_cands.append(s)

            if main_cands:
                # Winner is based on Subtitle Format Quality (ASS > PGS > SRT)
                winner_main = max(main_cands, key=lambda x: SUB_PRIORITY.get(x['properties'].get('codec_id', ''), 0))
                
                target_default = 1
                target_forced = 0
                current_default = winner_main['properties'].get('default_track', 0)
                current_forced = winner_main['properties'].get('forced_track', 0)
                
                if current_default != target_default or current_forced != target_forced:
                    edits.extend(['--edit', f"track:{winner_main['id']+1}", '--set', 'flag-default=1', '--set', 'flag-forced=0'])
                    local_fixes["flags"] += 1

                for s in sub_tracks:
                    if s['id'] != winner_main['id'] and s not in forced_cands:
                        if s['properties'].get('default_track'):
                            edits.extend(['--edit', f"track:{s['id']+1}", '--set', 'flag-default=0'])
                            local_fixes["flags"] += 1
            elif forced_cands:
                # No main candidates, but we have Signs/Songs tracks
                # Use the default one as main, or the first one if none are marked default
                default_in_forced = [t for t in forced_cands if t['properties'].get('default_track')]
                if default_in_forced:
                    winner_main = default_in_forced[0]
                else:
                    winner_main = forced_cands[0]
                
                # Set this one as default=1, forced=0 (it's the main track now)
                current_default = winner_main['properties'].get('default_track', 0)
                current_forced = winner_main['properties'].get('forced_track', 0)
                
                if current_default != 1 or current_forced != 0:
                    edits.extend(['--edit', f"track:{winner_main['id']+1}", '--set', 'flag-default=1', '--set', 'flag-forced=0'])
                    local_fixes["flags"] += 1
                
                # Other Signs/Songs should be default=0, forced=0
                for s in forced_cands:
                    if s['id'] != winner_main['id']:
                        current_default = s['properties'].get('default_track', 0)
                        current_forced = s['properties'].get('forced_track', 0)
                        if current_default != 0 or current_forced != 0:
                            edits.extend(['--edit', f"track:{s['id']+1}", '--set', 'flag-default=0', '--set', 'flag-forced=0'])
                            local_fixes["flags"] += 1
            elif len(sub_tracks) > 1:
                # No explicit main or forced candidates — fall back to codec priority
                # Prefer higher-quality subtitle formats (ASS/SSA over PGS/SRT)
                winner_main = max(
                    sub_tracks,
                    key=lambda x: SUB_PRIORITY.get(x['properties'].get('codec_id', ''), 0)
                )

                highest_score = SUB_PRIORITY.get(winner_main['properties'].get('codec_id', ''), 0)
                # If we cannot determine a winner by codec priority, mark as failed
                if highest_score == 0:
                    file_failed = True
                    fail_reasons.append(f"No clear {lang} main track")
                else:
                    # Set winner as default main (default=1, forced=0)
                    current_default = winner_main['properties'].get('default_track', 0)
                    current_forced = winner_main['properties'].get('forced_track', 0)

                    if current_default != 1 or current_forced != 0:
                        edits.extend(['--edit', f"track:{winner_main['id']+1}", '--set', 'flag-default=1', '--set', 'flag-forced=0'])
                        local_fixes["flags"] += 1

                    # Clear default flag on all other subtitle tracks
                    for s in sub_tracks:
                        if s['id'] != winner_main['id']:
                            if s['properties'].get('default_track'):
                                edits.extend(['--edit', f"track:{s['id']+1}", '--set', 'flag-default=0'])
                                local_fixes["flags"] += 1

            if len(forced_cands) == 1:
                f_t = forced_cands[0]
                is_only_sub = 1 if len(sub_tracks) == 1 else 0
                
                current_default = f_t['properties'].get('default_track', 0)
                current_forced = f_t['properties'].get('forced_track', 0)
                
                if current_default != is_only_sub or current_forced != 1:
                    edits.extend(['--edit', f"track:{f_t['id']+1}", '--set', f'flag-default={is_only_sub}', '--set', 'flag-forced=1'])
                    local_fixes["flags"] += 1
                if not f_t['properties'].get('track_name'):
                    edits.extend(['--edit', f"track:{f_t['id']+1}", '--set', 'name=Signs/Songs'])
                    local_fixes["names"] += 1
            elif len(forced_cands) > 1 and main_cands:
                # Multiple Signs/Songs tracks AND we have main tracks - make the default one the forced track, others get cleared
                default_forced = [t for t in forced_cands if t['properties'].get('default_track')]
                if default_forced:
                    f_t = default_forced[0]
                    # Keep this one as default=1, forced=1
                    current_default = f_t['properties'].get('default_track', 0)
                    current_forced = f_t['properties'].get('forced_track', 0)
                    
                    if current_default != 1 or current_forced != 1:
                        edits.extend(['--edit', f"track:{f_t['id']+1}", '--set', 'flag-default=1', '--set', 'flag-forced=1'])
                        local_fixes["flags"] += 1
                    
                    # Other Signs/Songs tracks should be default=0, forced=0
                    for other in forced_cands:
                        if other['id'] != f_t['id']:
                            current_default = other['properties'].get('default_track', 0)
                            current_forced = other['properties'].get('forced_track', 0)
                            if current_default != 0 or current_forced != 0:
                                edits.extend(['--edit', f"track:{other['id']+1}", '--set', 'flag-default=0', '--set', 'flag-forced=0'])
                                local_fixes["flags"] += 1

    # --- 4. EXECUTION ---
    if file_failed:
        print(f"[FAILED] {os.path.basename(file_path)}: {', '.join(fail_reasons)}")
        stats["failed_files"] += 1
        stats["failed_list"].append(file_path)
    elif edits:
        try:
            subprocess.run(['mkvpropedit', file_path] + edits, check=True, capture_output=True)
            # Verify changes were actually made
            new_data = get_mkv_metadata(file_path)
            if new_data and new_data != data:
                print(f"[FIXED]  {os.path.basename(file_path)}")
                stats["fixed_files"] += 1
                stats["lang_fixes"] += local_fixes["lang"]
                stats["video_inherits"] += local_fixes["video"]
                stats["flag_fixes"] += local_fixes["flags"]
                stats["names_added"] += local_fixes["names"]
            else:
                print(f"[OK]     {os.path.basename(file_path)} (no changes needed)")
        except Exception as e:
            print(f"[ERR]    {os.path.basename(file_path)}: {e}")
            stats["failed_files"] += 1
            stats["failed_list"].append(file_path)
    else:
        print(f"[OK]     {os.path.basename(file_path)} (no changes needed)")
    
    return und_report

def run_all(root_dir='.'):
    print(f"--- Running Library Architect v6.0 ---")
    
    failed_cache = 'failed_files.json'
    files_to_scan = []

    # Check for existing failure cache
    if os.path.exists(failed_cache):
        with open(failed_cache, 'r') as f:
            old_fails = json.load(f)
        if old_fails:
            choice = input(f"Found {len(old_fails)} previously failed files. Run only these? (y/n): ").lower()
            if choice == 'y':
                files_to_scan = old_fails

    if not files_to_scan:
        for root, dirs, files in os.walk(root_dir):
            for f in files:
                if f.lower().endswith(('.mkv', '.mka')):
                    files_to_scan.append(os.path.join(root, f))

    full_und_report = {}
    for path in files_to_scan:
        stats["scanned"] += 1
        issues = process_file(path)
        if issues: full_und_report[path] = issues

    # Final Summaries
    print("\n" + "="*40)
    print(f"{'FINAL SUMMARY':^40}")
    print("="*40)
    print(f"Total Scanned:          {stats['scanned']}")
    print(f"Total Fixed:            {stats['fixed_files']}")
    print(f"Total Failed/Skipped:   {stats['failed_files']}")
    print("-" * 40)
    print(f"Langs Normalized:       {stats['lang_fixes']}")
    print(f"Flag Rules Applied:     {stats['flag_fixes']}")
    print("="*40)

    # Save Cache & Reports
    if stats["failed_list"]:
        with open(failed_cache, 'w') as f:
            json.dump(stats["failed_list"], f, indent=4)

    if unknown_codecs:
        with open('unknown_codecs.json', 'w') as f:
            json.dump(list(unknown_codecs), f, indent=4)
        print(f"New formats found: unknown_codecs.json")

if __name__ == "__main__":
    run_all('.')
