import os
import re
import time
import json
import requests
import subprocess
import shutil
from datetime import datetime
import win32file
import win32con
import pywintypes

# --- Configuration ---
API_KEY = os.getenv('TMDB_API_KEY')
if not API_KEY:
    print("Error: TMDB_API_KEY environment variable not set")
    exit(1)
BASE_URL = 'https://api.themoviedb.org/3'
MEDIA_DIR = os.getcwd()
CACHE_FILE = 'show_cache.json'
CUTOFF_DATE = datetime(2026, 2, 22, 0, 0, 0) 

# Option to skip confirmation for cached shows
PROCESS_ALL = input("Process all cached shows without prompting? [y/n]: ").strip().lower() == 'y'

# --- Advanced Manual Mapping ---
MANUAL_DATA = {
    "Panty & Stocking with Garterbelt": {
        "overrides": {
            "S02": { 
                "id": "246862",    # New series TMDB ID
                "map_season": 1    # Map local S02 to TMDB S01
            }
        },
        "mapping": {
            "S00E01": (0, 1), "S01E01": (1, 1), "S01E03": (1, 3),
            "S01E05": (1, 5), "S01E07": (1, 7), "S01E09": (1, 9),
            "S01E11": (1, 11), "S01E12": (1, 12), "S01E14": (1, 14),
            "S01E16": (1, 16), "S01E18": (1, 18), "S01E21": (1, 23),
            "S01E23": (1, 25), "S01E25": (1, 27),
        },
    },
    "Full Metal Panic!": {
        "mapping": {
            "S02E01": (0, 1), "S02E02": (0, 2), "S02E03": (2, 2),
            "S02E04": (2, 3), "S02E05": (2, 5), "S02E06": (2, 4),
            "S02E07": (2, 6), "S02E08": (2, 7), "S02E09": (2, 8),
            "S02E10": (2, 9), "S02E11": (2, 10), "S02E12": (2, 11),
            "S02E14": (0, 3), "S03E14": (0, 4),
        }
    }
}

REVERSE_SAFE_CHARS = {'꞉': ':', '╱': '/', '╲': '\\', '│': '|', '＊': '*', '？': '?', "''": '"', '＜': '<', '＞': '>'}
MKV_TOOL = shutil.which("mkvpropedit")
session_skipped_shows = set()
session_confirmed_ids = {}

def clean_metadata_title(name_or_filename):
    name = os.path.splitext(name_or_filename)[0]
    for safe, standard in REVERSE_SAFE_CHARS.items():
        name = name.replace(safe, standard)
    return name

def load_cache():
    if os.path.exists(CACHE_FILE):
        with open(CACHE_FILE, 'r') as f: return json.load(f)
    return {}

def save_cache(cache):
    with open(CACHE_FILE, 'w') as f: json.dump(cache, f, indent=4)

confirmed_shows = load_cache()
stats = {"success": 0, "failed": 0, "skipped_old": 0}

def get_show_id_interactively(show_folder):
    if show_folder in session_skipped_shows: return "SKIP"
    if show_folder in session_confirmed_ids: return session_confirmed_ids[show_folder]
    if show_folder in confirmed_shows:
        if not PROCESS_ALL:
            val = input(f"\nShow '{show_folder}' found in cache. Process? [y/n]: ").strip().lower()
            if val == 'n':
                session_skipped_shows.add(show_folder)
                return "SKIP"
        res_id = confirmed_shows[show_folder]
        session_confirmed_ids[show_folder] = res_id
        return res_id

    search_query = clean_metadata_title(re.sub(r'\s\(\d{4}\)', '', show_folder).strip('【】 '))
    print(f"\n--- New Show Found: {show_folder} ---")
    try:
        res = requests.get(f"{BASE_URL}/search/tv", params={"api_key": API_KEY, "query": search_query})
        results = res.json().get('results', [])
        if not results:
            val = input(f"No results. Enter TMDB ID or 's' to skip: ")
            res_id = None if val.lower() == 's' else val
        else:
            best = results[0]
            print(f"Match: {best['name']} ({best.get('first_air_date', 'N/A')})")
            val = input("Correct? [y/n] or enter TMDB ID: ").strip().lower()
            res_id = best['id'] if val in ['y', ''] else (None if val in ['n', 's'] else val)
        if res_id:
            confirmed_shows[show_folder] = res_id
            session_confirmed_ids[show_folder] = res_id
            save_cache(confirmed_shows)
            return res_id
    except: pass
    session_skipped_shows.add(show_folder)
    return "SKIP"

