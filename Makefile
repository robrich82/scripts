.PHONY: all clean mkv-audit mkv-movie mkv-musicvideo mkv-tvshow sw-update book-tracker test vet

all: mkv-audit mkv-movie mkv-musicvideo mkv-tvshow sw-update book-tracker

mkv-audit:
	cd mkv-audit && go build -o ../dist/mkv-audit.exe .

mkv-movie:
	cd mkv-movie && go build -o ../dist/mkv-movie.exe .

mkv-musicvideo:
	cd mkv-musicvideo && go build -o ../dist/mkv-musicvideo.exe .

mkv-tvshow:
	cd mkv-tvshow && go build -o ../dist/mkv-tvshow.exe .

sw-update:
	cd sw-update && go build -o ../dist/sw-update.exe .

book-tracker:
	cd book-tracker && go build -o ../dist/book-tracker.exe .

test:
	cd mkv-audit && go test ./...
	cd mkv-movie && go test ./...
	cd mkv-musicvideo && go test ./...
	cd mkv-tvshow && go test ./...

vet:
	cd mkv-audit && go vet ./... && gofmt -l . && staticcheck ./... && unparam ./...
	cd mkv-movie && go vet ./... && gofmt -l . && staticcheck ./... && unparam ./...
	cd mkv-musicvideo && go vet ./... && gofmt -l . && staticcheck ./... && unparam ./...
	cd mkv-tvshow && go vet ./... && gofmt -l . && staticcheck ./... && unparam ./...
	cd sw-update && go vet ./... && gofmt -l . && staticcheck ./... && unparam ./...
	cd book-tracker && go vet ./... && gofmt -l . && staticcheck ./... && unparam ./...

clean:
	rmdir /s /q dist 2>nul || true
