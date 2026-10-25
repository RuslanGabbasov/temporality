// Unified knowledge-topic taxonomy (one clustering for every surface):
// the same topic carries the same color in the knowledge map, the area
// treemap and the experience timeline, so a user learns the color language
// once instead of re-decoding a new palette per view.
//
// Colors are STABLE per topic name — derived by hashing, not by order of
// appearance — and the topic→color assignment never shifts when unrelated
// knowledge comes or goes.

/** Topic palette: distinct on both dark and light backgrounds. */
export const TOPIC_COLORS = [
  '#4f9cf9', // blue
  '#73daca', // teal
  '#e0af68', // amber
  '#bb9af7', // violet
  '#f7768e', // red
  '#9ece6a', // green
  '#ff9e64', // orange
  '#7dcfff', // cyan
  '#e8c07d', // sand
  '#b4f9f8', // light cyan
]

function hashTopic(topic: string): number {
  let h = 2166136261
  for (let i = 0; i < topic.length; i++) {
    h ^= topic.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

export function normalizeTopic(topic: string): string {
  return topic.trim().toLowerCase()
}

/** Stable color for one topic name — identical across all surfaces. */
export function topicColor(topic: string): string {
  const key = normalizeTopic(topic)
  if (!key) return '#7aa2f7'
  return TOPIC_COLORS[hashTopic(key) % TOPIC_COLORS.length]
}

/** The dominant topic of a set of knowledge items: the most frequent topic,
 *  with frequency ≥ 2 so a single loud item doesn't name a whole group. */
export function dominantTopic(
  memberTopics: Array<string[] | undefined>,
): string {
  const frequency = new Map<string, { count: number; display: string }>()
  for (const topics of memberTopics) {
    for (const topic of topics ?? []) {
      const key = normalizeTopic(topic)
      if (!key) continue
      const entry = frequency.get(key)
      if (entry) { entry.count++; continue }
      frequency.set(key, { count: 1, display: topic.trim() })
    }
  }
  let label = ''
  let bestCount = 1
  for (const [, entry] of frequency) {
    if (entry.count > bestCount) { bestCount = entry.count; label = entry.display }
  }
  return label
}

/** Up to two caption lines: the two most distinctive topics (same rationale
 *  as dominantTopic, one more entry). */
export function captionLines(
  memberTopics: Array<string[] | undefined>,
): string[] {
  const frequency = new Map<string, { count: number; display: string }>()
  for (const topics of memberTopics) {
    for (const topic of topics ?? []) {
      const key = normalizeTopic(topic)
      if (!key) continue
      const entry = frequency.get(key)
      if (entry) { entry.count++; continue }
      frequency.set(key, { count: 1, display: topic.trim() })
    }
  }
  const lines = [...frequency.values()]
    .filter((entry) => entry.count > 1)
    .sort((a, b) => b.count - a.count)
    .slice(0, 2)
    .map((entry) => entry.display)
  if (lines.length === 0) {
    const only = [...frequency.values()].sort((a, b) => b.count - a.count)[0]
    return only ? [only.display] : []
  }
  return lines
}

/** Topic of one knowledge item: the first normalized topic, '' when the item
 *  carries none. Used to bucket items per topic everywhere. */
export function itemTopic(topics: string[] | undefined): string {
  for (const topic of topics ?? []) {
    const key = normalizeTopic(topic)
    if (key) return key
  }
  return ''
}
