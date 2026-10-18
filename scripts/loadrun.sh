#!/bin/sh
# Load-test orchestration (docs/resiliance-and-scale.md §НТ): mock provider +
# resource sampler + the loadgen scenario, one command. Run from a repository
# checkout against a running stack whose kernel points at the mock provider:
#
#   TEMPORALITY_MODEL_BASE_URL=http://mockllm:8081/v1
#   TEMPORALITY_MODEL_ID=loadgen-model
#
# Usage: scripts/loadrun.sh [runs] [ramp-seconds] [report-path]
set -eu

RUNS="${1:-50}"
RAMP="${2:-60}"
REPORT="${3:-loadreport-$(date +%Y%m%d-%H%M%S).md}"
SAMPLER_PID=""

cleanup() {
  if [ -n "$SAMPLER_PID" ]; then
    kill "$SAMPLER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

echo "== building tools =="
go build -o /tmp/loadgen-mockllm ./examples/mockllm
go build -o /tmp/loadgen-runner ./examples/loadgen

echo "== starting mock provider on :8081 =="
/tmp/loadgen-mockllm -addr :8081 -first-token 200ms -token-gap 15ms -tokens 120 &
MOCK_PID=$!
trap 'kill $MOCK_PID $SAMPLER_PID 2>/dev/null' EXIT INT TERM
sleep 1

echo "== starting resource sampler (docker stats every 2s) =="
(
  while :; do
    echo "=== $(date +%H:%M:%S)"
    docker stats --no-stream --format '{{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}' \
      | grep -E 'agent-kernel|journal|postgres|debugger' || true
    sleep 2
  done
) > /tmp/loadgen-resources.log &
SAMPLER_PID=$!

echo "== load: $RUNS runs, ramp ${RAMP}s =="
/tmp/loadgen-runner \
  -kernel "${KERNEL:-http://localhost:8090}" \
  -token "${TOKEN:-}" \
  -project "${PROJECT:-loadgen}" \
  -agent "${AGENT:-loadgen-agent}" \
  -model "${MODEL:-loadgen-model}" \
  -runs "$RUNS" -ramp "${RAMP}s" \
  -report "$REPORT" \
  2>&1 | tee /tmp/loadgen-runner.log

kill "$MOCK_PID" 2>/dev/null || true

echo "== resource summary (peak cpu / peak mem per service) =="
# cpu peaks: "name cpu=X%" lines; mem peaks: numeric MiB values per name.
grep -oE '[a-z0-9-]+ cpu=[0-9.]+%' /tmp/loadgen-resources.log \
  | awk '{split($0,a," cpu="); if (a[2]+0 > pcpu[a[1]]) pcpu[a[1]]=a[2]+0}
         END {for (s in pcpu) printf "  %-14s peak cpu %.2f%%\n", s, pcpu[s]}' | sort
grep -oE '[a-z0-9-]+ .*mem=[0-9.]+[GM]iB' /tmp/loadgen-resources.log \
  | sed -E 's/ mem=([0-9.]+)([GM])iB.*/\t\1\t\2/' \
  | awk -F'\t' '{name=$1; sub(/ cpu=.*$/, "", name); mb=$2+0; if ($3=="G") mb*=1024;
          if (mb > pmem[name]) pmem[name]=mb}
         END {for (s in pmem) printf "  %-14s peak mem %.0f MiB\n", s, pmem[s]}' | sort
echo "full sampler log: /tmp/loadgen-resources.log"
echo "report: $REPORT"
