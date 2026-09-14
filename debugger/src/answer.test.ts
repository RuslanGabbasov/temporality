import { describe, expect, it } from 'vitest'
import { extractModelAnswer } from './answer'

function response(emission: Record<string, unknown>) {
  return { emission }
}

describe('extractModelAnswer', () => {
  it('prefers an explicit answer reasoning item case-insensitively and preserves whitespace', () => {
    expect(extractModelAnswer(response({
      reasoning: [
        { kind: 'analysis', text: 'Internal thought' },
        { kind: ' AnSwEr ', text: 'First line\n\n**plain markdown**' },
      ],
      completion: 'Finished',
    }))).toEqual({ kind: 'answer', label: 'Model answer', text: 'First line\n\n**plain markdown**' })
  })

  it('uses a non-empty completion when there is no explicit answer', () => {
    expect(extractModelAnswer(response({ reasoning: [{ kind: 'analysis', text: 'Thought' }], completion: '  Complete\nnow  ' })))
      .toEqual({ kind: 'completion', label: 'Model answer', text: '  Complete\nnow  ' })
  })

  it('joins reasoning text as cognition fallback', () => {
    expect(extractModelAnswer(response({ reasoning: [{ kind: 'hypothesis', text: 'One' }, { kind: 'constraint', text: 'Two\nlines' }], completion: '  ' })))
      .toEqual({ kind: 'cognition', label: 'Model cognition (no explicit answer)', text: 'One\n\nTwo\nlines' })
  })

  it('returns an empty result when no usable answer exists', () => {
    expect(extractModelAnswer(response({ reasoning: [{ kind: 'answer', text: '  ' }], completion: null })))
      .toEqual({ kind: 'empty', label: 'Model answer', text: '' })
  })
})
