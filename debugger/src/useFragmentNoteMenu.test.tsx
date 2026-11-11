// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it } from 'vitest'
import { useEffect, useRef } from 'react'
import { useFragmentNoteMenu } from './useFragmentNoteMenu'

function Harness({ enabled }: { enabled: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  const { menu } = useFragmentNoteMenu({ enabled, rootRef: ref })
  useEffect(() => { console.log('HARNESS RENDER menu=', !!menu, menu ? menu.quote : '') })
  return (
    <div>
      <div ref={ref} className="root"><p>Первый абзац с текстом.</p></div>
      {menu && <div className="menu" data-below={String(menu.below)}>{menu.quote}</div>}
    </div>
  )
}

describe('useFragmentNoteMenu', () => {
  it('opens on a selection inside the root and stays open across selectionchange', async () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    await act(async () => { root.render(<Harness enabled />) })

    const p = container.querySelector('p')!
    const range = document.createRange()
    range.setStart(p.firstChild!, 0)
    range.setEnd(p.firstChild!, 6)
    await act(async () => {
      const sel = window.getSelection()!
      sel.removeAllRanges()
      sel.addRange(range)
      document.dispatchEvent(new Event('selectionchange'))
      await new Promise((r) => setTimeout(r, 30))
      await act(async () => {})
    })
    await act(async () => {})
    console.log('HOOK menu=', !!container.querySelector('.menu'))
    expect(container.querySelector('.menu')).toBeTruthy()
    root.unmount()
  })
})
