#!/usr/bin/env python3
"""Analysis for the AML long-horizon experiment (experiment 2).

Reads the driver metrics dumps and the per-arm forensic databases and
produces the tables used in the report:
  - per-session aggregates per arm
  - asset trajectories (confidence/status per session)
  - recovery cost after the environment flip
  - sessions until stale suppression / new strategy dominance
  - lifecycle compliance (INJECTED -> REUSED -> VALIDATED/CONTRADICTED)

Usage:
  python3 scripts/aml_longhorizon_analysis.py --dump benchmarks/aml-main-run.json \
      --db-prefix aml2 --arms A,D --flip-at 6
"""

import argparse
import json
import subprocess
from collections import defaultdict

PG = ["docker", "exec", "temporality-postgres-1", "psql", "-U", "temporality",
      "-A", "-F", "\t", "-t"]


def psql(db, sql, ncols=None):
    cmd = PG + ["-d", db, "-c", sql]
    out = subprocess.run(cmd, capture_output=True, text=True)
    if out.returncode != 0:
        raise RuntimeError(f"psql {db} failed: {out.stderr}")
    rows = []
    for line in out.stdout.strip().split("\n"):
        if not line:
            continue
        parts = line.split("\t") if "\t" in line else line.split("|")
        if ncols and len(parts) < ncols:
            parts = parts + [""] * (ncols - len(parts))
        rows.append(parts)
    return rows


def load_dump(path):
    with open(path) as f:
        return json.load(f)


def session_of(session_id):
    # "D-s3" -> 3
    return int(session_id.rsplit("s", 1)[1])


def per_session_table(metrics, arms):
    print("\n=== Per-session aggregates ===")
    header = f"{'arm':>3} {'sess':>4} {'epoch':>5} {'ok':>4} {'steps':>5} {'calls':>5} {'fail':>4} {'rep':>3} {'tokens':>7} {'hints':>5} {'reused':>6} {'valid':>5} {'contr':>5} {'unres':>5}"
    print(header)
    for arm in arms:
        by_session = defaultdict(list)
        for m in metrics:
            if m["arm"] == arm:
                by_session[m["session"]].append(m)
        for session in sorted(by_session):
            rows = by_session[session]
            ok = sum(1 for r in rows if r["success"])
            steps = sum(r["steps"] for r in rows)
            calls = sum(r["tool_calls"] for r in rows)
            fail = sum(r["failed_calls"] for r in rows)
            rep = sum(r["repeated_failed"] for r in rows)
            tok = sum(r["total_tokens"] for r in rows)
            hints = sum(r["hints_injected"] for r in rows)
            reused = sum(r["reused"] for r in rows)
            valid = sum(r["validated"] for r in rows)
            contr = sum(r["contradicted"] for r in rows)
            unres = sum(r["unresolved"] for r in rows)
            epoch = rows[0]["epoch"]
            print(f"{arm:>3} {session:>4} {epoch:>5} {ok:>3}/{len(rows):<2} {steps:>5} {calls:>5} {fail:>4} {rep:>3} {tok:>7} {hints:>5} {reused:>6} {valid:>5} {contr:>5} {unres:>5}")


def billing_recovery_cost(metrics, arms, flip_at):
    """Recovery cost: billing tasks (T1, T5) failed calls and tokens after flip."""
    print(f"\n=== Recovery cost (billing tasks T1+T5, sessions >= {flip_at}) ===")
    print(f"{'arm':>3} {'sess':>4} {'billing fail':>12} {'billing tokens':>14} {'other fail':>10}")
    for arm in arms:
        for session in sorted({m["session"] for m in metrics if m["arm"] == arm}):
            if session < flip_at:
                continue
            bill = [m for m in metrics if m["arm"] == arm and m["session"] == session and m["task"] in ("T1", "T5")]
            other = [m for m in metrics if m["arm"] == arm and m["session"] == session and m["task"] not in ("T1", "T5")]
            bfail = sum(m["failed_calls"] for m in bill)
            btok = sum(m["total_tokens"] for m in bill)
            ofail = sum(m["failed_calls"] for m in other)
            print(f"{arm:>3} {session:>4} {bfail:>12} {btok:>14} {ofail:>10}")


