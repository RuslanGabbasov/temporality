#!/usr/bin/env python3
"""Token/time cost benchmark: Temporality render-loop vs classic harness.

Reads persisted render packets, emissions, and events for real episodes and
reconstructs what a classic (system + accumulating conversation) harness would
have spent on the same trajectory. Token estimate: chars/4 (same heuristic the
renderer itself uses).

Usage: python3 scripts/token_benchmark.py [episode_id ...]
       Without arguments, analyzes every episode that has cognitive steps.
"""
import json
import subprocess
import sys

PSQL = ["docker", "exec", "temporality-postgres-1", "psql", "-U", "temporality", "-d", "temporality", "-t", "-A", "-c"]

SYSTEM_PROMPT = None  # filled from frp/model/model.go below

# Classic harness sends tool definitions with every request; Temporality has
# no tools at all (affordances ride inside the render packet). Rough OpenAI-
# style tool-schema cost for the affordance set used in the demo episodes.
TOOL_DEFS_TOKENS = 1500


def psql_json(query):
    raw = subprocess.run(PSQL + [query], capture_output=True, text=True, check=True).stdout.strip()
    return json.loads(raw) if raw else None


def psql_rows(query):
    raw = subprocess.run(PSQL + [query], capture_output=True, text=True, check=True).stdout.strip()
    return [line.split("|") for line in raw.splitlines()] if raw else []


