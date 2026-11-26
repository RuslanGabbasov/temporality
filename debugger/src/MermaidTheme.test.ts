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
    // The neo-look ER renderer reads rowEven/rowOdd (not the legacy vars
    // above): unset, the base theme derives rowOdd = lighten(primaryColor,
    // 75) — the «white text on light blue» dark-theme regression.
    expect(themeVariables.rowOdd).toMatch(/^rgba\(/)
    expect(themeVariables.rowEven).toMatch(/^rgba\(/)
  })

  it('keeps ER attribute rows inside the dark surface hierarchy', () => {
    // Same contract in the dark theme: both stripes must stay translucent
    // tints over the elevated box — never mermaid's derived pastel.
    document.documentElement.setAttribute('data-theme', 'dark')
    const { themeVariables } = mermaidTheme()
    expect(themeVariables.rowOdd).toMatch(/^rgba\(255,255,255,/)
    expect(themeVariables.rowEven).toMatch(/^rgba\(255,255,255,/)
  })

  it('follows the dark toggle', () => {
    document.documentElement.setAttribute('data-theme', 'dark')
    expect(mermaidTheme().darkMode).toBe(true)
  })

  it('covers every branch a mindmap can number', () => {
    // assignSections numbers root branches d%11 (0..10) and mermaid renders
    // section styles only up to THEME_COLOR_LIMIT-2: a shorter ramp leaves the
    // 8th+ branch with no fill rule — the box and the label both inherit the
    // canvas text color (cream text on a cream box, invisible).
    document.documentElement.setAttribute('data-theme', 'dark')
    document.documentElement.style.setProperty('--tm-chart-3', '#f1c21b')
    const { themeVariables } = mermaidTheme()
    expect(themeVariables.THEME_COLOR_LIMIT).toBe('12')
    for (let n = 0; n <= 11; n++) {
      expect(themeVariables[`cScale${n}`], `cScale${n}`).toBeTruthy()
      expect(themeVariables[`cScaleInv${n}`], `cScaleInv${n}`).toBeTruthy()
      expect(themeVariables[`cScaleLabel${n}`], `cScaleLabel${n}`).toBeTruthy()
    }
    // The yellow band takes dark text ON the band (dark theme labels are
    // per-fill now — the old uniform canvas color read as white on yellow).
    expect(themeVariables.cScale3).toBe('#f1c21b')
    expect(themeVariables.cScaleLabel3).toBe(readableOnFill(themeVariables.cScale3))
    expect(themeVariables.cScaleLabel3).toBe('#14201D')
    document.documentElement.style.removeProperty('--tm-chart-3')
  })
})
