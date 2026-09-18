# Load test results

A recorded run of `deploy/loadtest/run.sh`. **This is not a performance
claim.** Everything — the service, Postgres, LocalStack, Keycloak, Jaeger,
Prometheus, Grafana and the load generator itself — ran on one 4-core laptop,
competing for the same CPUs. The numbers are useful for finding where this
system bends first, not for predicting what it does on real hardware.

The run did find where it bends, and it is not where the HTTP numbers suggest.

---

## Reproducing it

```bash
docker compose up -d
# wait for {"status":"ok"}
curl -s http://localhost:8080/health/ready

./deploy/loadtest/run.sh
```

Knobs, all optional:

```bash
DURATION=120s WALLET_POOL=100 CONTENDED_WALLETS=5 ./deploy/loadtest/run.sh
```

The wrapper starts k6 in a container on the Compose network, samples
`jungle_outbox_lag_seconds` once a second for the whole run, and prints the
before/after deltas of the application's own counters next to k6's summary.
Artifacts land in `deploy/loadtest/out/`.

## Environment

| | |
|---|---|
| Host | single laptop, 4 CPUs, 7 GiB RAM, WSL2 |
| Docker | 29.0.2, Compose 2.40.3 |
| App replicas | 1 |
| Everything co-located | service, Postgres, LocalStack, Keycloak, Jaeger, Prometheus, Grafana **and k6** all on the same host |
| k6 | 0.55.0, `grafana/k6` image, on the Compose network |
| Run date | 2026-09-18 |
| Database | empty at the start (`docker compose down -v` before the run) |

The load generator competing with the system under test for the same four
cores is the largest single caveat on every latency number below.

## Methodology

Three scenarios run concurrently, because each isolates a different property:

| Scenario | Shape | What it isolates |
|---|---|---|
| `spread` | ramping arrival rate, 40 → 160 req/s, over 50 wallets | throughput with **no** row contention — measures the service, not the lock |
| `contention` | 20 constant VUs, all hitting **3** wallets | what queueing on `SELECT ... FOR UPDATE` costs |
| `replay` | 5 constant VUs, each resending one identical operation forever | the idempotent-replay path, isolated from new work |

Details that make the numbers mean something:
- Tokens come from the real Keycloak and are refreshed per-VU after four
  minutes, so a long run does not decay into a wall of 401s.
- Setup opens 50 wallets funded with 1,000,000.00 each, so nothing drains and
  `INSUFFICIENT_BALANCE` never masquerades as a failure.
- Every operation carries a unique `externalTransactionId` and
  `Idempotency-Key`, except in `replay`, where every iteration after the first
  deliberately reuses both.
- **422 is not counted as an error.** A refused bet is a correct answer. Only
  transport failures, 401/403 and 5xx count against the thresholds, so a red
  run always means something actually broke.

## Results

All k6 thresholds passed. 65,731 iterations, zero failures, nothing dropped.

### Throughput

| | |
|---|---|
| HTTP requests | 65,847 (**655 req/s**) |
| Operations settled | 65,731 (**654 ops/s**) |
| Idempotent replays | 18,477 |
| Dropped iterations | 0 |
| Data sent / received | 105 MB / 22 MB |

The arrival rate was capped at 160/s in the `spread` stages; the observed
655/s is the three scenarios combined. The service was not pushed to
saturation on the HTTP path, so the ceiling is higher than this run measured.

### Latency

| Scenario | avg | p50 | p90 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `spread` (no contention) | 27.19 ms | 31.30 ms | 40.87 ms | 48.75 ms | 76.73 ms | 182.10 ms |
| `contention` (3 wallets, 20 VUs) | 35.02 ms | 33.11 ms | 41.00 ms | 47.07 ms | 71.92 ms | 185.78 ms |
| `replay` (pure idempotent replay) | 16.05 ms | 15.24 ms | 20.81 ms | 23.58 ms | 36.65 ms | 133.60 ms |
| **overall** | 28.10 ms | 31.01 ms | 38.88 ms | 44.33 ms | 68.60 ms | 236.83 ms |

`spread`'s average sits below its median because the scenario ramps: the
early, lightly loaded seconds pull the mean down.

### Errors