def apply_mkv(file_path, metadata, date_str, poster_url):
    # Ensure mkvpropedit is available
    if not MKV_TOOL:
        print("  [!] mkvpropedit not found in PATH")
        return False

    # Base command for mkvpropedit
    cmd = [MKV_TOOL, file_path, '--edit', 'info', '--set', f"title={metadata.get('title', '')}"]

    # 1. Update Internal MKV Header Date
    # mkvpropedit expects: YYYY-MM-DDTHH:MM:SSZ
    if date_str:
        cmd.extend(['--set', f'date={date_str}'])

    tag_elements = []
    if date_str:
        tag_elements.append(f"<Simple><Name>DATE</Name><String>{date_str.split('T')[0]}</String></Simple>")
        tag_elements.append(f"<Simple><Name>YEAR</Name><String>{date_str.split('-')[0]}</String></Simple>")

    if metadata.get('summary'):
        tag_elements.append(f"<Simple><Name>SUMMARY</Name><String>{metadata.get('summary', '')}</String></Simple>")
    if metadata.get('tmdb_id'):
        tag_elements.append(f"<Simple><Name>TMDB_ID</Name><String>{metadata.get('tmdb_id', '')}</String></Simple>")

    if tag_elements:
        tag_xml = f"<Tags><Tag><Targets><TargetTypeValue>50</TargetTypeValue></Targets>{''.join(tag_elements)}</Tag></Tags>"
        with open("temp_tags.xml", "w", encoding="utf-8") as f: f.write(tag_xml)
        cmd.extend(['--tags', 'all:temp_tags.xml'])

    # Attachment handling (Posters)
    img_temp = "temp_poster.jpg"
    # Probe existing attachments with mkvmerge to avoid "No attachment matched" warnings
    mkvmerge = shutil.which("mkvmerge")
    delete_args = []
    if mkvmerge:
        try:
            probe = subprocess.run([mkvmerge, '-i', file_path], capture_output=True, text=True, encoding='utf-8')
            probe_out = (probe.stdout or '') + '\n' + (probe.stderr or '')
            if 'image/jpeg' in probe_out or "type 'image/jpeg'" in probe_out:
                delete_args.extend(['--delete-attachment', 'mime-type:image/jpeg'])
            if 'image/png' in probe_out or "type 'image/png'" in probe_out:
                delete_args.extend(['--delete-attachment', 'mime-type:image/png'])
        except Exception:
            # If probing fails, fall back to the safer default of not issuing delete args
            delete_args = []
    else:
        # If mkvmerge isn't available, avoid delete args to prevent warnings
        delete_args = []

    if delete_args:
        cmd.extend(delete_args)

    if poster_url:
        try:
            r = requests.get(poster_url)
            if r.status_code == 200:
                with open(img_temp, 'wb') as f: f.write(r.content)
                cmd.extend(['--attachment-name', 'cover.jpg', '--attachment-mime-type', 'image/jpeg', '--add-attachment', img_temp])
        except Exception:
            pass

    # Execute MKVPropEdit
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, encoding='utf-8')
    except Exception as e:
        # Likely a problem invoking mkvpropedit
        print(f"  [!] subprocess error: {e}")
        if os.path.exists(img_temp): os.remove(img_temp)
        if os.path.exists("temp_tags.xml"): os.remove("temp_tags.xml")
        return False

    # Clean up temp files
    if os.path.exists(img_temp): os.remove(img_temp)
    if os.path.exists("temp_tags.xml"): os.remove("temp_tags.xml")

    # Consider the command successful if mkvpropedit exited with code 0
    # or if its output explicitly states the changes were written.
    combined = (result.stdout or "") + "\n" + (result.stderr or "")
    success_msg = "The changes are written to the file. Done."
    success = (result.returncode == 0) or (success_msg in combined)

    if not success:
        out = (result.stdout or "").strip()
        err = (result.stderr or "").strip()
        print(f"  [!] mkvpropedit failed (rc={result.returncode})\n{out}\n{err}")
    else:
        # If mkvpropedit printed success but returned non-zero, note it for debugging
        if result.returncode != 0:
            print(f"  [i] mkvpropedit returned rc={result.returncode} but reported success.\n{combined}")

    # 2. Update File System Timestamps (Modified & Created)
    if success and date_str:
        try:
            dt = datetime.strptime(date_str.split('T')[0], '%Y-%m-%d')
            ts = dt.timestamp()
            win_time = pywintypes.Time(dt)

            # Set Modified and Accessed
            os.utime(file_path, (ts, ts))

            # Set Created Date via pywin32
            handle = win32file.CreateFile(
                file_path,
                win32con.GENERIC_WRITE,
                win32con.FILE_SHARE_READ | win32con.FILE_SHARE_WRITE | win32con.FILE_SHARE_DELETE,
                None,
                win32con.OPEN_EXISTING,
                win32con.FILE_ATTRIBUTE_NORMAL,
                None
            )
            # SetFileTime(handle, createTime, accessTime, writeTime)
            win32file.SetFileTime(handle, win_time, None, None)
            handle.close()

        except Exception as e:
            print(f"  [!] Timestamp Error: {e}")

    return success

