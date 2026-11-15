import { describe, expect, it } from 'vitest'
import { priceRows, buildPricePayload } from './modelPrices'

// The token-price editor (docs/plan-cost-accounting.md): which rows exist and
// what the Save button sends. Both are pure — the modal only wires inputs.
describe('priceRows', () => {
  it('lists every model any provider has, plus whatever is already priced', () => {
    const rows = priceRows(
      [
        { name: 'zai', models: ['z-ai-pro', 'z-ai-turbo'] },
        { name: 'openai', models: ['gpt-4o'] },
        { name: 'fresh' }, // no catalog yet
      ],
      [
        { model: 'retired-model', prompt_per_1m: 1, completion_per_1m: 2 }, // its provider is gone
        { model: 'gpt-4o', prompt_per_1m: 2.5, completion_per_1m: 10 },
      ],
    )
    expect(rows.map((row) => row.model)).toEqual(['gpt-4o', 'retired-model', 'z-ai-pro', 'z-ai-turbo'])
    expect(rows.find((row) => row.model === 'gpt-4o')?.providers).toEqual(['openai'])
    expect(rows.find((row) => row.model === 'retired-model')?.providers).toEqual([])
  })

  it('merges a model two providers share and skips blank names', () => {
    const rows = priceRows(
      [
        { name: 'a', models: ['shared', '   '] },
        { name: 'b', models: ['shared'] },
      ],
      [],
    )
    expect(rows).toEqual([{ model: 'shared', providers: ['a', 'b'] }])
  })
})

describe('buildPricePayload', () => {
  const rows = [
    { model: 'm1', providers: ['p'] },
    { model: 'm2', providers: [] as string[] },
  ]

  it('drops blank rows — their price goes back to "not counted"', () => {
    const { prices, invalid } = buildPricePayload(rows, {
      m1: { prompt: '', completion: '' },
      m2: { prompt: '1.5', completion: '' },
    })
    expect(invalid).toEqual([])
    expect(prices).toEqual([{ model: 'm2', prompt_per_1m: 1.5, completion_per_1m: 0 }])
  })

  it('reports typed values that are not non-negative numbers', () => {
    const { prices, invalid } = buildPricePayload(rows, {
      m1: { prompt: 'abc', completion: '2' },
      m2: { prompt: '-1', completion: '3' },
    })
    expect(prices).toEqual([])
    expect(invalid).toEqual(['m1', 'm2'])
  })

  it('tolerates spaces and an untouched draft row', () => {
    const { prices, invalid } = buildPricePayload(rows, { m1: { prompt: ' 2 ', completion: ' 3 ' } })
    expect(invalid).toEqual([])
    expect(prices).toEqual([{ model: 'm1', prompt_per_1m: 2, completion_per_1m: 3 }])
  })
})
