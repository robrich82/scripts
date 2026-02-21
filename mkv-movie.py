import os
import subprocess
import shutil
import re
from datetime import datetime
import win32file
import win32con
import pywintypes

# --- Configuration ---
# Ensure these are in your System PATH
MEDIAINFO = shutil.which("mediainfo")
MKV_PROP = shutil.which("mkvpropedit")

try:
    import win32file, win32con, pywintypes
    HAS_WIN32 = True
except ImportError:
    HAS_WIN32 = False
    print("! 'pywin32' not found. Only 'Date Modified' will be updated.")

def get_metadata_via_mediainfo(file_path):
    """
    Uses MediaInfo to grab the specific tags we need.
    Returns (best_date, encoded_date)
    """
    try:
        # We query specifically for DATE, YEAR, and the Encoded_Date header
        # Output format: DATE|YEAR|Encoded_Date
        query = "General;%DATE%|%YEAR%|%Encoded_Date%"
        result = subprocess.run(
            [MEDIAINFO, f"--Inform={query}", file_path],
            capture_output=True, text=True, encoding='utf-8'
        )
        
        parts = result.stdout.strip().split('|')
        tag_date = parts[0] if len(parts) > 0 and parts[0] else None
        tag_year = parts[1] if len(parts) > 1 and parts[1] else None
        enc_date = parts[2] if len(parts) > 2 and parts[2] else None

        # Clean Encoded_Date (Format: UTC 2016-09-12 04:35:11)
        header_date = None
        if enc_date:
            match = re.search(r'(\d{4}-\d{2}-\d{2})', enc_date)
            if match:
                header_date = match.group(1)

        # Priority: DATE tag > YEAR tag > Header
        best_date = tag_date or tag_year or header_date
        
        # Debug output removed - function returns chosen dates

        return best_date, header_date
    except Exception as e:
        print(f"  [!] MediaInfo Error: {e}")
        return None, None

def sync_mkv_header(file_path, date_str):
    if not MKV_PROP: return
    try:
        # Format for mkvpropedit: YYYY-MM-DD
        formatted_date = f"{date_str}-01-01" if len(date_str) == 4 else date_str
        subprocess.run([MKV_PROP, file_path, "--edit", "info", "--set", f"date={formatted_date}T00:00:00Z"], 
                       capture_output=True, check=True)
    except Exception as e:
        print(f"  [!] Failed to update internal header: {e}")

def set_creation_time(path, timestamp):
    if not HAS_WIN32: return
    try:
        handle = win32file.CreateFile(
            path, win32con.GENERIC_WRITE,
            win32con.FILE_SHARE_READ | win32con.FILE_SHARE_WRITE | win32con.FILE_SHARE_DELETE,
            None, win32con.OPEN_EXISTING, win32con.FILE_ATTRIBUTE_NORMAL, None
        )
        win32file.SetFileTime(handle, pywintypes.Time(timestamp), None, None)
        handle.close()
    except: pass

def main():
    if not MEDIAINFO:
        print("Error: 'mediainfo' CLI not found. Please install it and add to PATH.")
        return

    target_dir = os.getcwd()
    
    for root, _, files in os.walk(target_dir):
        for file in sorted(files):
            if not file.lower().endswith(".mkv"): continue
            file_path = os.path.join(root, file)
            
            best_date, header_date = get_metadata_via_mediainfo(file_path)
            basename = os.path.basename(file_path)

            if not best_date:
                print(f"[SKIP]   {basename} (no date found in metadata)")
                continue

            try:
                # Parse into datetime object
                if len(best_date) == 4:
                    dt = datetime(int(best_date), 1, 1)
                else:
                    # Handles YYYY-MM-DD
                    dt = datetime.strptime(best_date[:10], "%Y-%m-%d")

                ts = dt.timestamp()

                header_needs_update = (best_date != header_date)

                # Check current filesystem times and only update if necessary
                current_mtime = os.path.getmtime(file_path)
                mtime_needs_update = abs(current_mtime - ts) > 1

                ctime_needs_update = False
                if HAS_WIN32:
                    current_ctime = os.path.getctime(file_path)
                    ctime_needs_update = abs(current_ctime - ts) > 1

                if header_needs_update:
                    sync_mkv_header(file_path, best_date)

                if mtime_needs_update:
                    os.utime(file_path, (ts, ts))

                if ctime_needs_update:
                    set_creation_time(file_path, ts)

                if header_needs_update or mtime_needs_update or ctime_needs_update:
                    print(f"[UPDATED] {basename} -> {best_date}")
                else:
                    print(f"[OK]      {basename} (no changes needed)")

            except Exception as e:
                print(f"[ERR]     {basename}: {e}")

if __name__ == "__main__":
    main()
