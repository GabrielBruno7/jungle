#!/usr/bin/env bash

set -euo pipefail

API="${API:-http://localhost:8080}"
DURATION="${DURATION:-60s}"
OUT_DIR="${OUT_DIR:-deploy/loadtest/out}"
LAG_SAMPLE_INTERVAL="${LAG_SAMPLE_INTERVAL:-1}"

mkdir -p "$OUT_DIR"
chmod 777 "$OUT_DIR" 2>/dev/null || true

metric() {
    local name="$1"
    curl -s --max-time 5 "$API/metrics" \
        | awk -v n="$name" '
            $0 ~ "^"n"([{ ]|$)" {
                v = $NF
                if (v + 0 == v) { sum += v; seen = 1 }
            }
            END { if (seen) printf "%.6f\n", sum; else print "0" }
        '
}

require_ready() {
    if ! curl -sf --max-time 5 "$API/health/ready" > /dev/null; then
        echo "The service is not ready at $API." >&2
        echo "Start it first:  docker compose up -d" >&2
        exit 1
    fi
}

echo "==> Checking the service is up"
require_ready
curl -s "$API/health/ready"
echo

echo "==> Snapshotting metrics before the run"
BEFORE_CONFLICTS=$(metric jungle_concurrency_conflicts_total)
BEFORE_DUPLICATES=$(metric jungle_duplicate_operations_total)
BEFORE_RESULTS=$(metric jungle_wager_transaction_results_total)
BEFORE_PUBLISHED=$(metric jungle_outbox_published_total)
BEFORE_RETRIES=$(metric jungle_outbox_retries_total)
BEFORE_DLQ=$(metric jungle_messages_dead_lettered_total)

printf "    conflicts=%s duplicates=%s results=%s published=%s\n" \
    "$BEFORE_CONFLICTS" "$BEFORE_DUPLICATES" "$BEFORE_RESULTS" "$BEFORE_PUBLISHED"

LAG_FILE="$OUT_DIR/outbox-lag.txt"
: > "$LAG_FILE"
(
    while true; do
        metric jungle_outbox_lag_seconds >> "$LAG_FILE" 2>/dev/null || true
        sleep "$LAG_SAMPLE_INTERVAL"
    done
) &
LAG_PID=$!
trap 'kill "$LAG_PID" 2>/dev/null || true' EXIT

echo "==> Running k6"
set +e
docker compose run --rm \
    -e DURATION="$DURATION" \
    k6 run --summary-export=/out/k6-summary.json /scripts/load.js \
    2>&1 | tee "$OUT_DIR/k6-output.txt"
K6_STATUS=${PIPESTATUS[0]}
set -e

kill "$LAG_PID" 2>/dev/null || true
trap - EXIT

echo
echo "==> Waiting 10s for the outbox to drain"
sleep 10
metric jungle_outbox_lag_seconds >> "$LAG_FILE"

echo "==> Snapshotting metrics after the run"
AFTER_CONFLICTS=$(metric jungle_concurrency_conflicts_total)
AFTER_DUPLICATES=$(metric jungle_duplicate_operations_total)
AFTER_RESULTS=$(metric jungle_wager_transaction_results_total)
AFTER_PUBLISHED=$(metric jungle_outbox_published_total)
AFTER_RETRIES=$(metric jungle_outbox_retries_total)
AFTER_DLQ=$(metric jungle_messages_dead_lettered_total)

delta() {
    awk -v a="$1" -v b="$2" 'BEGIN { printf "%.0f\n", b - a }'
}

PEAK_LAG=$(awk 'BEGIN{m=0} {if ($1+0 > m) m=$1+0} END{printf "%.3f\n", m}' "$LAG_FILE")
FINAL_LAG=$(tail -n 1 "$LAG_FILE" | awk '{printf "%.3f\n", $1+0}')
LAG_SAMPLES=$(wc -l < "$LAG_FILE" | tr -d ' ')

echo
echo "================ application metrics (deltas over the run) ================"
printf "  wager transactions settled     %s\n" "$(delta "$BEFORE_RESULTS" "$AFTER_RESULTS")"
printf "  duplicate operations detected  %s\n" "$(delta "$BEFORE_DUPLICATES" "$AFTER_DUPLICATES")"
printf "  concurrency conflicts          %s\n" "$(delta "$BEFORE_CONFLICTS" "$AFTER_CONFLICTS")"
printf "  outbox events published        %s\n" "$(delta "$BEFORE_PUBLISHED" "$AFTER_PUBLISHED")"
printf "  outbox publish retries         %s\n" "$(delta "$BEFORE_RETRIES" "$AFTER_RETRIES")"
printf "  messages dead-lettered         %s\n" "$(delta "$BEFORE_DLQ" "$AFTER_DLQ")"
printf "  outbox lag peak                %ss  (over %s samples)\n" "$PEAK_LAG" "$LAG_SAMPLES"
printf "  outbox lag after draining      %ss\n" "$FINAL_LAG"
echo "==========================================================================="
echo
echo "Artifacts: $OUT_DIR/k6-output.txt, $OUT_DIR/k6-summary.json, $LAG_FILE"

if [ "$K6_STATUS" -ne 0 ]; then
    echo
    echo "k6 exited non-zero: at least one threshold failed. See the summary above." >&2
fi

exit "$K6_STATUS"
