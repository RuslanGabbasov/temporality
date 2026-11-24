import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ScatterView } from './PresentationViz2'

// The competitive map's captions: the Y title stands vertically in the left
// margin (it used to lie horizontal across the tick labels, clipped to 12
// characters) and the legend row sits below the X title — the old `H - 8`
// landed on exactly the same baseline as the X caption.

const component = {
  data: {
    points: [
      { x: 1, y: 2, group: 'alpha' },
      { x: 2, y: 3, group: 'beta' },
    ],
    x_title: 'Reach',
    y_title: 'Impact of the partnership',
  },
  props: {},
}

function render(): string {
  return renderToStaticMarkup(<ScatterView component={component} />)
}

describe('ScatterView axis captions', () => {
  it('rotates the Y title along the left margin instead of laying it over the ticks', () => {
    const html = render()
    expect(html).toContain('rotate(-90)')
    // the vertical run has the whole plot height — no 12-character chop
    expect(html).toContain('Impact of the partnership')
  })

  it('keeps the legend row below the X title and inside the frame', () => {
    const html = render()
    const xTitleY = Number(/<text[^>]*\by="([0-9.]+)"[^>]*>Reach</.exec(html)?.[1])
    const legendY = Number(/transform="translate\([0-9.]+, ([0-9.]+)\)"/.exec(html)?.[1])
    const frameH = Number(/viewBox="0 0 [0-9.]+ ([0-9.]+)"/.exec(html)?.[1])
    expect(xTitleY).toBeGreaterThan(0)
    expect(legendY).toBeGreaterThan(xTitleY + 8)
    expect(legendY).toBeLessThan(frameH)
  })
})
