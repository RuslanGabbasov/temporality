#!/usr/bin/env python3
"""FRP Graph-Facts vs Investigation-Story vs Chat-Transcript experiment.

Question (TZ «Графовая память vs история расследования»): what exactly from a
past investigation does a transformer need to continue investigating —
structured graph facts, a compact sequential story, or a raw chat transcript?

Fixture: the existing real-repo benchmark (bleve v2.6.1). Episode 1 (T1,
levenshtein local bug) is already frozen in the canonical substrate
`frp_real_t1_cold`. Episode 2 solves T2 (numeric cross-module bug) in four
arms, all with the same model / limits / objective core:

  A cold  — fresh substrate, plain T2 objective (no memory at all)
  B facts — clone of S1, штатный FRP render: world_memory + procedures
            (mode=all, exactly how the runtime delivers durable memory)
  C story — fresh substrate, T2 objective + sequential investigation story
            distilled from S1 (events + cognitive_steps -> adapter)
  D chat  — fresh substrate, T2 objective + assistant/tool transcript of E1
            rebuilt from S1 (emissions + execution results)

Delivery for C/D is the objective text only — no runtime changes, no manual
prompt surgery: the story/transcript IS prior knowledge the agent walks in
with. B is the only arm that uses the штатный memory pipeline.

Usage:
  python3 scripts/realrepo_story_benchmark.py --smoke       # build texts + render check, no model
  python3 scripts/realrepo_story_benchmark.py               # 4 arms in parallel
  python3 scripts/realrepo_story_benchmark.py --arms story  # subset
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
from concurrent.futures import ThreadPoolExecutor

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from benchmark import compose_executor_running, env_or_fail, say, stop  # noqa: E402
import realrepo_benchmark as base  # noqa: E402

S1_DB = "frp_real_t1_cold"
STORY_DB_PREFIX = "frp_real_t2s"

ARMS = ("cold", "facts", "story", "chat")


# ---------------------------------------------------------------- source loading


def fetch_json(database, sql):
    raw = base.psql(database, sql)
    return json.loads(raw) if raw else None


def load_episode(db, episode_id):
    """Everything the story/transcript builders need, from the substrate only."""
    steps = fetch_json(db, f"""
        SELECT coalesce(jsonb_agg(row ORDER BY committed_at), '[]'::jsonb) FROM (
            SELECT cs.emission AS emission, cs.committed_at AS committed_at
            FROM cognitive_steps cs JOIN frames f ON f.frame_id = cs.parent_frame_id
            WHERE f.episode_id = '{episode_id}') row""")
    events = fetch_json(db, f"""
        SELECT coalesce(jsonb_agg(row ORDER BY tx_time, event_id), '[]'::jsonb) FROM (
            SELECT type AS type, tx_time AS tx_time, event_id AS event_id, payload AS payload
            FROM events WHERE episode_id = '{episode_id}'
              AND type IN ('world.observation','world.effect','execution.created',
                           'execution.completed','execution.failed','claim.supported',
                           'claim.refuted','claim.superseded')) row""")
    executions = fetch_json(db, f"""
        SELECT coalesce(jsonb_agg(row ORDER BY created_event), '[]'::jsonb) FROM (
            SELECT execution_id AS execution_id, affordance_id AS affordance_id,
                   data->'request_id' AS request_id, created_event AS created_event
            FROM executions WHERE episode_id = '{episode_id}') row""")
    objective_text = base.psql(db, f"SELECT data->>'text' FROM objectives WHERE episode_id = '{episode_id}' LIMIT 1")
    return {"steps": steps or [], "events": events or [], "executions": executions or [],
            "objective": objective_text or ""}


def execution_results(events, executions):
    """execution_id -> {affordance, emission_id, kind, text} from effect/failed/obs events."""
    results, meta = {}, {}
    for row in executions:
        meta[row["execution_id"]] = {"affordance": row.get("affordance_id"), "emission_id": None}
    for event in events:
        payload = event.get("payload") or {}
        inner = payload.get("payload") or {}
        execution_id = payload.get("execution_id") or inner.get("execution_id")
        if event["type"] == "execution.created":
            meta.setdefault(execution_id, {})["emission_id"] = payload.get("emission_id")
        elif event["type"] == "world.observation" and execution_id:
            kind = payload.get("observation_type")
            if kind == "file_content":
                text = f"прочитан файл {short_path(inner.get('path'))} ({inner.get('size')} байт)"
                results[execution_id] = {"kind": "read", "text": text, "content": inner.get("content") or "",
                                         "path": short_path(inner.get("path"))}
        elif event["type"] == "world.effect" and execution_id:
            effect = payload.get("effect_type")
            if effect == "process_run":
                stdout = inner.get("stdout") or ""
                exit_code = inner.get("exit_code")
                text = f"exit {exit_code}\n{stdout.strip()}"
                results[execution_id] = {"kind": "process", "text": text, "stdout": stdout,
                                         "command": inner.get("command"), "args": inner.get("args"), "exit_code": exit_code}
            else:
                summary = {"replacements": inner.get("replacements")}
                results[execution_id] = {"kind": "effect",
                                          "text": f"патч применён ({inner.get('replacements')} замен) в {short_path(inner.get('path'))}",
                                          "path": short_path(inner.get("path"))}
        elif event["type"] == "execution.failed":
            error = payload.get("error") or {}
            diagnostics = error.get("diagnostics") or {}
            stdout = (diagnostics.get("stdout") or "") + (diagnostics.get("stderr") or "")
            results[execution_id] = {"kind": "process", "text": f"exit {diagnostics.get('exit_code')}\n{stdout.strip()}",
                                     "stdout": stdout, "command": None, "args": None,
                                     "exit_code": diagnostics.get("exit_code")}
    for execution_id, info in meta.items():
        results.setdefault(execution_id, {"kind": "unknown", "text": "(результата нет)"})
        results[execution_id]["affordance"] = info.get("affordance")
        results[execution_id]["emission_id"] = info.get("emission_id")
    return results


def short_path(path):
    if not path:
        return "?"
    marker = "/ws-t1-"
    return path[path.index(marker) + len(marker):] if marker in path else path.rsplit("/", 1)[-1]


def cap(text, limit, marker="…"):
    text = text or ""
    if len(text) <= limit:
        return text
    lines = text.splitlines()
    if len(lines) > 40:
        return "\n".join(lines[:40]) + f"\n{marker} (обрезано, ещё {len(lines) - 40} строк)"
    return text[:limit] + marker


def fail_lines(stdout, limit=12):
    kept = [line.strip() for line in (stdout or "").splitlines()
            if "--- FAIL" in line or "FAIL" in line or "expected" in line.lower() or "Error" in line]
    return kept[:limit]


def bootstrap_facts(events):
    """Compact structural facts the fresh agent would otherwise rediscover.
    Only the workspace ROOT listing (not the recursive discovery) plus go.mod
    and README headers."""
    facts = []
    root_path = None
    for event in events:
        payload = event.get("payload") or {}
        if event["type"] == "world.observation" and payload.get("observation_type") == "stat" and not payload.get("execution_id"):
            inner = payload.get("payload") or {}
            if inner.get("directory"):
                root_path = inner.get("path")
    for event in events:
        payload = event.get("payload") or {}
        if event["type"] != "world.observation" or payload.get("execution_id") or not payload.get("ingestion_run_id"):
            continue
        inner = payload.get("payload") or {}
        kind = payload.get("observation_type")
        if kind == "directory_listing" and inner.get("path") == root_path:
            items = inner.get("items") or []
            dirs = [item["name"] for item in items if item.get("directory")]
            files = [item["name"] for item in items if not item.get("directory")]
            facts.append(f"Корень репозитория ({inner.get('entries')} записей): каталоги {', '.join(dirs)}; файлы {', '.join(files[:8])}"
                         + (" и др." if len(files) > 8 else ""))
        elif kind == "file_content":
            name = short_path(inner.get("path"))
            content = inner.get("content") or ""
            if name.endswith("go.mod"):
                module = next((line for line in content.splitlines() if line.startswith("module ")), "")
                gover = next((line for line in content.splitlines() if line.startswith("go ")), "")
                facts.append(f"go.mod: {module.strip()}; {gover.strip()}")
            elif name.upper().startswith("README"):
                first = next((line.strip() for line in content.splitlines()
                              if line.strip() and not line.strip().startswith(("#", "[!", "<", "-"))), "")
                if first:
                    facts.append(f"README: {cap(first, 160)}")
    return facts


def describe_action(action):
    name = action.get("affordance")
    args = action.get("args") or {}
    if name == "read_file":
        return f"read_file({short_path(args.get('path'))})"
    if name in ("write_file", "create_file"):
        return f"{name}({short_path(args.get('path'))})"
    if name == "patch_file":
        return f"patch_file({short_path(args.get('path'))})"
    if name in ("run_tests", "run_command"):
        parts = [str(args.get("command"))] + [str(a) for a in (args.get("args") or [])] if args.get("command") else [str(a) for a in (args.get("args") or [])]
        return f"{name}({' '.join(parts).strip()})"
    return f"{name}({json.dumps(args, ensure_ascii=False)[:120]})"


def pair_results(emission, actions, results):
    """Actions -> their execution results. The executor may run actions of one
    emission out of order, so file ops are paired by path, the rest sequentially."""
    pending = [result for result in results.values()
               if result.get("emission_id") == emission.get("emission_id")]
    pairs = []
    for action in actions:
        candidates = [r for r in pending if not r.get("_used") and
                      (r.get("affordance") == action.get("affordance") or not r.get("affordance"))]
        if not candidates:
            continue
        path = str((action.get("args") or {}).get("path") or "")
        chosen = next((r for r in candidates if path and (r.get("path") or "").endswith(path.lstrip("./"))), candidates[0])
        chosen["_used"] = True
        pairs.append((action, chosen))
    return pairs


def reasoning_block(emission, kinds=("answer", "inference", "constraint")):
    blocks = []
    for item in emission.get("reasoning") or []:
        if item.get("kind") in kinds and item.get("text"):
            blocks.append(f"({item['kind']}) {item['text'].strip()}")
    return blocks


# ---------------------------------------------------------------- builders


def build_story(episode, compact=False):
    """C: sequential investigation story — задача → разведка → шаги (гипотеза,
    рассуждение, действия, результаты) → итог. Generated from substrate data."""
    results = execution_results(episode["events"], episode["executions"])
    lines = []
    joiner = " " if compact else "\n"
    lines.append("Контекст: ниже — история предыдущего расследования в ЭТОМ ЖЕ репозитории (другая задача, уже решённая). "
                 "Она может помочь ориентироваться в репозитории, но не содержит ответа на текущую задачу.")
    lines.append(f"Задача того расследования: {episode['objective'].strip()}")
    facts = bootstrap_facts(episode["events"])
    if facts:
        lines.append("Первоначальный осмотр репозитория (автоматическая разведка):")
        lines.extend(f"- {fact}" for fact in facts)
    lines.append("Ход расследования:")
    for index, step in enumerate(episode["steps"], 1):
        emission = step["emission"]
        parts = []
        claims = [c for c in (emission.get("claims") or []) if c.get("proposition")]
        for claim in claims:
            parts.append(f"Гипотеза (уверенность {claim.get('confidence')}): {claim['proposition'].strip()}")
        if not compact:
            parts.extend(reasoning_block(emission))
        actions = emission.get("actions") or []
        if actions:
            parts.append("Действия: " + "; ".join(describe_action(a) for a in actions))
        for action, result in pair_results(emission, actions, results):
            if result["kind"] == "process":
                if result.get("exit_code") == 0:
                    summary = "exit 0 — успех"
                    stdout = result.get("stdout") or ""
                    if "ok  " in stdout:
                        summary += f"; все пакеты ok ({len([l for l in stdout.splitlines() if l.startswith('ok')])} пакетов)"
                else:
                    fails = fail_lines(result.get("stdout"))
                    summary = "FAILED: " + ("; ".join(fails[:4]) if fails else cap(result.get("stdout") or "", 200))
                parts.append(f"Результат {describe_action(action)} → {summary}")
            elif result["kind"] == "read":
                parts.append(f"Результат read_file → {result['text']}")
            elif result["kind"] == "effect":
                parts.append(f"Результат {describe_action(action)} → {result['text']}")
        for op in emission.get("claim_ops") or []:
            if op.get("op") == "confirm":
                parts.append("Гипотеза подтверждена результатами проверок.")
        if emission.get("completion"):
            parts.append(f"Итог расследования: {emission['completion'].strip()}")
        header = f"Шаг {index}: " if compact else f"Шаг {index}:"
        lines.append(header + joiner.join(parts) if compact else header + "\n  " + "\n  ".join(parts))
    return "\n".join(lines)


def build_transcript(episode):
    """D: assistant/tool transcript of E1, as a classic chat harness would keep it."""
    results = execution_results(episode["events"], episode["executions"])
    lines = ["Контекст: ниже — транскрипт предыдущего расследования в ЭТОМ ЖЕ репозитории (другая задача, уже решённая), "
             "в формате «assistant / tool». Он может помочь ориентироваться в репозитории, но не содержит ответа на текущую задачу."]
    lines.append(f"[задача] {episode['objective'].strip()}")
    for step in episode["steps"]:
        emission = step["emission"]
        assistant = []
        assistant.extend(reasoning_block(emission))
        for claim in emission.get("claims") or []:
            if claim.get("proposition"):
                assistant.append(f"Гипотеза (уверенность {claim.get('confidence')}): {claim['proposition'].strip()}")
        lines.append("\nassistant:\n" + "\n".join(assistant))
        for action, result in pair_results(emission, emission.get("actions") or [], results):
            if result["kind"] == "read":
                body = cap(result.get("content") or result["text"], 6000)
            elif result["kind"] == "process":
                body = cap(result.get("text") or "", 2500)
            else:
                body = result.get("text") or ""
            mark = f" (exit {result['exit_code']})" if result.get("exit_code") is not None else ""
            lines.append(f"\ntool> {describe_action(action)}{mark}:\n{body.strip()}")
        if emission.get("completion"):
            lines.append("\nassistant (итог):\n" + emission["completion"].strip())
    return "\n".join(lines)


# ---------------------------------------------------------------- smoke


def smoke(args, story, transcript):
    head = base.head
    head("Smoke: story / transcript built from S1")
    say(f"story      : {len(story)} chars (~{len(story) // 4} tokens)")
    say(f"transcript : {len(transcript)} chars (~{len(transcript) // 4} tokens)")
    say("--- story preview " + "-" * 50)
    say(story)
    say("--- transcript preview " + "-" * 46)
    say(transcript[:6000])
    if len(transcript) > 6000:
        say(f"… (+{len(transcript) - 6000} chars)")

    head("Smoke: render does not trim the long objective")
    task = dict(base.TASKS[1])
    database = f"{STORY_DB_PREFIX}_smoke"
    parent = tempfile.mkdtemp(prefix="temporality-story-smoke-")
    runtime_, run_base = None, None
    try:
        workspace, _ = base.prepare_workspace(args.source, task, parent)
        base.reset_database(database)
        runtime_, run_base = base.spawn_runtime(args.port, database)
        base.register_world(run_base, base.WORLD_ID, workspace, 1)
        for name, context in (("story", story), ("chat", transcript)):
            objective = task["objective"] + "\n\n" + context
            status, boot = base.request_status("POST", "/v1/bootstrap", {
                "world_id": base.WORLD_ID, "objective_text": objective, "budget_tokens": args.budget_tokens,
            }, base=run_base)
            if status != 201:
                say(f"[{name}] bootstrap FAILED: {status} {json.dumps(boot, ensure_ascii=False)[:300]}")
                continue
            status, packet = base.request_status("POST", "/v1/render", {
                "frame_id": boot["frame_id"], "objective_id": boot["objective_id"], "budget_tokens": args.budget_tokens,
            }, base=run_base)
            if status != 200:
                say(f"[{name}] render FAILED: {status} {json.dumps(packet, ensure_ascii=False)[:300]}")
                continue
            goal_text = ""
            for section in packet.get("sections") or []:
                if section.get("kind") == "objective":
                    goal_text = json.dumps(section.get("items"), ensure_ascii=False)
            tail = context.strip()[-60:]
            fully = tail in goal_text
            say(f"[{name}] bootstrap=201 render=200 objective_in_render={fully} "
                f"(est_tokens={packet.get('token_usage', {}).get('estimated')} / budget={args.budget_tokens})")
            if not fully:
                say(f"[{name}] tail expected in render: …{tail}")
    finally:
        if runtime_:
            stop(runtime_)
        shutil.rmtree(parent, ignore_errors=True)


# ---------------------------------------------------------------- experiment


def run_story_arm(args, arm, story, transcript, parent, port):
    task = dict(base.TASKS[1])
    if arm == "story":
        task["objective"] = task["objective"] + "\n\n" + story
    elif arm == "chat":
        task["objective"] = task["objective"] + "\n\n" + transcript
    database = f"{STORY_DB_PREFIX}_{arm}"
    workspace, initial_sha = base.prepare_workspace(args.source, base.TASKS[1], parent)
    label = f"T2-{arm}"

    def attempt(n):
        base.git_restore(workspace, initial_sha)
        if arm == "facts":
            base.clone_database(S1_DB, database)
        else:
            base.reset_database(database)
        runtime_, run_base = base.spawn_runtime(port, database)
        try:
            if arm == "facts":
                say(f"  [{label}#{n}] substrate={database} clone-of={S1_DB} (mode=all, штатный world_memory+procedures)")
                return base.run_warm(args, run_base, database, workspace, base.WORLD_ID, label, task, initial_sha, world_memory_mode="all")
            say(f"  [{label}#{n}] fresh substrate={database} objective+{ 'story' if arm == 'story' else 'transcript' if arm == 'chat' else 'plain' }")
            return base.run_cold(args, run_base, database, workspace, base.WORLD_ID, label, task, initial_sha)
        finally:
            stop(runtime_)

    metrics = base.run_arm_with_retries(label, attempt, args.attempts, retry_on_failure=True)
    metrics["durable_memory"] = base.world_memory_snapshot(database, base.WORLD_ID)
    base.say_arm_result(metrics)
    return metrics


def run_story_arm_safely(label, attempt_fn):
    """One arm's fatal error must not kill sibling arms (infra included)."""
    try:
        return attempt_fn()
    except SystemExit as fatal:
        say(f"  [{label}] FATAL: {fatal}")
    except Exception:  # noqa: BLE001 - isolate infra failures to this arm
        import traceback
        say(f"  [{label}] FATAL (unexpected):\n{traceback.format_exc(limit=8)}")
    return {"phase": label, "task": "T2", "success": False, "fatal": "arm aborted",
                "steps": 0, "total_tokens": 0, "prompt_tokens": 0, "completion_tokens": 0, "seconds": 0,
                "read_file": 0, "affordance_requests": 0, "run_tests": 0, "writes": 0,
                "repeated_read_paths": 0, "distinct_read_paths": 0, "claims_born_candidate": 0,
                "claims_confirmed": 0, "claims_refuted": 0, "claim_ops_on_memory": 0,
                "claims_echoing_memory": 0, "memory_claim_refs_seen": 0, "stale_file_reads": 0,
                "stale_symbol_claims": 0, "world_memory_states_last": {}, "world_memory_items": [],
                "procedure_items": [], "degenerate": False, "timeout": False, "timeline": []}


