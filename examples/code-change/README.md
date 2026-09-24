# Phase 2 code-change acceptance fixture

`calculator/` is a deliberately failing Go module. `Add` subtracts instead of adding; its tests cover positive, negative, and zero operands. The failing baseline is intentional and is part of the acceptance scenario.

## Local baseline

```sh
cd examples/code-change/calculator
go test ./...
```

Expected before the agent task: tests fail because `Add(2, 3)` returns `-1` instead of `5`.

## Agent task

> Find and fix the defect in this repository. Inspect the existing tests, make the smallest correct change, run `go test ./...`, and report the files changed and the test output. Do not claim validation unless the command completed successfully.

Configure the Kernel sandbox root to the absolute `examples/code-change` directory and pass `workspace_path` as its `calculator` child. A complete Phase 2 run must retain the actual operation approvals, command results, file evidence, and independent Reviewer/QA results. The fixture by itself is not evidence that the live code-change acceptance test passed.
