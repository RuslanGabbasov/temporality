// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ListSuggestInput, filterOptions, type SuggestOption } from './ListSuggestInput'

/** The identity dialog's allow-list input: click opens the dropdown with the
 *  full live option list, typing narrows it, a click adds the exact value.
 *  Free text stays possible; chosen values drop out of the suggestions. */

const OPTIONS: SuggestOption[] = [
  { value: 'coder', label: 'Coder', sub: 'agent' },
  { value: 'reviewer', label: 'Reviewer', sub: 'agent' },
  { value: 'github', label: 'GitHub MCP', sub: 'stdio' },
]

describe('filterOptions', () => {
  it('matches label, value and sub, case-insensitively', () => {
    expect(filterOptions(OPTIONS, '').map((o) => o.value)).toEqual(['coder', 'reviewer', 'github'])
    expect(filterOptions(OPTIONS, 'cod').map((o) => o.value)).toEqual(['coder'])
    expect(filterOptions(OPTIONS, 'Git').map((o) => o.value)).toEqual(['github'])
    expect(filterOptions(OPTIONS, 'agent').map((o) => o.value)).toEqual(['coder', 'reviewer'])
    expect(filterOptions(OPTIONS, 'zzz')).toEqual([])
  })
})

describe('ListSuggestInput', () => {
  let root: Root
  let container: HTMLElement
  let onChangeCalls: string[][]

  beforeEach(() => {
    document.body.innerHTML = ''
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    onChangeCalls = []
  })

  afterEach(() => {
    act(() => root.unmount())
  })

  // React controlled inputs ignore a bare value set + input event: the native
  // value setter must go through first.
  const type = (text: string) => {
    const el = container.querySelector('input') as HTMLInputElement
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!
    setter.call(el, text)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  }

  const render = (value: string[] = []) => {
    act(() => {
      root.render(
        <ListSuggestInput
          value={value}
          onChange={(next) => { onChangeCalls.push(next) }}
          options={OPTIONS}
          placeholder="pick or type — empty = any"
        />,
      )
    })
  }

  const input = () => container.querySelector('input') as HTMLInputElement
  const rows = () => [...container.querySelectorAll('.suggest-row')]

  it('renders chosen values as removable chips with their labels', () => {
    render(['coder', 'hand-typed-id'])
    const chips = [...container.querySelectorAll('.suggest-chip')]
    expect(chips.map((c) => c.textContent)).toEqual(['Coder', 'hand-typed-id'])
  })

  it('opens the full option list on focus and adds a picked value exactly', () => {
    render()
    act(() => input().focus())
    expect(rows()).toHaveLength(3)
    expect(rows().map((r) => r.querySelector('.suggest-row-value')!.textContent)).toEqual(['coder', 'reviewer', 'github'])

    act(() => (rows()[0] as HTMLElement).click())
    expect(onChangeCalls).toEqual([['coder']])
  })

  it('narrows suggestions as you type and drops already-chosen values', () => {
    render(['coder'])
    act(() => input().focus())
    // 'coder' is already chosen — only the other two are actionable.
    expect(rows().map((r) => r.querySelector('.suggest-row-value')!.textContent)).toEqual(['reviewer', 'github'])
    act(() => type('review'))
    expect(rows().map((r) => r.querySelector('.suggest-row-label')!.textContent)).toEqual(['Reviewer'])
  })

  it('commits hand-typed free text on Enter', () => {
    render()
    act(() => input().focus())
    act(() => type('future-agent'))
    act(() => input().dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
    expect(onChangeCalls).toEqual([['future-agent']])
  })
})
