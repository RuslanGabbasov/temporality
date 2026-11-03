import { useMemo } from 'react'
import { useT } from './i18n'
import { sideBySideDiff, type DiffCell } from './diffRows'

interface SideBySideDiffProps {
  title: string
  oldLabel: string
  newLabel: string
  oldText: string
  newText: string
}

function Cell({ cell, side }: { cell: DiffCell | null; side: 'old' | 'new' }) {
  const mark = side === 'old' ? '−' : '+'
  return (
    <>
      <td className={`diff-num${cell ? ` diff-${side}` : ' diff-empty'}`}>{cell?.lineNumber}</td>
      <td className={`diff-text${cell ? ` diff-${side}` : ' diff-empty'}`}>{cell && <><span className="diff-mark">{mark}</span>{cell.text}</>}</td>
    </>
  )
}

export default function SideBySideDiff({ title, oldLabel, newLabel, oldText, newText }: SideBySideDiffProps) {
  const t = useT()
  const { rows, added, removed } = useMemo(() => sideBySideDiff(oldText, newText), [oldText, newText])

  return (
    <div className="diff-block">
      <div className="diff-title">
        <span>{title}</span>
        <span className="diff-counters">
          <span className="diff-counter-added">+{added}</span>
          <span className="diff-counter-removed">−{removed}</span>
        </span>
      </div>
      {rows.length === 0 ? (
        <div className="diff-unchanged">{t('diff.no_changes') ?? 'No changes'}</div>
      ) : (
        <div className="diff-scroll">
          <table className="diff-table">
            <thead>
              <tr>
                <th colSpan={2}>{oldLabel}</th>
                <th colSpan={2}>{newLabel}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row, index) => row.kind === 'gap' ? (
                <tr key={index} className="diff-gap">
                  <td colSpan={4}>{t('diff.hidden_lines', { count: String(row.hidden) }) ?? `${row.hidden} unchanged lines`}</td>
                </tr>
              ) : (
                <tr key={index}>
                  {row.kind === 'same' ? (
                    <>
                      <td className="diff-num">{row.left.lineNumber}</td>
                      <td className="diff-text">{row.left.text}</td>
                      <td className="diff-num">{row.right.lineNumber}</td>
                      <td className="diff-text">{row.right.text}</td>
                    </>
                  ) : (
                    <>
                      <Cell cell={row.left} side="old" />
                      <Cell cell={row.right} side="new" />
                    </>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