def tokens(text):
    return max(1, (len(text.encode()) + 3) // 4)


def load_system_prompt():
    source = open("frp/model/model.go").read()
    start = source.index('const systemPrompt = `') + len('const systemPrompt = `')
    end = source.index("`", start)
    return source[start:end]


def analyze(episode_id):
    steps = psql_rows(
        f"SELECT cs.render_packet, cs.emission FROM cognitive_steps cs "
        f"JOIN frames f ON f.frame_id = cs.parent_frame_id "
        f"WHERE f.episode_id = '{episode_id}' ORDER BY cs.committed_at"
    )
    if not steps:
        return None
    events = psql_json(
        f"SELECT json_agg(e ORDER BY e.valid_time, e.event_id)::text FROM "
        f"(SELECT * FROM events WHERE episode_id = '{episode_id}' ORDER BY valid_time, event_id) e"
    )
    objective_row = psql_rows(f"SELECT data->>'text' FROM objectives WHERE episode_id = '{episode_id}' LIMIT 1")
    objective_text = objective_row[0][0] if objective_row else "objective"

    system = load_system_prompt()
    rows = []
    classic_prefix = tokens(system) + tokens(objective_text) + TOOL_DEFS_TOKENS
    committed = [row[0] for row in psql_rows(
        f"SELECT cs.committed_at FROM cognitive_steps cs JOIN frames f ON f.frame_id = cs.parent_frame_id "
        f"WHERE f.episode_id = '{episode_id}' AND cs.render_packet IS NOT NULL ORDER BY cs.committed_at"
    )]
    started = psql_rows(f"SELECT min(valid_time)::text FROM events WHERE episode_id = '{episode_id}'")
    for index, (packet_raw, emission_raw) in enumerate(steps, start=1):
        if not packet_raw or not emission_raw:
            continue  # legacy step rows without persisted packets/emissions
        packet = json.loads(packet_raw)
        emission = json.loads(emission_raw)
        # --- temporality: exactly what was sent to the model
        frp_input = tokens(json.dumps(packet))
        frp_output = tokens(json.dumps(emission))
        sections = {s["kind"]: tokens(json.dumps(s["items"])) for s in packet.get("sections", [])}
        overhead = packet.get("token_usage", {}).get("estimated")
        # --- classic harness on the same trajectory: system+objective+all
        # observations/assistant turns so far, appended verbatim (no budget cap)
        events_so_far = events[: index_of_step(events, index, len(steps))]
        tool_results = sum(tokens(json.dumps(ev.get("payload"))) for ev in events_so_far if ev["type"] == "world.observation")
        assistant_turns = index * tokens(json.dumps(emission))
        classic_input = classic_prefix + tool_results + assistant_turns
        rows.append({
            "step": index,
            "frp_input": frp_input,
            "frp_estimated": overhead,
            "frp_output": frp_output,
            "classic_input": classic_input,
            "sections": sections,
            "seconds": step_seconds(committed, index, started[0][0] if started else None),
        })
    return {"episode": episode_id, "rows": rows, "total_events": len(events)}


def index_of_step(events, index, total_steps):
    """Map step number to a plausible event-prefix length: split events evenly
    across steps (observation timing per step is not stored on the step row)."""
    if total_steps == 0:
        return 0
    return round(len(events) * index / total_steps)


def step_seconds(committed, index, episode_start):
    """Wall-clock of step N = gap between commit(N-1) and commit(N); the first
    step is measured from the episode's first event."""
    from datetime import datetime
    parse = lambda value: datetime.fromisoformat(value.replace("+00", "+00:00").replace(" ", "T")) if " " in value else datetime.fromisoformat(value)
    try:
        end = parse(committed[index - 1])
        begin = parse(committed[index - 2]) if index >= 2 else parse(episode_start)
        return round((end - begin).total_seconds(), 1)
    except (ValueError, IndexError):
        return None


def main():
    ids = sys.argv[1:]
    if not ids:
        ids = [row[0] for row in psql_rows(
            "SELECT DISTINCT f.episode_id FROM cognitive_steps cs JOIN frames f ON f.frame_id = cs.parent_frame_id"
        )]
    for episode_id in ids:
        result = analyze(episode_id)
        if not result:
            print(f"episode {episode_id}: no steps, skipped")
            continue
        print(f"\n=== episode {result['episode'][:8]}… ({len(result['rows'])} steps, {result['total_events']} events) ===")
        print(f"{'step':>4} {'frp in':>8} {'frp out':>8} {'classic in':>10} {'ratio':>6} {'sec':>6}   biggest sections")
        frp_total = classic_total = 0
        for row in result["rows"]:
            frp_total += row["frp_input"] + row["frp_output"]
            classic_total += row["classic_input"] + row["frp_output"]
            top = sorted(row["sections"].items(), key=lambda kv: -kv[1])[:3]
            top = ", ".join(f"{k}:{v}" for k, v in top)
            ratio = row["classic_input"] / row["frp_input"] if row["frp_input"] else 0
            print(f"{row['step']:>4} {row['frp_input']:>8} {row['frp_output']:>8} {row['classic_input']:>10} {ratio:>5.1f}x {str(row['seconds'] or '-'):>6}   {top}")
        print(f"totals: temporality={frp_total} tokens, classic={classic_total} tokens")
        project(result, frp_total, classic_total)


def project(result, frp_total, classic_total):
    """Extrapolate per-step input cost to a 50-step session: frp is capped by
    budget_tokens (flat), classic accumulates every observation verbatim."""
    rows = result["rows"]
    if len(rows) < 2:
        return
    frp_flat = rows[-1]["frp_input"]
    growth = (rows[-1]["classic_input"] - rows[0]["classic_input"]) / max(1, len(rows) - 1)
    classic_flat = rows[-1]["classic_input"]
    crossover = None
    line = []
    for n in (1, 5, 10, 25, 50):
        classic_n = classic_flat + growth * (n - len(rows))
        frp_n = frp_flat
        if crossover is None and classic_n > frp_n:
            crossover = n
        line.append(f"n={n}: frp≈{frp_n/1000:.0f}K classic≈{classic_n/1000:.0f}K")
    print("projection (input tokens/step): " + "; ".join(line))
    if crossover:
        print(f"classic overtakes frp around step {crossover} on this workload")


if __name__ == "__main__":
    main()
