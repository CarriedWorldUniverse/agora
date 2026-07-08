#!/usr/bin/env bash
# ctxmap bench campaign 1 — first real numbers (2026-07-08).
set -u
cd "$(dirname "$0")"
BIN=./ctxbench-bin
DB=~/.ctxmap/bench.db
LOG=~/.ctxmap/campaign1.log
: > "$LOG"

run() { # workload map tail reps
  echo "=== $(basename "$1" .json) (map=$2 tail=$3 reps=$4) $(date -u +%H:%M:%SZ) ===" | tee -a "$LOG"
  $BIN -workload "$1" -map="$2" -tail "$3" -reps "$4" -db "$DB" 2>>"$HOME/.ctxmap/campaign1-stderr.log" | tee -a "$LOG"
}

for wl in workloads/correction-fits.json workloads/rederive-fits.json workloads/multihop-10-fits.json; do
  run "$wl" true  8   3
  run "$wl" false 8   3
  run "$wl" false 999 3
done

echo "=== fits tier done, starting overflow $(date -u +%H:%M:%SZ) ===" | tee -a "$LOG"
run workloads/multihop-10-overflow.json true  8   2
run workloads/multihop-10-overflow.json false 999 2
echo "=== campaign complete $(date -u +%H:%M:%SZ) ===" | tee -a "$LOG"
