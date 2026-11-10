# Every top-level directory with a go.mod is a module, built by a target of the same name.
MODULES := $(patsubst %/go.mod,%,$(wildcard */go.mod))

# Lets $(foreach) emit one recipe line per module, so make echoes each and stops on the first failure.
define NL


endef

.PHONY: all clean test vet vet-linux vuln $(MODULES)

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
