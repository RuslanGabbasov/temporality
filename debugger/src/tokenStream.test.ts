import { describe, expect, it } from 'vitest'
import { TokenMeter } from './tokenStream'

describe('TokenMeter', () => {
  it('accumulates deltas into approximate tokens', () => {
    const meter = new TokenMeter()
    meter.apply({ type: 'delta', turn: 3, text: 'a'.repeat(40) }, 1_000)
    meter.apply({ type: 'delta', turn: 3, text: 'b'.repeat(40) }, 2_000)
    const m = meter.metrics(2_500)
    expect(m.approxTokens).toBe(20) // 80 chars / 4
    expect(m.lastChunkAgoMs).toBe(500)
    expect(m.streaming).toBe(true)
  })

  it('counts reasoning text as stream life', () => {
    const meter = new TokenMeter()
    meter.apply({ type: 'delta', turn: 1, reasoning: 'thinking...' }, 1_000)
    expect(meter.metrics(1_100).approxTokens).toBeGreaterThan(0)
    expect(meter.metrics(1_100).streaming).toBe(true)
  })

  it('counts streamed tool-call args as stream life and exposes the tool', () => {
    const meter = new TokenMeter()
    meter.apply({ type: 'delta', turn: 5, tool: 'run_command', args: '{"command":' }, 1_000)
    meter.apply({ type: 'delta', turn: 5, args: '["go","test"]}' }, 2_000)
    const m = meter.metrics(2_500)
    // 27 chars of args / 4 — the tool-only turn is visible as streaming.
    expect(m.approxTokens).toBeGreaterThan(0)
    expect(m.streaming).toBe(true)
    expect(m.tool).toBe('run_command')
  })

  it('clears the tool when the model moves on to answer text', () => {
    const meter = new TokenMeter()
    meter.apply({ type: 'delta', turn: 5, tool: 'plan', args: '{"goal":' }, 1_000)
    meter.apply({ type: 'delta', turn: 5, text: 'Here is the answer' }, 2_000)
    expect(meter.metrics(2_000).tool).toBeUndefined()
  })

  it('snapshot carries tool args from a mid-tool-call reconnect', () => {
    const meter = new TokenMeter()
    meter.apply({ type: 'snapshot', turn: 5, tool: 'plan', args: 'x'.repeat(400) }, 1_000)
    const m = meter.metrics(1_000)
    expect(m.approxTokens).toBe(100)
    expect(m.streaming).toBe(true)
    expect(m.tool).toBe('plan')
  })

  it('resets on turn change', () => {
    const meter = new TokenMeter()
    meter.apply({ type: 'delta', turn: 1, text: 'x'.repeat(100) }, 1_000)
    meter.apply({ type: 'delta', turn: 2, text: 'y'.repeat(20) }, 2_000)
    expect(meter.metrics(2_000).approxTokens).toBe(5) // 20 chars / 4
  })

  it('snapshot replaces accumulated state', () => {
    const meter = new TokenMeter()
    meter.apply({ type: 'delta', turn: 1, text: 'short' }, 1_000)
    meter.apply({ type: 'snapshot', turn: 1, text: 'z'.repeat(400) }, 2_000)
    expect(meter.metrics(2_000).approxTokens).toBe(100)
  })

  it('estimates the rate over the rolling window only', () => {
    const meter = new TokenMeter()
    // Burst at t=0 far outside the 5s window, then a steady trickle.
    meter.apply({ type: 'delta', turn: 1, text: 'a'.repeat(4000) }, 0)
    meter.apply({ type: 'delta', turn: 1, text: 'b'.repeat(40) }, 10_000)
    meter.apply({ type: 'delta', turn: 1, text: 'c'.repeat(40) }, 12_000)
    const m = meter.metrics(12_500)
    // Window holds the two recent 40-char chunks; the span runs from the first
    // window sample to now (2.5s), so the trailing idle drags the rate: 80/4/2.5 = 8.
    expect(m.tps).toBe(8)
    // Total is unaffected by the window.
    expect(m.approxTokens).toBe(1020)
  })

  it('reports zero rate before the window spans half a second', () => {
    const meter = new TokenMeter()
    meter.apply({ type: 'delta', turn: 1, text: 'a'.repeat(400) }, 1_000)
    expect(meter.metrics(1_100).tps).toBe(0)
  })

  it('marks lastChunkAgo infinite before the first chunk', () => {
    const meter = new TokenMeter()
    expect(meter.metrics(5_000).lastChunkAgoMs).toBe(Number.POSITIVE_INFINITY)
    expect(meter.metrics(5_000).streaming).toBe(false)
  })

  it('stops streaming on done', () => {
    const meter = new TokenMeter()
    meter.apply({ type: 'delta', turn: 1, text: 'x'.repeat(40) }, 1_000)
    meter.apply({ type: 'done' }, 2_000)
    const m = meter.metrics(2_000)
    expect(m.streaming).toBe(false)
    // Token count survives: the final row still shows what streamed.
    expect(m.approxTokens).toBe(10)
  })
})