def report(metrics_by_arm, story, transcript, args, env):
    base.head("Results")
    rows = []
    for arm in ARMS:
        run = metrics_by_arm.get(f"T2-{arm}")
        if not run:
            continue
        rows.append({
            "arm": arm, "success": run["success"], "steps": run["steps"], "total_tokens": run["total_tokens"],
            "prompt_tokens": run["prompt_tokens"], "completion_tokens": run["completion_tokens"],
            "seconds": run["seconds"], "reads": run["read_file"], "distinct": run.get("distinct_read_paths", 0),
            "repeat_reads": run.get("repeated_read_paths", 0), "run_tests": run.get("run_tests", 0),
            "writes": run.get("writes", 0), "discovery_actions": run.get("discovery_actions", 0),
            "mem_refs": run.get("memory_claim_refs_seen", 0), "ops_on_mem": run.get("claim_ops_on_memory", 0),
            "revisits": run.get("refuted_revisits", 0), "degen": run.get("degenerate", False),
            "timeout": run.get("timeout", False), "att": run.get("attempt", 1),
        })
    columns = ["arm", "success", "steps", "total_tokens", "seconds", "reads", "distinct", "repeat_reads",
               "run_tests", "writes", "discovery_actions", "mem_refs", "ops_on_mem", "revisits", "degen", "att", "tmo"]
    widths = {name: max(len(name), *(len(str(row.get(name))) for row in rows)) if rows else len(name) for name in columns}
    say("  ".join(name.ljust(widths[name]) for name in columns))
    for row in rows:
        say("  ".join(str(row.get(name)).ljust(widths[name]) for name in columns))
    cold = metrics_by_arm.get("T2-cold", {}).get("total_tokens") or 0
    if cold:
        base.head("Ratios vs cold (total tokens)")
        for arm in ARMS:
            run = metrics_by_arm.get(f"T2-{arm}")
            if run and run["total_tokens"]:
                say(f"  {arm:6s} {run['total_tokens']:>8d}  {run['total_tokens'] / cold:.2f}x  success={run['success']}")
    return {"rows": rows, "ratios_vs_cold": {arm: (metrics_by_arm.get(f"T2-{arm}", {}).get("total_tokens") or 0) / cold
                                             for arm in ARMS if cold and metrics_by_arm.get(f"T2-{arm}")},
            "context_sizes": {"story_chars": len(story), "transcript_chars": len(transcript)},
            "model": env.get("TEMPORALITY_MODEL_ID"), "args": {k: v for k, v in vars(args).items()}}


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--source", default=base.DEFAULT_SOURCE)
    parser.add_argument("--arms", default="cold,facts,story,chat")
    parser.add_argument("--max-steps", type=int, default=16)
    parser.add_argument("--budget-tokens", type=int, default=16000)
    parser.add_argument("--port", type=int, default=18200, help="base port; arms use port..port+3 (default 18200)")
    parser.add_argument("--degenerate-after", type=int, default=6)
    parser.add_argument("--time-limit", type=int, default=1500)
    parser.add_argument("--attempts", type=int, default=2)
    parser.add_argument("--serial-arms", action="store_true")
    parser.add_argument("--smoke", action="store_true", help="build texts + render smoke test, no model calls")
    parser.add_argument("--keep", action="store_true")
    parser.add_argument("--dump", default=None)
    args = parser.parse_args()

    if compose_executor_running():
        sys.exit("compose executor is running and will steal executions: run `docker compose stop executor` first")
    if not os.path.exists(base.RUNTIME_BINARY) or not os.path.exists(base.EXECUTOR_BINARY):
        sys.exit("binaries not found: run `make build`")
    base.PROJECT_ROOT = os.getcwd()
    go = os.environ.get("TEMPORALITY_GO_BINARY", "go")
    if shutil.which(go) is None and os.path.exists("/usr/local/go/bin/go"):
        os.environ["PATH"] = "/usr/local/go/bin:" + os.environ.get("PATH", "")
    if shutil.which(go) is None:
        sys.exit("go toolchain not found")
    selected_arms = [arm.strip() for arm in args.arms.split(",") if arm.strip() in ARMS]
    if not selected_arms:
        sys.exit(f"no arms matched {args.arms}")

    episode_id = base.psql(S1_DB, "SELECT DISTINCT episode_id FROM events WHERE episode_id IS NOT NULL LIMIT 1")
    if not episode_id:
        sys.exit(f"canonical substrate {S1_DB} has no episode — cannot build story/transcript")
    episode = load_episode(S1_DB, episode_id)
    say(f"E1 in {S1_DB}: episode={episode_id} steps={len(episode['steps'])} events={len(episode['events'])}")
    story = build_story(episode)
    transcript = build_transcript(episode)

    if args.smoke:
        smoke(args, story, transcript)
        return

    env = env_or_fail()
    head_sha = subprocess.run(["git", "-C", args.source, "rev-parse", "HEAD"], capture_output=True, text=True)
    if head_sha.stdout.strip() != base.BASE_SHA:
        sys.exit(f"source repo is not at the pinned commit {base.BASE_SHA}")

    base.head(f"0. Preconditions: model={env['TEMPORALITY_MODEL_ID']} arms={selected_arms} story~{len(story)//4}tok transcript~{len(transcript)//4}tok")
    dump = args.dump or time.strftime("benchmarks/realrepo-story-%Y%m%d-%H%M%S.json")
    os.makedirs("benchmarks", exist_ok=True)

    parent = tempfile.mkdtemp(prefix="temporality-story-")
    metrics_by_arm = {}
    try:
        jobs = [(arm, lambda arm=arm, port=args.port + index: run_story_arm_safely(
                    f"T2-{arm}", lambda arm=arm, port=port: run_story_arm(args, arm, story, transcript, parent, port)))
                for index, arm in enumerate(selected_arms)]
        if args.serial_arms or len(jobs) == 1:
            metrics_by_arm = {}
            for arm, fn in jobs:
                metrics_by_arm[f"T2-{arm}"] = fn()
                with open(dump, "w") as handle:
                    json.dump({"metrics": metrics_by_arm, "partial": True}, handle, ensure_ascii=False, indent=2)
        else:
            import concurrent.futures as futures_mod
            with ThreadPoolExecutor(max_workers=len(jobs)) as pool:
                submitted = {pool.submit(fn): f"T2-{arm}" for arm, fn in jobs}
                for done in futures_mod.as_completed(submitted):
                    label = submitted[done]
                    metrics_by_arm[label] = done.result()
                    say(f"  arm finished: {label} — partial dump saved")
                    with open(dump, "w") as handle:
                        json.dump({"metrics": metrics_by_arm, "partial": True}, handle, ensure_ascii=False, indent=2)
        summary = report(metrics_by_arm, story, transcript, args, env)
        with open(dump, "w") as handle:
            json.dump({"metrics": metrics_by_arm, "summary": summary,
                       "story": story, "transcript": transcript,
                       "source_episode": {"db": S1_DB, "episode_id": episode_id}}, handle, ensure_ascii=False, indent=2)
        say(f"\nmetrics : {dump}")
    finally:
        if not args.keep:
            shutil.rmtree(parent, ignore_errors=True)


if __name__ == "__main__":
    main()
