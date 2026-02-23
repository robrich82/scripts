.PHONY: all clean mkv-audit mkv-movie mkv-musicvideo mkv-tvshow test

PYINSTALLER = pyinstaller
PYINSTALLER_FLAGS = --onefile --hidden-import="win32timezone"

all: mkv-audit mkv-movie mkv-musicvideo mkv-tvshow

mkv-audit:
	$(PYINSTALLER) $(PYINSTALLER_FLAGS) mkv-audit.py

mkv-movie:
	$(PYINSTALLER) $(PYINSTALLER_FLAGS) mkv-movie.py

mkv-musicvideo:
	$(PYINSTALLER) $(PYINSTALLER_FLAGS) mkv-musicvideo.py

mkv-tvshow:
	cd mkv-tvshow && go build -o ../dist/mkv-tvshow.exe .

test:
	cd mkv-tvshow && go test ./...

clean:
	rmdir /s /q dist build 2>nul || true
	del /q *.spec 2>nul || true
