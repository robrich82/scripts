.PHONY: all clean mkv-audit mkv-movie mkv-musicvideo mkv-tvshow

PYINSTALLER = pyinstaller
PYINSTALLER_FLAGS = --onefile

all: mkv-audit mkv-movie mkv-musicvideo mkv-tvshow

mkv-audit:
	$(PYINSTALLER) $(PYINSTALLER_FLAGS) mkv-audit.py

mkv-movie:
	$(PYINSTALLER) $(PYINSTALLER_FLAGS) mkv-movie.py

mkv-musicvideo:
	$(PYINSTALLER) $(PYINSTALLER_FLAGS) mkv-musicvideo.py

mkv-tvshow:
	$(PYINSTALLER) $(PYINSTALLER_FLAGS) mkv-tvshow.py

clean:
	rmdir /s /q dist build 2>nul || true
	del /q *.spec 2>nul || true
