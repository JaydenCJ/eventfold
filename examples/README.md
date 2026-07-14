# eventfold examples

Two runnable scripts, both self-contained. Build the binary first:

```bash
go build -o eventfold ./cmd/eventfold
```

## seed-demo.sh

Generates a deterministic four-week SaaS-shaped dataset — 40 users moving
through `pageview → signup → activate → subscribe`, with free/pro plans and
decaying return visits — and ingests it. The generator is a fixed arithmetic
sequence, so every run produces byte-identical events and every query below
is reproducible.

```bash
bash examples/seed-demo.sh /tmp/eventfold-demo
./eventfold funnel    --dir /tmp/eventfold-demo --steps "signup,activate,subscribe" --window 14d --by plan
./eventfold retention --dir /tmp/eventfold-demo --cohort signup --period week --periods 4
./eventfold rollup    --dir /tmp/eventfold-demo
./eventfold count     --dir /tmp/eventfold-demo --event pageview --by day
```

## api-session.sh

Starts `eventfold serve` on `127.0.0.1:8991` over a temp store, ingests
three events with `curl` over HTTP, queries the funnel endpoint, then runs
the same funnel on the CLI to show both paths agree — they share the same
query engine. Requires `curl`.

```bash
bash examples/api-session.sh
```