def asset_trajectories(db):
    print("\n=== Asset trajectories (final state) ===")
    rows = psql(db, """
        select id, service, environment, version_context, kind,
               recommendation->>'param', recommendation->>'value',
               confidence, status, confirmation_count, contradiction_count,
               source_sessions, last_confirmed_session
        from aml_assets order by created_at
    """, ncols=13)
    for r in rows:
        (aid, service, env, ver, kind, param, value, conf, status,
         ccount, dcount, sessions, last_conf) = r
        print(f"  {service} [{env}/{ver}] {kind} {param}={value}: conf={conf} {status} "
              f"confirm={ccount} contradict={dcount} sessions={sessions} last={last_conf}")
    # Confidence evolution per asset from events.
    print("\n=== Confidence evolution (from REINFORCED/WEAKENED/EXTRACT events) ===")
    rows = psql(db, """
        select session_id, asset_id, event_type,
               payload->>'confidence', payload->>'confidence_before', payload->>'confidence_after'
        from aml_events
        where event_type in ('EXTRACT','REINFORCED','WEAKENED')
        order by id
    """, ncols=6)
    for session_id, aid, etype, conf, before, after in rows:
        detail = f"conf={conf}" if conf else f"{before} -> {after}"
        print(f"  {session_id} {aid[:8]} {etype:10} {detail}")


def stale_suppression(db, flip_session):
    print(f"\n=== Stale suppression (post-flip injections of pre-flip assets) ===")
    # assets created before flip session whose recommendation value is 'oauth' on billing
    rows = psql(db, f"""
        select e.session_id, a.id, a.recommendation->>'value', e.event_type
        from aml_events e join aml_assets a on a.id = e.asset_id
        where e.event_type = 'INJECTED' and a.service = 'billing-api'
        order by e.id
    """)
    for session_id, aid, value, _ in rows:
        marker = " <-- AFTER FLIP" if session_of(session_id) >= flip_session else ""
        print(f"  {session_id} asset={aid[:8]} value={value}{marker}")


def new_strategy_dominance(db):
    print("\n=== New strategy (pat) timeline ===")
    rows = psql(db, """
        select e.session_id, e.event_type
        from aml_events e join aml_assets a on a.id = e.asset_id
        where a.service='billing-api' and a.recommendation->>'value'='pat'
        order by e.id
    """)
    for session_id, etype in rows:
        print(f"  {session_id} {etype}")


def lifecycle_counts(db):
    print("\n=== Lifecycle events ===")
    rows = psql(db, "select event_type, count(*) from aml_events group by event_type order by event_type")
    total = {}
    for etype, count in rows:
        total[etype] = int(count)
        print(f"  {etype:14} {count}")
    return total


def compliance(metrics, arm):
    rows = [m for m in metrics if m["arm"] == arm]
    injected = sum(m["hints_injected"] for m in rows)
    reused = sum(m["reused"] for m in rows)
    return injected, reused


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dump", required=True)
    ap.add_argument("--db-prefix", required=True)
    ap.add_argument("--arms", default="A,D")
    ap.add_argument("--flip-at", type=int, default=6)
    args = ap.parse_args()

    metrics = load_dump(args.dump)
    arms = [a.strip() for a in args.arms.split(",")]

    per_session_table(metrics, arms)
    billing_recovery_cost(metrics, arms, args.flip_at)

    for arm in arms:
        if arm == "A":
            continue
        db = f"{args.db_prefix}_arm_{arm.lower()}"
        print(f"\n===== arm {arm} (db {db}) =====")
        lifecycle_counts(db)
        asset_trajectories(db)
        stale_suppression(db, args.flip_at)
        new_strategy_dominance(db)

    print("\n=== Compliance (injected vs reused) ===")
    for arm in arms:
        injected, reused = compliance(metrics, arm)
        print(f"  arm {arm}: injected={injected} reused={reused}")


if __name__ == "__main__":
    main()
