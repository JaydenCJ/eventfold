# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-13

### Added

- NDJSON event ingestion into day-partitioned files (`events/2026-06-01.ndjson`)
  with strict per-line validation (required `event`/`user`/`ts`, RFC 3339 or
  unix-epoch timestamps, scalar-only properties, size limits) and idempotent
  re-ingestion via optional `id` keys.
- `funnel` subcommand: ordered multi-step conversion with a configurable
  window, re-anchoring on every first-step occurrence, deterministic
  timestamp tie-breaking, repeated step names, median time-to-step, and
  `--by` segmentation on a first-step property.
- `retention` subcommand: cohort retention triangles by day or week
  (Monday-start, UTC), first-event cohort assignment, and named or
  any-event activity.
- `count` and `events` subcommands: per-day/week volumes and distinct-user
  counts, with a `source` column showing rollup vs raw-scan provenance.
- `rollup` subcommand: per-day aggregate files with byte-size freshness
  fingerprints; fresh rollups answer daily counts without re-reading raw
  events, stale ones fall back to scanning automatically.
- `serve` subcommand: loopback-only JSON API (`/v1/ingest`, `/v1/funnel`,
  `/v1/retention`, `/v1/count`, `/v1/events`, `/v1/health`) sharing every
  line of query logic with the CLI; non-loopback bind addresses refused.
- Stable JSON envelope (`schema_version: 1`) on `--format json` and all API
  responses; scripted exit codes (0 ok, 1 strict-ingest failure, 2 usage,
  3 runtime).
- Runnable examples (`examples/seed-demo.sh`, `examples/api-session.sh`)
  and an on-disk format reference (`docs/file-format.md`).
- 88 deterministic offline tests (unit + in-process CLI and API
  integration) and `scripts/smoke.sh`.

[0.1.0]: https://github.com/JaydenCJ/eventfold/releases/tag/v0.1.0
