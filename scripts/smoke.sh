#!/usr/bin/env bash
# End-to-end smoke test for eventfold: builds the binary, ingests a fixed
# event fixture into a temp store, and asserts on real CLI output across
# every subcommand and exit code. No network, idempotent, finishes in
# seconds.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

BIN="$WORKDIR/eventfold"
DATA="$WORKDIR/data"
FIXTURE="$WORKDIR/events.ndjson"

echo "1. build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/eventfold) || fail "go build failed"

echo "2. version matches manifest"
"$BIN" --version | grep -qx "eventfold 0.1.0" || fail "--version mismatch"

echo "3. ingest a fixture with one invalid and one duplicate line"
cat >"$FIXTURE" <<'NDJSON'
{"event":"signup","user":"u1","ts":"2026-06-01T09:00:00Z","id":"e1","props":{"plan":"pro"}}
{"event":"activate","user":"u1","ts":"2026-06-01T10:00:00Z","id":"e2"}
{"event":"subscribe","user":"u1","ts":"2026-06-03T09:00:00Z","id":"e3","props":{"plan":"pro"}}
{"event":"signup","user":"u2","ts":"2026-06-01T11:00:00Z","id":"e4","props":{"plan":"free"}}
{"event":"activate","user":"u2","ts":"2026-06-05T11:00:00Z","id":"e5"}
{"event":"signup","user":"u3","ts":"2026-06-08T09:00:00Z","id":"e6"}
{"event":"pageview","user":"u1","ts":"2026-06-08T12:00:00Z","id":"e7"}
{"event":"pageview","user":"u1","ts":"2026-06-08T12:00:00Z","id":"e7"}
this line is not an event
NDJSON
OUT="$("$BIN" ingest --dir "$DATA" --quiet "$FIXTURE")"
echo "$OUT" | grep -q "ingested 7 events into 4 day files (1 duplicate, 1 invalid)" \
  || fail "ingest summary wrong: $OUT"
[ -f "$DATA/events/2026-06-01.ndjson" ] || fail "day partition missing"

echo "4. re-ingest is a no-op (idempotency by id)"
"$BIN" ingest --dir "$DATA" --quiet "$FIXTURE" | grep -q "ingested 0 events" \
  || fail "re-ingest wrote duplicates"

echo "5. funnel counts ordered conversion inside the window"
FUN="$("$BIN" funnel --dir "$DATA" --steps "signup,activate,subscribe" --window 7d)"
echo "$FUN" | grep -q "entered: 3 users" || fail "funnel entered wrong"
echo "$FUN" | grep -q "2. activate" || fail "funnel step row missing"
echo "$FUN" | grep -q "33.3%" || fail "funnel overall conversion should be 33.3%"
echo "$FUN" | grep -q "█" || fail "funnel gauge missing"

echo "6. funnel --by segments on a first-step property"
"$BIN" funnel --dir "$DATA" --steps "signup,activate" --window 7d --by plan \
  | grep -q "plan = pro" || fail "funnel segment missing"

echo "7. JSON output carries the machine envelope"
JSON="$("$BIN" funnel --dir "$DATA" --steps "signup,activate" --format json)"
echo "$JSON" | grep -q '"tool": "eventfold"' || fail "json envelope missing"
echo "$JSON" | grep -q '"entered": 3' || fail "json entered wrong"

echo "8. retention builds a weekly cohort triangle"
RET="$("$BIN" retention --dir "$DATA" --cohort signup --period week --periods 2)"
echo "$RET" | grep -q "2026-06-01" || fail "cohort row missing"
echo "$RET" | grep -q "50.0%" || fail "week-1 retention should be 50.0%"

echo "9. rollups build once, then report fresh, and serve daily counts"
"$BIN" rollup --dir "$DATA" | grep -q "4 built, 0 already fresh" || fail "rollup build wrong"
"$BIN" rollup --dir "$DATA" | grep -q "0 built, 4 already fresh" || fail "rollup freshness wrong"
CNT="$("$BIN" count --dir "$DATA" --event signup --by day)"
echo "$CNT" | grep -q "2026-06-01         2         2  rollup" || fail "daily count not from rollup"
echo "$CNT" | grep -q "3 events across 4 buckets" || fail "count total wrong"

echo "10. events lists names by volume"
"$BIN" events --dir "$DATA" | grep -q "signup            3         3" || fail "events listing wrong"

echo "11. exit codes: strict data failure 1, usage 2"
set +e
"$BIN" ingest --dir "$DATA" --strict --quiet "$FIXTURE" >/dev/null 2>&1
[ $? -eq 1 ] || fail "strict ingest should exit 1"
"$BIN" funnel --dir "$DATA" --steps "signup,activate" --format yaml >/dev/null 2>&1
[ $? -eq 2 ] || fail "bad --format should exit 2"
"$BIN" serve --dir "$DATA" --addr 0.0.0.0:9 >/dev/null 2>&1
[ $? -eq 2 ] || fail "non-loopback serve should exit 2"
set -e

echo "SMOKE OK"
