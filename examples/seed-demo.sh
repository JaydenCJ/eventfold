#!/usr/bin/env bash
# Generates a deterministic four-week demo dataset (~40 users moving through
# pageview → signup → activate → subscribe) and ingests it with eventfold.
#
#   bash examples/seed-demo.sh /tmp/eventfold-demo
#
# The generator is a fixed arithmetic sequence, not a RNG: running it twice
# produces byte-identical events, so every query in the README is
# reproducible. Requires the eventfold binary on PATH or in the repo root.
set -euo pipefail

DEST="${1:?usage: seed-demo.sh <data-dir>}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if command -v eventfold >/dev/null 2>&1; then
  BIN=eventfold
elif [ -x "$ROOT/eventfold" ]; then
  BIN="$ROOT/eventfold"
else
  echo "eventfold binary not found; build it first: go build -o eventfold ./cmd/eventfold" >&2
  exit 1
fi

TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

# Every date stays inside 2026-06-01..2026-06-28 so day arithmetic is plain
# integer math on the day-of-month.
emit() { # emit <event> <user-num> <day> <hour> [extra-json-props]
  local extra="${5:-}"
  printf '{"event":"%s","user":"u%03d","ts":"2026-06-%02dT%02d:00:00Z"%s}\n' \
    "$1" "$2" "$3" "$4" "$extra" >>"$TMP"
}

for i in $(seq 1 40); do
  signup_day=$((1 + (i * 5) % 21))
  hour=$((9 + i % 8))
  plan="free"
  [ $((i % 3)) -eq 0 ] && plan="pro"

  # Everyone browses before signing up.
  emit pageview "$i" "$signup_day" $((hour - 1))
  emit signup "$i" "$signup_day" "$hour" ',"props":{"plan":"'"$plan"'","ref":"demo"}'

  # 7 in 10 activate within a couple of days.
  if [ $((i % 10)) -lt 7 ]; then
    emit activate "$i" $((signup_day + i % 3)) $((hour + 1))
  fi
  # 3 in 10 subscribe a few days after activating.
  if [ $((i % 10)) -lt 3 ]; then
    emit subscribe "$i" $((signup_day + i % 3 + 1 + i % 4)) "$hour" ',"props":{"plan":"'"$plan"'"}'
  fi
  # Returning pageviews trail off over the following weeks.
  for w in 1 2 3; do
    if [ $((i % (w + 1))) -eq 0 ] && [ $((signup_day + w * 7)) -le 28 ]; then
      emit pageview "$i" $((signup_day + w * 7)) "$hour"
    fi
  done
done

"$BIN" ingest --dir "$DEST" "$TMP"
echo
echo "Try:"
echo "  $BIN funnel    --dir $DEST --steps 'signup,activate,subscribe' --window 14d --by plan"
echo "  $BIN retention --dir $DEST --cohort signup --period week --periods 4"
echo "  $BIN rollup    --dir $DEST && $BIN count --dir $DEST --event pageview --by day"
