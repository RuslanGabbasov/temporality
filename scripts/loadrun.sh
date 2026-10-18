#!/bin/bash
# Load-test orchestration (docs/resilience-and-scale.md §НТ): preflight
# (sandbox, provider health), the load scenario, resource sampling and a
# markdown report — one command per profile.
#
# Prerequisites on this host: Go (builds the tools) and docker CLI (sampler).
# The stack runs with the mock provider configured in .env:
#
#   TEMPORALITY_MODEL_BASE_URL=http://mockllm:8081/v1
#   TEMPORALITY_MODEL_ID=loadgen-model
#   # and the stack started with the loadtest profile:
#   docker compose -f docker-compose.yml -f docker-compose.dev.yml --profile loadtest up -d
#
# Usage:
#   scripts/loadrun.sh [runs] [ramp-seconds] [report-path] [--hitl] [--heavy]
#
#   --hitl   mock answers ask_human after the tool turns; the driver plays the
#            human with -human-delay 30s — the queue-wait (HWT) profile.
#   --heavy  heavier workload: 12s sandbox commands (concurrent containers),
#            1.5s token gaps (long streams), ask_human pause in the middle —
#            the profile that stresses sandboxes, journal write volume and
#            the token bus at once.
set -eu

RUNS="50"
RAMP="60"
REPORT=""
HITL="0"
HEAVY="0"
SET_RUNS=""
SET_RAMP=""
while [ $# -gt 0 ]; do
  case "$1" in
    --hitl) HITL="1" ;;
    --heavy) HEAVY="1" ;;
    *) if [ -z "${SET_RUNS:-}" ]; then RUNS="$1"; SET_RUNS=1
       elif [ -z "${SET_RAMP:-}" ]; then RAMP="$1"; SET_RAMP=1
       elif [ -z "${REPORT:-}" ]; then REPORT="$1"
       fi ;;
  esac
  shift
done
[ -n "$REPORT" ] || REPORT="loadreport-$(date +%Y%m%d-%H%M%S).md"
KERNEL_URL="${KERNEL:-http://localhost:8090}"
JOURNAL_URL="${JOURNAL:-http://localhost:8080}"
MOCK_URL="${MOCK:-http://localhost:8081}"
TOKEN="${TOKEN:-}"
PROJECT="${PROJECT:-loadgen}"

