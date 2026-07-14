# eventfold on-disk format

Everything eventfold stores is a plain text file you can read, grep, diff,
back up and version without eventfold's help. This document is the contract:
tools that write these files directly are first-class citizens.

## Directory layout

```
<data-dir>/
├── events/
│   ├── 2026-06-01.ndjson    # one append-only file per UTC day
│   └── 2026-06-02.ndjson
└── rollups/
    ├── 2026-06-01.json      # per-day aggregates, derived, safe to delete
    └── 2026-06-02.json
```

Partitioning is by the **UTC day of the event timestamp**, not ingestion
time. Files that do not match `YYYY-MM-DD.ndjson` are ignored by scans.

## Event lines

One JSON object per line:

```json
{"event":"signup","user":"u_419","ts":"2026-06-01T09:30:00Z","id":"evt-8f2","props":{"plan":"pro","seats":3}}
```

| Field | Required | Rules |
|---|---|---|
| `event` | yes | 1–128 bytes, no control characters |
| `user` | yes | 1–128 bytes, no control characters; the distinct-count key |
| `ts` | yes | RFC 3339 string, or unix epoch number (≥1e11 ⇒ milliseconds); year 1970–3000 |
| `id` | no | ≤128 bytes; idempotency key, deduplicated within the target day partition |
| `props` | no | ≤32 keys; keys ≤64 bytes; scalar values only, coerced to strings, ≤256 bytes |

Unknown top-level fields are rejected (a typoed `"even":` should fail loudly,
not silently drop data). Lines over 1 MiB are rejected. On ingest, invalid
lines are counted and reported but never abort the batch — pass `--strict`
to turn any invalid line into exit code 1.

Stored lines are canonicalized: timestamps become UTC RFC 3339, and key
order is fixed, so identical events are byte-identical on disk.

## Deduplication semantics

Events **with** an `id` are idempotent: re-ingesting the same export is a
no-op, because the id is checked against the batch and the target day file.
Events **without** an `id` are always appended — eventfold cannot guess
whether two identical pageviews are a retry or a real second view.

## Rollup files

`rollups/<day>.json` caches per-day, per-event aggregates:

```json
{
  "schema_version": 1,
  "date": "2026-06-01",
  "source_bytes": 8412,
  "events": {
    "signup": { "count": 12, "users": 9 }
  }
}
```

`source_bytes` fingerprints the partition the rollup was computed from.
Partitions are append-only, so a size mismatch means the rollup is stale;
stale or missing rollups make queries fall back to scanning raw events, and
`eventfold rollup` rebuilds them. Rollups are pure derived data — deleting
the whole `rollups/` directory is always safe.

Daily `count` queries are served from fresh rollups. Weekly buckets and the
`events` listing always scan, because distinct users cannot be summed
across daily aggregates without double counting — eventfold prefers a slow
correct answer over a fast wrong one, and the `source` column in `count`
output tells you which path answered.

## Tolerance rules

Scans skip (and count) malformed lines instead of failing the whole query:
a hand-edited partition should degrade a report by one line, not kill it.
The skipped-line count is surfaced in scan statistics, so silent corruption
does not stay silent.
