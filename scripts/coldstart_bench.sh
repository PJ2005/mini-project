#!/bin/bash
BINARY=${1:-./interlink}
CONFIG=${2:-config.yaml}
HEALTH_URL=${3:-http://localhost:8080/health}
RUNS=${4:-30}

results=()
for i in $(seq 1 $RUNS); do
  start_ns=$(date +%s%N)
  $BINARY -config $CONFIG &
  PID=$!
  until curl -sf $HEALTH_URL > /dev/null 2>&1; do sleep 0.001; done
  end_ns=$(date +%s%N)
  elapsed_ms=$(( (end_ns - start_ns) / 1000000 ))
  results+=($elapsed_ms)
  kill $PID 2>/dev/null
  wait $PID 2>/dev/null
  sleep 0.5
done

echo "runs: $RUNS"
printf '%s\n' "${results[@]}" | awk '{sum+=$1; if(NR==1||$1<min)min=$1;
  if($1>max)max=$1} END {printf "mean: %.1f ms\nmin: %d ms\nmax: %d ms\n",
  sum/NR, min, max}'

