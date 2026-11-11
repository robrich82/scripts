# Every top-level directory with a go.mod is a module, built by a target of the same name.
MODULES := $(patsubst %/go.mod,%,$(wildcard */go.mod))

# Lets $(foreach) emit one recipe line per module, so make echoes each and stops on the first failure.
define NL


endef

.PHONY: all clean test vet vet-linux vuln openapi $(MODULES)

all: $(MODULES)

$(MODULES):
	cd $@ && go build -o ../dist/$@.exe .

test:
	$(foreach m,$(MODULES),cd $(m) && go test ./...$(NL))

vet:
	$(foreach m,$(MODULES),cd $(m) && go vet ./... && go mod tidy -diff && golangci-lint run ./... && golangci-lint fmt --diff ./...$(NL))

# Type-checks and lints the non-Windows stubs, as CI does.
vet-linux:
	$(foreach m,$(MODULES),cd $(m) && GOOS=linux go vet ./... && GOOS=linux golangci-lint run ./...$(NL))

# Needs network access to the Go vulnerability database.
vuln:
	$(foreach m,$(MODULES),cd $(m) && govulncheck ./...$(NL))

clean:
	rm -rf dist

# Plex has no download URL for the spec; it's embedded in the Redoc page as __redoc_state.
# jq -e fails on no match, so a page layout change errors instead of emptying openapi.json.
openapi:
	curl -fsSL https://developer.plex.tv/pms/ \
	  | sed -n 's/^[[:space:]]*const __redoc_state = \(.*\);[[:space:]]*$$/\1/p' \
	  | jq -en 'input | .spec.data | select(.openapi and (.paths | length) > 0)' > openapi.json.tmp \
	  || { rm -f openapi.json.tmp; echo "openapi: no spec found on developer.plex.tv/pms/" >&2; exit 1; }
	mv openapi.json.tmp openapi.json
