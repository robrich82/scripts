import os
import re
import difflib

music_dir = os.getcwd()
video_exts = ('.mkv', '.mp4', '.avi', '.mov', '.vob')
audio_exts = ('.flac', '.mp3', '.m4a', '.wav', '.ogg', '.wma', '.mka', '.dsf')
video_suffixes = ['-behindthescenes', '-concert', '-interview', '-live', '-lyrics', '-video']

skipped_videos = [] # List to track what you skipped
ignore_patterns = [] # Patterns to ignore in filenames

# Load ignore patterns from file
ignore_file = os.path.join(music_dir, 'ignore_patterns.txt')
if os.path.exists(ignore_file):
    try:
        with open(ignore_file, 'r', encoding='utf-8') as f:
            ignore_patterns = [line.strip() for line in f if line.strip()]
    except UnicodeDecodeError:
        # Fall back to cp1252 for existing files
        with open(ignore_file, 'r', encoding='cp1252') as f:
            ignore_patterns = [line.strip() for line in f if line.strip()]

def remove_ignored_parts(filename, patterns):
    """Remove ignored patterns from filename"""
    result = filename
    for pattern in patterns:
        result = re.sub(re.escape(pattern), '', result)
    # Clean up double spaces
    result = re.sub(r'\s+', ' ', result).strip()
    return result

print(f"--- Scanning and Matching in: {music_dir} ---\n")

try:
    for root, dirs, files in os.walk(music_dir):
        audio_data = {}
        for f in files:
            if f.lower().endswith(audio_exts):
                base = os.path.splitext(f)[0]
                parts = base.split(' - ', 1)
                if len(parts) > 1:
                    # Store: "Track Name" -> "Track Name - Track Name"
                    audio_data[base] = f"{base} - {parts[1]}"

        for f in files:
            if f.lower().endswith(video_exts):
                video_full_path = os.path.join(root, f)
                video_base, video_ext = os.path.splitext(f)
                
                clean_video = video_base
                found_suffix = "" 
                for suffix in video_suffixes:
                    if suffix in video_base:
                        clean_video = video_base.rsplit(suffix, 1)[0]
                        found_suffix = suffix
                        break 
                
                # Apply ignore patterns
                clean_video_ignored = remove_ignored_parts(clean_video, ignore_patterns)
                
                # Extract track number from video name
                video_track = clean_video_ignored.split(' - ', 1)[0] if ' - ' in clean_video_ignored else None
                
                # Find closest match from audio file names
                # First, try to match by track number
                suggestions = []
                if video_track:
                    # Filter audio files with matching track number
                    matching_track = [name for name in audio_data.keys() if name.startswith(video_track)]
                    if matching_track:
                        suggestions = difflib.get_close_matches(clean_video_ignored, matching_track, n=1, cutoff=0.3)
                
                # If no track number match found, fall back to general similarity matching
                if not suggestions:
                    suggestions = difflib.get_close_matches(clean_video_ignored, audio_data.keys(), n=1, cutoff=0.3)
                
                if suggestions:
                    matched_base = audio_data[suggestions[0]]
                    target_name = matched_base + (found_suffix if found_suffix else '-video')
                    
                    # Compare against the cleaned video name, not the original with ignored parts
                    if target_name != clean_video_ignored + (found_suffix if found_suffix else '-video'):
                        print(f"\n[?] UNMATCHED VIDEO: {f}")
                        print(f"    SUGGESTED MATCH: {target_name}{video_ext}")
                        print(f"    Current: {clean_video}")
                        print(f"    Expected: {matched_base}")
                        
                        choice = input("    (r)ename, (i)gnore pattern, (s)kip: ").lower()
                        if choice == 'r':
                            new_path = os.path.join(root, target_name + video_ext)
                            try:
                                os.rename(video_full_path, new_path)
                                print(f"    [OK] Renamed to: {target_name}{video_ext}")
                            except Exception as e:
                                print(f"    [ERROR] Could not rename: {e}")
                        elif choice == 'i':
                            # Calculate the difference
                            diff = clean_video.replace(matched_base, '', 1).strip()
                            if not diff:
                                # Try removing from the end
                                if clean_video.startswith(matched_base):
                                    diff = clean_video[len(matched_base):].strip()
                            
                            pattern = input(f"    Enter the part to ignore (default: '{diff}'): ").strip()
                            if not pattern:
                                pattern = diff
                            
                            if pattern:
                                ignore_patterns.append(pattern)
                                # Save to file
                                with open(ignore_file, 'a', encoding='utf-8') as f:
                                    f.write(pattern + '\n')
                                print(f"    [OK] Added '{pattern}' to ignore list")
                                # Re-check now
                                clean_video_ignored = remove_ignored_parts(clean_video, ignore_patterns)
                                if clean_video_ignored == matched_base:
                                    print(f"    [OK] File now matches after ignoring pattern!")
                                else:
                                    print(f"    Still doesn't match after ignoring")
                        else:
                            print("    [SKIP] Kept original name.")
                            skipped_videos.append(video_full_path)
                else:
                    # Optional: track videos that didn't even get a suggestion
                    pass

except KeyboardInterrupt:
    print("\n\n[INTERRUPTED] Scan cancelled by user.")

print(f"\n--- Scan Complete ---")

# Print the final report of skipped items
if skipped_videos:
    print(f"\n--- SKIPPED VIDEOS ({len(skipped_videos)}) ---")
    for path in skipped_videos:
        print(path)
else:
    print("\nNo videos were skipped.")
