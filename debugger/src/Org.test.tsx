import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Org, { allowsAnything, identitySummaryTags } from './Org'
import { I18nProvider } from './i18n'
import type { ExecutionIdentity } from './workspaceApi'

const t = (key: string) => key

function stubLocalStorage() {
  vi.stubGlobal('localStorage', {
    getItem: () => null,
    setItem: () => {},
    removeItem: () => {},
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Org', () => {
  it('renders the first-run organization wizard without crashing', () => {
    stubLocalStorage()
    const html = renderToStaticMarkup(
      <I18nProvider>
        <Org />
      </I18nProvider>,
    )
    // No units loaded yet (effects never run server-side) → the root-unit
    // wizard branch renders instead of the tree + inspector.
    expect(html).toContain('Set up your organization')
    expect(html).toContain('Create organization')
  })
})

describe('allowsAnything', () => {
  it('treats "*", empty and absent lists as allow-all', () => {
    expect(allowsAnything(['*'])).toBe(true)
    expect(allowsAnything([])).toBe(true)
    expect(allowsAnything(undefined)).toBe(true)
    expect(allowsAnything(null)).toBe(true)
  })

  it('treats concrete entries as restricted', () => {
    expect(allowsAnything(['coder'])).toBe(false)
    expect(allowsAnything(['coder', 'reviewer'])).toBe(false)
  })
})

describe('identitySummaryTags', () => {
  const identity = (mutate?: (i: ExecutionIdentity) => void): ExecutionIdentity => {
    const base: ExecutionIdentity = {
      id: 'exec-1',
      name: 'PR Reviewer',
      description: '',
      allowed_agents: ['*'],
      allowed_mcp: ['github', 'slack'],
      allowed_providers: [],
      allowed_projects: ['temporality'],
      human_targets: [],
      created_at: '2026-10-01T09:00:00Z',
      updated_at: '2026-10-01T09:00:00Z',
    }
    mutate?.(base)
    return base
  }

  it('summarizes each dimension: any vs concrete counts', () => {
    const tags = identitySummaryTags(identity(), t)
    expect(tags.map((x) => x.text)).toEqual([
      'org.identity_sum_agents: org.policy_any',
      'org.identity_sum_mcp: 2',
      'org.identity_sum_providers: org.policy_any',
      'org.identity_sum_projects: 1',
      'org.identity_sum_targets: org.policy_any',
    ])
  })

  it('always reports all five dimensions', () => {
    const tags = identitySummaryTags(identity((i) => {
      i.allowed_agents = ['coder', 'reviewer', 'tester']
      i.human_targets = ['ruslan']
    }), t)
    expect(tags).toHaveLength(5)
    expect(tags.find((x) => x.key === 'org.identity_sum_agents')?.text).toBe('org.identity_sum_agents: 3')
    expect(tags.find((x) => x.key === 'org.identity_sum_targets')?.text).toBe('org.identity_sum_targets: 1')
  })
})
