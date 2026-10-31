---
name: check
description: Run the full test and lint suite (make vet + make vet-linux + make test) across all modules. Use this skill before committing, after making code changes, or any time you want to verify the repo is clean — never run go vet or go test directly.
---

Run the full test and lint suite across all modules.

## Steps

1. Run `make vet` — executes `go vet`, `gofmt`, `staticcheck`, and `unparam` across all modules.
   - If vet fails, report the errors clearly. Do not proceed to tests until vet passes.
   - Fix any `gofmt` or `go vet` issues before continuing.
2. Run `make vet-linux` — type-checks the non-Windows stubs, as CI does.
   - If it fails, report the errors clearly and fix them before continuing.
3. Run `make test` — executes `go test ./...` across all modules.
   - Report any test failures with the full output.

## Pass criteria

All three commands must exit 0. Report overall status as PASS or FAIL at the end.
