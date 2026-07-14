# Contributing to eventfold

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go ≥1.22; nothing else — the runtime is standard library only.

```bash
git clone https://github.com/JaydenCJ/eventfold && cd eventfold
go build -o eventfold ./cmd/eventfold
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary, ingests a fixed event fixture into a
temp store, and asserts on real CLI output across every subcommand and exit
code; it must finish by printing `SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (88 deterministic tests, no network).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   modules (the funnel/retention engines never touch the filesystem —
   only `store` and `rollup` do I/O).

## Ground rules

- Keep dependencies at zero; adding one needs strong justification in the PR.
- No network calls except the loopback API the user starts explicitly with
  `eventfold serve`. No telemetry, ever.
- Determinism first: identical input must produce byte-identical reports,
  including all orderings — the test suite and smoke script assert on it.
- The CLI and the JSON API must share query logic (`internal/query`);
  never fork analytics behavior between the two.
- Data files are the user's: the on-disk NDJSON format is documented in
  `docs/file-format.md`, and format changes need a schema-version bump.
- Code comments and doc comments are written in English.

## Reporting bugs

Include the output of `eventfold version`, the full command you ran, the
report output, and — for wrong numbers — a minimal NDJSON snippet that
reproduces the miscount, since events in, numbers out is the whole contract.

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
