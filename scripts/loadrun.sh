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
#   --heavy  heavier workload: 10s sandbox commands (concurrent containers),
#            1.5s token gaps (long streams), ask_human pause in the middle —
#            the profile that stresses sandboxes, journal write volume and
#            the token bus at once.
#
# The mock provider is the compose service `mockllm` (profile loadtest): the
# script recreates it with MOCKLLM_* env for the chosen profile, so a --heavy
# run can never silently inherit the previous run's settings.
set -eu

# Compose and go build resolve paths relative to the repo root.
cd "$(dirname "$0")/.."

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
cleanup() {
  [ -n "$SAMPLER_PID" ] && kill "$SAMPLER_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "== 1/3 build tools (requires Go) =="
go build -o /tmp/loadgen-runner ./examples/loadgen

echo "== 2/3 mock profile + preflight =="

# apply_mock_profile recreates the mockllm compose service with the profile
# env (shell env wins over .env in compose interpolation). Deterministic
# campaigns: every invocation pins all five knobs, including the light
# default, so a plain run after a --heavy one is never polluted.
apply_mock_profile() {
  first="$1"; gap="$2"; tokens="$3"; command="$4"; ask="$5"
  export MOCKLLM_FIRST_TOKEN="$first" MOCKLLM_TOKEN_GAP="$gap" MOCKLLM_TOKENS="$tokens"
  export MOCKLLM_TOOL_COMMAND="$command" MOCKLLM_ASK_AFTER_TOOLS="$ask"
  echo "   mock profile: first-token=$first token-gap=$gap tokens=$tokens ask-after-tools=$ask"
  echo "   recreating the mockllm compose service…"
  docker compose -f docker-compose.yml -f docker-compose.dev.yml --profile loadtest up -d --force-recreate --no-deps mockllm >/dev/null
  cid=$(docker compose -f docker-compose.yml -f docker-compose.dev.yml --profile loadtest ps -q mockllm)
  if [ -z "$cid" ]; then
    echo "   ERROR: mockllm container not found after up — is the loadtest profile defined here?" >&2
    return 1
  fi
  # A container that was not actually recreated silently measures the wrong
  # profile (a whole campaign wasted): assert the env and the process boot
  # line, not just the health endpoint.
  env_gap=$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$cid" | sed -n 's/^MOCKLLM_TOKEN_GAP=//p')
  if [ "$env_gap" != "$gap" ]; then
    echo "   ERROR: mockllm env MOCKLLM_TOKEN_GAP=$env_gap, expected $gap — container was not recreated with the profile" >&2
    return 1
  fi
  boot=$(docker logs "$cid" 2>&1 | grep -m1 'mockllm on' || true)
  echo "   mock boot: ${boot:-<no log>}"
  case "$boot" in
    *"token-gap=$gap"*) : ;;
    *) echo "   ERROR: mock boot line does not match the requested profile (token-gap=$gap)" >&2; return 1 ;;
  esac
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    health=$(curl -s -m 2 "$MOCK_URL/healthz" || true)
    case "$health" in *ok*) return 0 ;; esac
    sleep 1
  done
  echo "   ERROR: mock provider did not become healthy after recreate" >&2
  return 1
}

if [ "$HEAVY" = "1" ]; then
  # Durations in the Go-normalized form: the mock logs its config through
  # time.Duration.String() (1500ms prints as 1.5s) — the boot assertion below
  # compares strings, so the profile must be written the way Go prints it.
  apply_mock_profile 1s 1.5s 300 "sleep 10 && echo heavy" 2
  hitl_flags="-hitl -human-delay 45s"
  PROFILE="heavy"
elif [ "$HITL" = "1" ]; then
  apply_mock_profile 200ms 15ms 120 "echo loadgen step" 2
  hitl_flags="-hitl -human-delay 30s"
  PROFILE="hitl"
else
  apply_mock_profile 200ms 15ms 120 "echo loadgen step" 0
  hitl_flags=""
  PROFILE="light"
fi
MOCK_HEALTH=$(curl -s -m 5 "$MOCK_URL/healthz" || true)
case "$MOCK_HEALTH" in
  *ok*) echo "   mock provider: up ($MOCK_URL)" ;;
  *)    echo "   WARNING: mock provider not reachable at $MOCK_URL — check the loadtest profile"; exit 1 ;;
esac
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

# Sampler runs for the whole campaign (step 3/3 below); the mock was already
# verified by apply_mock_profile.
(
  while :; do
    echo "=== $(date +%H:%M:%S) containers=$(docker ps -q | wc -l)"
    docker stats --no-stream --format '{{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}' \
      | grep -E 'agent-kernel|journal|postgres|mockllm|debugger' || true
    sleep 1
  done
) > /tmp/loadgen-resources.log &
SAMPLER_PID=$!

echo "== 3/3 load: $RUNS runs, ramp ${RAMP}s, profile=$PROFILE =="
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
  -profile "$PROFILE" \
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

# Traffic proof: the mock's own counters must show the campaign. An idle
# counter means the kernel's provider points somewhere else (a stale host
# process, another instance) — the report above measured the wrong provider.
metrics=$(curl -s -m 5 "$MOCK_URL/metrics" || true)
mock_total=$(echo "$metrics" | sed -n 's/.*"total_requests":\([0-9]*\).*/\1/p')
mock_final=$(echo "$metrics" | sed -n 's/.*"final_answers":\([0-9]*\).*/\1/p')
mock_ask=$(echo "$metrics" | sed -n 's/.*"tool_calls_issued":\([0-9]*\).*/\1/p')
if [ -n "$mock_total" ] && [ "$mock_total" -ge "$RUNS" ] 2>/dev/null; then
  echo "mock provider traffic: total_requests=$mock_total tool_calls=$mock_ask final_answers=$mock_final (runs=$RUNS)"
elif [ -n "$mock_total" ]; then
  echo "WARNING: mock provider counters look idle (total_requests=$mock_total for $RUNS runs) — the kernel's workspace provider likely points to a different mock/base URL; the campaign did not exercise this container."
  echo "         Check the provider base URL in the UI (Настройки → Провайдеры) or TEMPORALITY_MODEL_BASE_URL."
fi
echo "report: $REPORT"
