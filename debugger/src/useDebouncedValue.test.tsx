// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TRACE_DEBOUNCE_MS, useDebouncedValue } from './useDebouncedValue'

// NFR-9: the debounce is the gate between a keystroke and any reaction to it
// (URL replaceState, pure client-side re-filtering). The value a consumer
// sees must lag the keystroke by ≥300 ms and land once — the timer restarts
// on every change, so a burst of keystrokes commits only the last value.

let root: Root
let container: HTMLElement

beforeEach(() => {
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  vi.useFakeTimers()
})

afterEach(async () => {
  vi.useRealTimers()
  await act(async () => root.unmount())
  container.remove()
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = false
})

const Probed = ({ value, delay }: { value: string; delay?: number }): React.ReactElement => {
  const committed = useDebouncedValue(value, delay)
  container.dataset.committed = committed
  return <span>{committed}</span>
}

const renderValue = async (value: string, delay?: number) => {
  await act(async () => root.render(<Probed value={value} delay={delay} />))
}

const committed = (): string => container.dataset.committed ?? ''

describe('useDebouncedValue (NFR-9)', () => {
  it('starts with the initial value without waiting', async () => {
    await renderValue('initial')
    expect(committed()).toBe('initial')
  })

  it(`does not commit before ${TRACE_DEBOUNCE_MS} ms, commits after`, async () => {
    const DEFAULT = TRACE_DEBOUNCE_MS
    await renderValue('initial')
    await renderValue('typed')
    expect(committed()).toBe('initial')
    await act(async () => { vi.advanceTimersByTime(DEFAULT - 1) })
    expect(committed()).toBe('initial')
    await act(async () => { vi.advanceTimersByTime(1) })
    expect(committed()).toBe('typed')
  })

  it('restarts the timer on every keystroke — a burst commits only the last value', async () => {
    await renderValue('initial')
    for (let i = 1; i <= 5; i += 1) {
      await renderValue(`text-${i}`)
      await act(async () => { vi.advanceTimersByTime(TRACE_DEBOUNCE_MS - 1) })
      expect(committed()).toBe('initial')
    }
    await act(async () => { vi.advanceTimersByTime(1) })
    expect(committed()).toBe('text-5')
  })

  it('supports a custom delay', async () => {
    await renderValue('initial', 50)
    await renderValue('quick', 50)
    await act(async () => { vi.advanceTimersByTime(49) })
    expect(committed()).toBe('initial')
    await act(async () => { vi.advanceTimersByTime(1) })
    expect(committed()).toBe('quick')
  })
})

describe('TRACE_DEBOUNCE_MS', () => {
  it('pins the §8/NFR-9 contract at 300 ms', () => {
    expect(TRACE_DEBOUNCE_MS).toBe(300)
  })
})