SAMPLER_PID=""
MOCK_PID=""
cleanup() {
  [ -n "$SAMPLER_PID" ] && kill "$SAMPLER_PID" 2>/dev/null || true
  [ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "== 1/4 build tools (requires Go) =="
go build -o /tmp/loadgen-runner ./examples/loadgen
if [ "$HITL" = "1" ] || [ "$HEAVY" = "1" ]; then
  go build -o /tmp/mockllm-bin ./examples/mockllm
fi

echo "== 2/4 preflight =="
auth=()
[ -n "$TOKEN" ] && auth=(-H "Authorization: Bearer $TOKEN")
# Token check first: a gated stand fails every call with 401, and the report
# would be a table of failures instead of a load measurement.
who=$(curl -s -m 10 "${auth[@]}" "$KERNEL_URL/v1/agent/whoami" || true)
case "$who" in
  *auth_enabled*) echo "   token accepted: $(echo "$who" | sed -n 's/.*"subject":"\([^"]*\)".*/\1/p')" ;;
  *anonymous*)    echo "   anonymous mode (auth disabled)" ;;
  *)              echo "   WARNING: token rejected — expect 401s (whoami: $who)" ;;
esac
status=$(curl -s -m 10 "${auth[@]}" "$KERNEL_URL/v1/setup/status" || echo '{}')
needs_sandbox=$(echo "$status" | sed -n 's/.*"sandbox":{"configured":\([a-z]*\).*/\1/p')
pinned=$(echo "$status" | grep -o '"pinned_by_env":true' || true)
if [ "$needs_sandbox" = "false" ]; then
  echo "   sandbox not configured — building via /v1/setup/sandbox/build (minutes)…"
  code=$(curl -s -o /tmp/loadrun-sandbox.json -w '%{http_code}' -m 900 -X POST \
    "${auth[@]}" -H 'Content-Type: application/json' "$KERNEL_URL/v1/setup/sandbox/build" || echo curl_err)
  [ "$code" = "200" ] || { echo "   sandbox build failed (HTTP $code): $(cat /tmp/loadrun-sandbox.json)"; exit 1; }
  echo "   sandbox built: $(sed -n 's/.*"image":"\([^"]*\)".*/\1/p' /tmp/loadrun-sandbox.json)"
elif [ -n "$pinned" ]; then
  echo "   sandbox pinned by KERNEL_SANDBOX_IMAGE — skipping build (by design)"
fi
mock_health=$(curl -s -m 5 "$MOCK_URL/healthz" || true)
case "$mock_health" in
  *ok*) echo "   mock provider: up ($MOCK_URL)" ;;
  *)    echo "   WARNING: mock provider not reachable at $MOCK_URL — check the loadtest profile / process"; exit 1 ;;
esac

echo "== 3/4 resource sampler (1s: containers, cpu, mem) =="
(
  while :; do
    echo "=== $(date +%H:%M:%S) containers=$(docker ps -q | wc -l)"
    docker stats --no-stream --format '{{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}' \
      | grep -E 'agent-kernel|journal|postgres|mockllm|debugger' || true
    sleep 1
  done
) > /tmp/loadgen-resources.log &
SAMPLER_PID=$!

echo "== 4/4 load: $RUNS runs, ramp ${RAMP}s, hitl=$HITL heavy=$HEAVY =="
hitl_flags=""
mock_flags="-first-token 200ms -token-gap 15ms -tokens 120"
if [ "$HITL" = "1" ]; then
  hitl_flags="-hitl -human-delay 30s"
fi
if [ "$HEAVY" = "1" ]; then
  # Heavy profile: 10s sandbox commands (long-lived concurrent containers),
  # 1.5s token gaps (long streams), and an ask_human pause mid-run —
  # sandboxes + journal volume + token bus stressed together.
  mock_flags="-first-token 1s -token-gap 1500ms -tokens 300 -tool-command 'sleep 10 && echo heavy'"
  hitl_flags="-hitl -human-delay 45s"
fi
/tmp/loadgen-runner \
  -kernel "$KERNEL_URL" \
  -journal "$JOURNAL_URL" \
  -token "$TOKEN" \
  -project "$PROJECT" \
  -agent "${AGENT:-loadgen-agent}" \
  -model "${MODEL:-loadgen-model}" \
  -runs "$RUNS" -ramp "${RAMP}s" \
  -timeout "${RUN_TIMEOUT:-20m}" \
  $hitl_flags \
  -report "$REPORT" \
  2>&1 | tee /tmp/loadgen-runner.log

echo

echo "== resource summary (peaks per service + concurrent sandboxes) =="
grep -oE '[a-z0-9-]+ cpu=[0-9.]+%' /tmp/loadgen-resources.log \
  | awk '{split($0,a," cpu="); if (a[2]+0 > pcpu[a[1]]) pcpu[a[1]]=a[2]+0}
         END {for (s in pcpu) printf "  %-14s peak cpu %.2f%%\n", s, pcpu[s]}' | sort
grep -oE '[a-z0-9-]+ .*mem=[0-9.]+[GM]iB' /tmp/loadgen-resources.log \
  | sed -E 's/ mem=([0-9.]+)([GM])iB.*/\t\1\t\2/' \
  | awk -F'\t' '{name=$1; sub(/ cpu=.*$/, "", name); mb=$2+0; if ($3=="G") mb*=1024;
          if (mb > pmem[name]) pmem[name]=mb}
         END {for (s in pmem) printf "  %-14s peak mem %.0f MiB\n", s, pmem[s]}' | sort
# Peak concurrent sandboxes: the container count line from the sampler.
grep -oE 'containers=[0-9]+' /tmp/loadgen-resources.log \
  | awk -F= '{if ($2+0 > m) m=$2+0} END {print "  peak concurrent containers:", m}'
echo "full sampler log: /tmp/loadgen-resources.log"
echo "report: $REPORT"
