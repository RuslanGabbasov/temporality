#!/usr/bin/env python3
"""Break down an agent run's wall time into model / tool / platform overhead."""
import sys
from datetime import datetime

def parse(path):
    rows = []
    with open(path) as f:
        for line in f:
            parts = line.rstrip("\n").split("\t")
            if len(parts) < 7:
                continue
            typ, ts, lat, turn, tool, tokens, err = parts[:7]
            rows.append({
                "type": typ,
                "ts": datetime.fromisoformat(ts),
                "lat_ms": int(lat) if lat.isdigit() else None,
                "turn": turn,
                "tool": tool,
                "tokens": int(tokens) if tokens.isdigit() else None,
                "err": err,
            })
    return rows

def fmt_ms(ms):
    return f"{ms/1000:8.2f}s"

def main(path):
    rows = parse(path)
    if not rows:
        print("no events")
        return
    wall = (rows[-1]["ts"] - rows[0]["ts"]).total_seconds() * 1000

    model_ms = sum(r["lat_ms"] or 0 for r in rows if r["type"] == "model.completed")
    model_calls = [r for r in rows if r["type"] == "model.completed"]
    tool_done = [r for r in rows if r["type"] in ("tool.completed", "tool.failed")]
    tool_ms = sum(r["lat_ms"] or 0 for r in tool_done)

    print(f"events: {len(rows)}   wall: {fmt_ms(wall)}   "
          f"turns: {len(model_calls)}   tools: {len(tool_done)}")
    print()
    print("=== model calls ===")
    for r in model_calls:
        print(f"  turn {r['turn']:>3}: {fmt_ms(r['lat_ms'] or 0)}  {r['tokens'] or '?'} tok")
    print(f"  model total: {fmt_ms(model_ms)}  ({100*model_ms/wall:.0f}% of wall)")

    print()
    print("=== tool calls ===")
    for r in tool_done:
        print(f"  {r['tool'] or r['type']:<28} {fmt_ms(r['lat_ms'] or 0)}  {('ERR ' + r['err']) if r['type']=='tool.failed' else ''}")
    print(f"  tool total: {fmt_ms(tool_ms)}  ({100*tool_ms/wall:.0f}% of wall)")

    overhead = wall - model_ms - tool_ms
    print()
    print(f"=== PLATFORM OVERHEAD (wall - model - tools): {fmt_ms(overhead)}  ({100*overhead/wall:.0f}% of wall) ===")

    # per-turn gap: model.completed(N) -> model.started(N+1)
    print()
    print("=== inter-turn gaps (model.completed -> next model.started) ===")
    for i, r in enumerate(rows):
        if r["type"] != "model.completed":
            continue
        nxt = next((x for x in rows[i+1:] if x["type"] == "model.started"), None)
        if not nxt:
            continue
        gap_ms = (nxt["ts"] - r["ts"]).total_seconds() * 1000
        # tool time inside the gap
        t_ms = sum(x["lat_ms"] or 0 for x in rows[i+1:rows.index(nxt)]
                   if x["type"] in ("tool.completed", "tool.failed"))
        tools_in = [x["tool"] for x in rows[i+1:rows.index(nxt)]
                    if x["type"] in ("tool.started",)]
        print(f"  turn {r['turn']:>3} -> {nxt['turn']:>3}: gap {fmt_ms(gap_ms)}  "
              f"tools {fmt_ms(t_ms)}  net {fmt_ms(gap_ms - t_ms)}  [{', '.join(tools_in) or '-'}]")

    # biggest inter-event gaps overall
    print()
    print("=== top inter-event gaps ===")
    gaps = []
    for a, b in zip(rows, rows[1:]):
        gaps.append(((b["ts"] - a["ts"]).total_seconds() * 1000, a, b))
    for g, a, b in sorted(gaps, reverse=True)[:12]:
        if g < 1000:
            break
        print(f"  {fmt_ms(g)}  {a['type']}{(' · ' + a['tool']) if a['tool'] else ''} -> {b['type']}{(' · ' + b['tool']) if b['tool'] else ''}")

if __name__ == "__main__":
    main(sys.argv[1])
