import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { TemperatureControl } from './Agents'

/** The temperature control contract: a 0..1 slider with a manual input box —
 *  NOT a stepper NumberInput whose ± buttons fight the fraction. */
describe('TemperatureControl', () => {
  const html = (value: number) => renderToStaticMarkup(
    <TemperatureControl value={value} onChange={() => {}} label="Температура" />,
  )

  it('renders a slider with the manual input, bounded 0..1 with a fine step', () => {
    const markup = html(0.7)
    expect(markup).toContain('cds--slider')
    expect(markup).toContain('type="number"')
    expect(markup).toMatch(/min="0"/)
    expect(markup).toMatch(/max="1"/)
    expect(markup).toMatch(/step="0\.05"/)
    expect(markup).toMatch(/value="0\.7"/)
  })

  it('never renders the ± stepper buttons of a NumberInput', () => {
    const markup = html(0)
    expect(markup).not.toContain('cds--number__controls')
    expect(markup).not.toContain('cds--number input')
  })

  it('falls back to 0 for a missing/NaN value instead of NaN-crashing the slider', () => {
    expect(html(Number.NaN)).toMatch(/value="0"/)
  })
})
