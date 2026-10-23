import { useEffect, useId, useRef, useState } from 'react'

/** Renders one ```mermaid fenced block as an SVG diagram.
 *
 *  The mermaid bundle (~1 MB) is loaded lazily on the first diagram — pages
 *  without diagrams never pay for it. Rendering failures (a malformed chart
 *  from a model) degrade to the chart source in a <pre>, never break the
 *  artifact. mermaid's own securityLevel stays 'strict': diagram labels are
 *  model-generated text and must not execute. */
export default function MermaidDiagram({ chart }: { chart: string }) {
  const t = { error: 'Diagram failed to render — showing the source' }
  const [svg, setSvg] = useState('')
  const [failed, setFailed] = useState(false)
  const id = useId().replace(/[^a-zA-Z0-9]/g, '')
  const rendered = useRef('')

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const mermaid = (await import('mermaid')).default
        mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'neutral' })
        const key = `mermaid-${id}-${chart.length}`
        const { svg: renderedSvg } = await mermaid.render(key, chart)
        if (cancelled) return
        rendered.current = chart
        setSvg(renderedSvg)
      } catch {
        if (!cancelled) setFailed(true)
      }
    })()
    return () => { cancelled = true }
  }, [chart, id])

  if (failed) {
    return (
      <div>
        <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)', marginBottom: '0.15rem' }}>{t.error}</div>
        <pre style={{ padding: '0.5rem', background: 'var(--tm-raised)', color: 'var(--tm-text-2)', fontSize: '0.72rem', fontFamily: '"SFMono-Regular", Consolas, monospace', maxHeight: '260px', overflow: 'auto', borderRadius: '4px', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{chart}</pre>
      </div>
    )
  }

  return (
    <div
      className="mermaid-diagram"
      data-chart={rendered.current ? undefined : 'pending'}
      // mermaid.render produces a self-contained SVG; securityLevel 'strict'
      // strips scripts and event handlers from model-generated labels.
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  )
}
