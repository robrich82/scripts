---
name: check
description: Run the full test and lint suite (`mise run vet` + `mise run vet-linux` + `mise run test`) across all modules. Use this skill before committing, after making code changes, or any time you want to verify the repo is clean — never run go vet or go test directly.
---

Run the full test and lint suite across all modules.

## Steps

1. Run `mise run vet` — executes `go vet`, `go mod tidy -diff`, `golangci-lint run` and `golangci-lint fmt --diff` across all modules.
   - If vet fails, report the errors clearly. Do not proceed to tests until vet passes.
   - Fix any `go vet` or golangci-lint issues before continuing. For a `gofmt` finding, run `gofmt -w` on the file; for a `go mod tidy -diff` failure, run `go mod tidy` in that module.
2. Run `mise run vet-linux` — type-checks and lints the non-Windows stubs, as CI does.
   - If it fails, report the errors clearly and fix them before continuing.
3. Run `mise run test` — executes `go test ./...` across all modules.
   - Report any test failures with the full output.

## Pass criteria

All three commands must exit 0. Report overall status as PASS or FAIL at the end.