| Class | Count |
|---|---|
| Transport failures (status 0) | 0 |
| 401 / 403 | 0 |
| 5xx | 0 |
| 409 conflicts | 0 |
| 422 business rejections | 0 |
| `http_req_failed` | 0.00 % (0 / 65,847) |
| checks passed | 100.00 % (65,731 / 65,731) |

### Application-side counters (deltas over the run)

| | |
|---|---|
| Wager transactions settled | 65,731 |
| Duplicate operations detected | 18,477 |
| **Concurrency conflicts** | **0** |
| Outbox events published | 12,620 |
| Outbox publish retries | 0 |
| Messages dead-lettered | 0 |
| **Outbox lag, peak** | **66.3 s** (102 samples, one per second) |
| Outbox lag, after a 10 s drain window | 66.3 s |

---

## What the numbers actually say

### 1. The outbox publisher is still the bottleneck

This is the finding, and it is the same one as in the previous run, now
smaller but not gone.

The service settled 65,731 operations in 100 seconds. The 47,254 of them that
were not replays wrote **92,650 events** into the outbox, because a `BET` or
`WIN` produces two (`WagerTransactionProcessed` and `WalletBalanceChanged`)
and a `LOSS` produces one. In the same window the publisher got **12,620** of
them out. At the end of the run, 73,247 events were still waiting, and
`jungle_outbox_lag_seconds` had climbed to 66 seconds and was still rising
when the load stopped.

Measured right after the run, with no HTTP load at all, the publisher drains
at about **306 events/s**:

```
published in 30s: 9178  ->  305.9 events/s sustained
backlog at that moment: 56,854 events
```

So the write path produced roughly 900 events/s against a publish path that
clears about 300. The overrun is a factor of three, and it accumulates for as
long as the load lasts.

Worth being precise about two things:

- **This is throughput, not correctness.** Every one of those events is
  committed in the same transaction as the balance change it describes.
  Nothing is lost and nothing is published before its cause; the events are
  simply published *late*, and under sustained load the lag grows without
  bound.
- **The gauge is what turned a suspicion into a number.**
  `jungle_outbox_lag_seconds` is the metric to alert on, and it is the reason
  this section exists at all.

The previous run measured the same publisher at **42 events/s**. The
difference is `PublishOutbox.RunOnce`, which now keeps claiming and draining
until a claim comes back empty instead of publishing one batch per tick. That
is a sevenfold improvement from a small change, and it is not the real fix:
each event still costs one `SendMessage` round trip plus one transaction to
mark it published. `SendMessageBatch` (10 messages per call) with a single
batched `MarkPublished` is the change that would close the gap, and it is
identified, not implemented.

### 2. Zero concurrency conflicts, and that is the design working

Twenty VUs hammering three wallets produced **zero** increments of
`jungle_concurrency_conflicts_total`. That is not the scenario failing to
generate contention; it is the concurrency strategy behaving as documented.

`SELECT ... FOR UPDATE` **serializes** writers on a wallet row: they queue
rather than collide. The version compare-and-swap behind it is a second
barrier that only fires if a writer somehow proceeds without the lock, so in
normal operation it never trips. Contention shows up as *latency*, not as
conflicts, and the numbers show exactly that: contended p50 is 33.11 ms
against 31.30 ms uncontended, and at p95 the contended scenario is actually
slightly faster (47.07 ms vs 48.75 ms), because `spread` was carrying the
ramping arrival rate while `contention` ran at a fixed 20 VUs.

Queueing 20 writers on 3 rows cost roughly 2 ms at the median. The lock is
not a throughput problem at this scale.

### 3. Replays are the cheapest path, as intended

`replay` was the fastest scenario by a wide margin: p50 15.24 ms against
31.30 ms for new work, and p99 36.65 ms against 76.73 ms. 18,477 replays were
served without a single duplicate debit. The fast path in
`ProcessWager.Execute`, which answers an already-settled operation without
opening a write transaction or taking the wallet lock, is doing what it was
written to do.

### 4. What this run did not establish

- **The HTTP ceiling.** Latency stayed flat as the arrival rate climbed, so
  saturation was never reached. A proper capacity test would ramp until p95
  degrades.
- **Multi-instance behaviour.** This ran against one replica. With
  `--scale app=3` the publishers contend for outbox rows, which is a different
  and more interesting shape, and it is also the configuration where the
  publish bottleneck would ease.
- **Anything about real hardware.** Four shared cores running the database,
  three supporting services, the observability stack and the load generator is
  not a representative deployment.
