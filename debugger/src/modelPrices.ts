// Model token prices — the UI editor for what KERNEL_MODEL_PRICES used to be
// the only way to say (docs/plan-cost-accounting.md). The env value stays the
// seed: a list saved here replaces it (source "db") until it is cleared, and
// the kernel applies a save to the running process, so the next run is already
// priced by it.
//
// The key is the model id, exactly like the env table — one price per model,
// no provider dimension. The editor still groups rows by the providers that
// list them, because that is where an operator looks for a model.
import { KERNEL_API } from './kernelApi'
import { authHeaders } from './api'
import { reportKernelReachable, reportKernelTransportFailure } from './kernelAvailability'

export interface ModelPrice {
  model: string
  prompt_per_1m: number
  completion_per_1m: number
}

export interface ModelPriceList {
  prices: ModelPrice[]
  source: 'env' | 'db'
}

/** One editor row: a model and the providers that list it (a price can also
 *  outlive the provider that produced it — then the list is empty). */
export interface PriceRow {
  model: string
  providers: string[]
}

/** The two inputs as typed: strings keep editing smooth, parsing happens on save. */
export interface PriceDraft {
  prompt: string
  completion: string
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response
  try {
    response = await fetch(`${KERNEL_API}${path}`, {
      ...init,
      headers: { ...(init?.body ? { 'Content-Type': 'application/json' } : {}), ...authHeaders() },
    })
  } catch {
    reportKernelTransportFailure('unreachable')
    throw new Error('server unreachable — no connection')
  }
  if (!response.ok) {
    if (response.status === 502 || response.status === 503 || response.status === 504) {
      reportKernelTransportFailure('gateway')
    }
    const text = await response.text()
    let detail = `${response.status} ${text}`
    try {
      const payload = JSON.parse(text) as { error?: string }
      if (payload.error) detail = payload.error
    } catch { /* not JSON — the raw body is the diagnosis */ }
    throw new Error(detail)
  }
  reportKernelReachable()
  return response.json() as Promise<T>
}

export function modelPrices(): Promise<ModelPriceList> {
  return request('/v1/agent/cost/prices')
}

export function saveModelPrices(prices: ModelPrice[]): Promise<ModelPriceList> {
  return request('/v1/agent/cost/prices', { method: 'PUT', body: JSON.stringify({ prices }) })
}

/** Rows the editor renders: every model any provider lists, plus every model
 *  already priced. Sorted by model so the list is stable across reloads. */
export function priceRows(providers: { name: string; models?: string[] }[], prices: ModelPrice[]): PriceRow[] {
  const owners = new Map<string, Set<string>>()
  const add = (model: string, provider?: string) => {
    const key = model.trim()
    if (!key) return
    let names = owners.get(key)
    if (!names) {
      names = new Set<string>()
      owners.set(key, names)
    }
    if (provider) names.add(provider)
  }
  for (const provider of providers) {
    for (const model of provider.models ?? []) add(model, provider.name)
  }
  for (const price of prices) add(price.model)
  return [...owners.entries()]
    .map(([model, names]) => ({ model, providers: [...names].sort() }))
    .sort((left, right) => left.model.localeCompare(right.model))
}

export interface PricePayload {
  prices: ModelPrice[]
  /** Models whose typed value is not a non-negative number — the save must not go out with them. */
  invalid: string[]
}

/** Draft → payload. A row left blank drops its price (its cost goes back to
 *  "not counted"); a half-filled row reads the empty side as zero. */
export function buildPricePayload(rows: PriceRow[], draft: Record<string, PriceDraft>): PricePayload {
  const prices: ModelPrice[] = []
  const invalid: string[] = []
  for (const row of rows) {
    const entry = draft[row.model] ?? { prompt: '', completion: '' }
    const prompt = entry.prompt.trim()
    const completion = entry.completion.trim()
    if (prompt === '' && completion === '') continue
    const promptValue = prompt === '' ? 0 : Number(prompt)
    const completionValue = completion === '' ? 0 : Number(completion)
    if (!Number.isFinite(promptValue) || promptValue < 0 || !Number.isFinite(completionValue) || completionValue < 0) {
      invalid.push(row.model)
      continue
    }
    prices.push({ model: row.model, prompt_per_1m: promptValue, completion_per_1m: completionValue })
  }
  return { prices, invalid }
}
