import { describe, expect, it } from 'vitest'
import { segmentMarkdown, diffWords, diffMarkdown } from './markdownDiff'

describe('segmentMarkdown', () => {
  it('splits on blank lines and classifies the blocks', () => {
    const blocks = segmentMarkdown([
      '# Заголовок',
      '',
      'Первый абзац — в две',
      'строки одного блока.',
      '',
      '- пункт один',
      '- пункт два',
      '',
      '```sh',
      'echo hi',
      '',
      'echo bye',
      '```',
      '',
      '| A | B |',
      '| - | - |',
    ].join('\n'))
    expect(blocks.map((b) => b.kind)).toEqual(['heading', 'paragraph', 'list', 'code', 'table'])
    // Blank lines inside a fence never split the code block.
    expect(blocks[3].text).toContain('echo bye')
    // The reflowed paragraph stays one block.
    expect(blocks[1].text).toContain('строки одного блока')
  })
})

describe('diffWords', () => {
  it('marks added and removed words inside the sentence', () => {
    const changes = diffWords('ветка fix/flaky-e2e не существует', 'ветка fix/flaky-e2e2 существует')
    const ops = changes.map((c) => c.op).join(',')
    expect(ops).toContain('del')
    expect(ops).toContain('add')
    const removed = changes.filter((c) => c.op === 'del').map((c) => c.text).join(' ')
    const added = changes.filter((c) => c.op === 'add').map((c) => c.text).join(' ')
    expect(removed).toContain('не')
    expect(added).toContain('flaky-e2e2')
  })

  it('keeps spaces between words when rebuilding the runs', () => {
    const changes = diffWords('Выкатка выполняется по вторникам через пайплайн.', 'Выкатка выполняется по четвергам через пайплайн.')
    const same = changes.filter((c) => c.op === 'same').map((c) => c.text).join('')
    expect(same).toContain('Выкатка выполняется по')
    expect(same).toContain('через пайплайн.')
  })

  it('reads as prose: emphasis markers do not leak into the story', () => {
    const changes = diffWords('Выкатка **по вторникам** через пайплайн.', 'Выкатка **по четвергам** через пайплайн.')
    const all = changes.map((c) => c.text).join('')
    expect(all).not.toContain('*')
    expect(changes.some((c) => c.op === 'del' && c.text.includes('вторникам'))).toBe(true)
    expect(changes.some((c) => c.op === 'add' && c.text.includes('четвергам'))).toBe(true)
  })
})

describe('diffMarkdown', () => {
  const oldDoc = [
    '# Регламент выкладки',
    '',
    'Выкатка выполняется по вторникам через пайплайн.',
    '',
    '## Откат',
    'Откат — командой rollback из репозитория.',
  ].join('\n')
  const newDoc = [
    '# Регламент выкладки',
    '',
    'Выкатка выполняется по четвергам через пайплайн.',
    '',
    '## Мониторинг',
    'После выкладки дежурный смотрит графики.',
    '',
    '## Откат',
    'Откат — командой rollback из репозитория.',
  ].join('\n')

  it('a reflowed/edited paragraph is ONE modified row with a word story', () => {
    const diff = diffMarkdown(oldDoc, newDoc)
    const modified = diff.rows.filter((r) => r.kind === 'modified')
    expect(modified).toHaveLength(1)
    expect(modified[0].changes?.some((c) => c.op === 'add' && c.text.includes('четвергам'))).toBe(true)
    expect(modified[0].changes?.some((c) => c.op === 'del' && c.text.includes('вторникам'))).toBe(true)
  })

  it('anchors unchanged blocks and reports added/removed ones', () => {
    const diff = diffMarkdown(oldDoc, newDoc)
    expect(diff.rows.filter((r) => r.kind === 'same').length).toBeGreaterThanOrEqual(2)
    // The new section (heading glued to its paragraph) arrives as one block.
    expect(diff.rows.filter((r) => r.kind === 'added')).toHaveLength(1)
    expect(diff.rows.filter((r) => r.kind === 'removed')).toHaveLength(0)
    expect(diff.counts).toEqual({
      same: diff.counts.same,
      added: 1,
      removed: 0,
      modified: 1,
    })
    const added = diff.rows.find((r) => r.kind === 'added')
    expect(added?.newBlock?.text).toContain('Мониторинг')
  })

  it('a completely different document reads as removals and additions', () => {
    const diff = diffMarkdown('Старый раздел про Jenkins.', 'Совсем другая инструкция про Kubernetes.')
    expect(diff.counts.modified).toBe(0)
    expect(diff.counts.added).toBe(1)
    expect(diff.counts.removed).toBe(1)
  })

  it('identical documents are all-same', () => {
    const diff = diffMarkdown(oldDoc, oldDoc)
    expect(diff.counts.added + diff.counts.removed + diff.counts.modified).toBe(0)
    expect(diff.counts.same).toBeGreaterThan(2)
  })
})
