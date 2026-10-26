/** Word-form tolerant query matching for the knowledge map (and any other
 *  client-side text filter that wants «по словоформе», not exact substrings).
 *
 *  Every whitespace-separated query word must stem-match some word of the
 *  text. The stem rule is a shared prefix of at least STEM_FLOOR characters:
 *  «тест» matches «тесты»/«тестирование», «ставить» matches «ставят» (they
 *  diverge before the tail, plain prefix rules miss that), «run» matches
 *  «runs» — while «по» vs «пока» or «race» vs «rate» stay apart. Ids (hashes)
 *  stay exact-substring: splitting them into words is noise. */

const STEM_FLOOR = 3

/** Lowercase alphanumeric words; Cyrillic included via unicode ranges. */
export function tokenize(text: string): string[] {
  return text
    .toLowerCase()
    .split(/[^0-9a-zа-яё]+/i)
    .filter(Boolean)
}

export function stemsMatch(word: string, token: string): boolean {
  if (word === token) return true
  const max = Math.min(word.length, token.length)
  let shared = 0
  while (shared < max && word[shared] === token[shared]) shared++
  return shared >= STEM_FLOOR
}

/** True when every query word stem-matches the text. A wordless query
 *  (punctuation only) matches everything, like the empty query does. */
export function queryMatchesText(query: string, text: string): boolean {
  const words = tokenize(query)
  if (words.length === 0) return true
  const tokens = tokenize(text)
  return words.every((word) => tokens.some((token) => stemsMatch(word, token)))
}

/** The map's filter: word-form match against the proposition and the topics,
 *  plain substring against the id (ids are hashes, not prose). */
export function queryMatchesItem(query: string, item: { id: string; proposition: string; topics?: string[] }): boolean {
  if (queryMatchesText(query, item.proposition)) return true
  if (item.id.toLowerCase().includes(query.trim().toLowerCase())) return true
  return (item.topics ?? []).some((topic) => queryMatchesText(query, topic))
}
