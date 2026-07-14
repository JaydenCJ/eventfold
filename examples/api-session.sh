#!/usr/bin/env bash
# Demonstrates the JSON API end to end: starts a loopback server over a
# temp store, ingests events over HTTP, queries a funnel, and shuts down.
#
#   bash examples/api-session.sh
#
# Requires curl and the eventfold binary (PATH or repo root).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if command -v eventfold >/dev/null 2>&1; then
  BIN=eventfold
elif [ -x "$ROOT/eventfold" ]; then
  BIN="$ROOT/eventfold"
else
  echo "eventfold binary not found; build it first: go build -o eventfold ./cmd/eventfold" >&2
  exit 1
fi

DATA="$(mktemp -d)"
ADDR="127.0.0.1:8991"
"$BIN" serve --dir "$DATA" --addr "$ADDR" &
SRV=$!
trap 'kill "$SRV" 2>/dev/null; rm -rf "$DATA"' EXIT

# Wait for the listener (bounded).
for _ in $(seq 1 50); do
  curl -fsS --noproxy '*' "http://$ADDR/v1/health" >/dev/null 2>&1 && break
  sleep 0.1
done

echo "== ingest over HTTP"
curl -fsS --noproxy '*' -X POST --data-binary @- "http://$ADDR/v1/ingest" <<'NDJSON'
{"event":"signup","user":"u1","ts":"2026-06-01T09:00:00Z","props":{"plan":"pro"}}
{"event":"activate","user":"u1","ts":"2026-06-01T10:00:00Z"}
{"event":"signup","user":"u2","ts":"2026-06-01T11:00:00Z","props":{"plan":"free"}}
NDJSON

echo
echo "== funnel over HTTP"
curl -fsS --noproxy '*' "http://$ADDR/v1/funnel?steps=signup,activate&window=1d"

echo
echo "== the same store answers on the CLI"
"$BIN" funnel --dir "$DATA" --steps "signup,activate" --window 1d
