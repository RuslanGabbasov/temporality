// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { mermaidTheme, readableOnFill } from './MermaidDiagram'

// The mermaid theme bridge contract: what the diagram painter receives.
describe('mermaidTheme', () => {
  it('keeps mindmap labels readable on the node boxes in light theme', () => {
    // The neo look fills node boxes with mainBkg — the label color must be
    // the canvas text color, NOT the band color (the «invisible text in the
    // light theme» regression: light labels on light boxes).
    document.documentElement.setAttribute('data-theme', 'light')
    const { darkMode, themeVariables } = mermaidTheme()
    expect(darkMode).toBe(false)
    expect(themeVariables.cScaleLabel1).toBe(themeVariables.textColor)
    expect(themeVariables.cScaleLabel8).toBe(themeVariables.textColor)
    // The root tspan sits ON the vivid teal box — its own contrast pick.
    expect(themeVariables.gitBranchLabel0).toBe(readableOnFill(themeVariables.git0))
    expect(themeVariables.cScaleLabel0).toBe(readableOnFill(themeVariables.git0))
    // ER attribute rows carry explicit tints (never the derived pastels).
    expect(themeVariables.attributeBackgroundColorEven).toBeTruthy()
    expect(themeVariables.attributeBackgroundColorOdd).toBeTruthy()
  })

  it('follows the dark toggle', () => {
    document.documentElement.setAttribute('data-theme', 'dark')
    expect(mermaidTheme().darkMode).toBe(true)
  })
})