print(f"Scanning {MEDIA_DIR}...")
for show_folder in sorted(os.listdir(MEDIA_DIR)):
    show_path = os.path.join(MEDIA_DIR, show_folder)
    if not os.path.isdir(show_path): continue
    show_id = get_show_id_interactively(show_folder)
    if show_id == "SKIP": continue

    main_poster = None
    try:
        show_data = requests.get(f"{BASE_URL}/tv/{show_id}", params={"api_key": API_KEY}).json()
        if show_data.get('poster_path'): main_poster = f"https://image.tmdb.org/t/p/original{show_data['poster_path']}"
    except: pass

    for root, _, files in os.walk(show_path):
        season_cache = {}
        for file in sorted(files):
            if not file.endswith(".mkv"): continue
            file_path = os.path.join(root, file)
            if datetime.fromtimestamp(os.path.getmtime(file_path)) < CUTOFF_DATE:
                stats["skipped_old"] += 1
                continue

            # Plex Multi-episode Renaming
            m_full = re.search(r'([Ss]\d+)((?:[Ee]\d+){2,})', file)
            if m_full:
                e_nums = re.findall(r'[Ee](\d+)', m_full.group(2))
                if len(e_nums) > 2:
                    new_filename = file.replace(m_full.group(2), f"E{e_nums[0]}-E{e_nums[-1]}")
                    os.rename(file_path, os.path.join(root, new_filename))
                    file, file_path = new_filename, os.path.join(root, new_filename)

            m = re.search(r'S(\d+)E(\d+)', file, re.IGNORECASE)
            if not m: continue
            s_num, e_num = int(m.group(1)), int(m.group(2))
            
            curr_id, t_s, t_e = show_id, s_num, e_num
            if show_folder in MANUAL_DATA:
                entry = MANUAL_DATA[show_folder]
                s_key = f"S{s_num:02}"
                if s_key in entry.get("overrides", {}):
                    ovr = entry["overrides"][s_key]
                    curr_id, t_s = ovr.get("id", curr_id), ovr.get("map_season", t_s)
                if f"{s_key}E{e_num:02}" in entry.get("mapping", {}):
                    t_s, t_e = entry["mapping"][f"{s_key}E{e_num:02}"]

            rel_date, p_use, summ = None, None, ""
            try:
                c_key = f"{curr_id}_S{t_s}"
                if c_key not in season_cache:
                    season_cache[c_key] = requests.get(f"{BASE_URL}/tv/{curr_id}/season/{t_s}", params={"api_key": API_KEY}).json()
                s_res = season_cache[c_key]
                p_use = f"https://image.tmdb.org/t/p/original{s_res['poster_path']}" if s_res.get('poster_path') else main_poster
                ep = next((e for e in s_res.get('episodes', []) if e['episode_number'] == t_e), None)
                if ep:
                    summ = ep.get('overview', '')
                    if ep.get('air_date'): rel_date = f"{ep['air_date']}T00:00:00Z"
            except: pass

            meta = {"title": clean_metadata_title(file), "summary": summ, "tmdb_id": str(curr_id)}
            if apply_mkv(file_path, meta, rel_date, p_use):
                print(f"[✓] Processed: {file}")
                stats["success"] += 1
            else: print(f"  [X] Failed: {file}")
            time.sleep(0.05)
