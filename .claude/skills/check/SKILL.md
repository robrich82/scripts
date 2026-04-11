---
name: check
description: Run tests and linting across all modules (make test + make vet)
disable-model-invocation: true
allowed-tools: Bash(make *)
---

Run the full test and lint suite across all modules.

## Steps

1. Run `make vet` — executes `go vet`, `gofmt`, `staticcheck`, and `unparam` across all modules.
   - If vet fails, report the errors clearly. Do not proceed to tests until vet passes.
   - Fix any `gofmt` or `go vet` issues before continuing.
2. Run `make test` — executes `go test ./...` across all modules.
   - Report any test failures with the full output.

## Pass criteria

Both commands must exit 0. Report overall status as PASS or FAIL at the end.
