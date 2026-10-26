import { describe, expect, it } from 'vitest'
import { queryMatchesItem, queryMatchesText, stemsMatch, tokenize } from './knowledgeQuery'

describe('tokenize', () => {
  it('splits on punctuation and case-folds, Cyrillic included', () => {
    expect(tokenize('Проверка, go test — тест!')).toEqual(['проверка', 'go', 'test', 'тест'])
  })
})

describe('stemsMatch', () => {
  it('matches word forms through the shared stem', () => {
    expect(stemsMatch('тест', 'тестирование')).toBe(true)
    expect(stemsMatch('тесты', 'тест')).toBe(true)
    expect(stemsMatch('прогон', 'прогона')).toBe(true)
    // Pairs that diverge before the tail — plain prefix rules miss these.
    expect(stemsMatch('ставить', 'ставят')).toBe(true)
    expect(stemsMatch('тестирования', 'тестирование')).toBe(true)
    expect(stemsMatch('run', 'runs')).toBe(true)
    expect(stemsMatch('runs', 'run')).toBe(true)
  })

  it('keeps short and unrelated words apart', () => {
    expect(stemsMatch('по', 'пока')).toBe(false)
    expect(stemsMatch('на', 'настройка')).toBe(false)
    expect(stemsMatch('race', 'rate')).toBe(false)
    expect(stemsMatch('тест', 'прогон')).toBe(false)
    expect(stemsMatch('по', 'по')).toBe(true)
  })
})

describe('queryMatchesText', () => {
  it('every query word must find a home in the text', () => {
    expect(queryMatchesText('тест прогон', 'запуск теста и прогона тестов')).toBe(true)
    expect(queryMatchesText('тест кот', 'запуск теста')).toBe(false)
  })

  it('a wordless query matches everything', () => {
    expect(queryMatchesText('   ', 'что угодно')).toBe(true)
    expect(queryMatchesText('—', 'что угодно')).toBe(true)
  })
})

describe('queryMatchesItem', () => {
  const item = {
    id: 'ext/847f200c3ef2f18e75d7945f',
    proposition: 'Тесты activities ставят KERNEL_FAULT_AFTER_EFFECT и требуют Cleanup',
    topics: ['тестирование', 'kernel'],
  }

  it('finds inflected forms in the proposition', () => {
    expect(queryMatchesItem('тест', item)).toBe(true)
    expect(queryMatchesItem('ставить', item)).toBe(true)
    expect(queryMatchesItem('kernel fault', item)).toBe(true)
  })

  it('finds topics by word form', () => {
    expect(queryMatchesItem('тестирования', item)).toBe(true)
    expect(queryMatchesItem('kernel', item)).toBe(true)
  })

  it('keeps id lookup as a plain substring', () => {
    expect(queryMatchesItem('847f200c', item)).toBe(true)
    expect(queryMatchesItem('847f', item)).toBe(true)
  })

  it('rejects a query no word answers', () => {
    expect(queryMatchesItem('деплой продакшена', item)).toBe(false)
  })
})
