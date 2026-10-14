// UUID v4 generation that works over plain http on the LAN too.
// crypto.randomUUID() only exists in secure contexts (https / localhost); on
// http://host:3000 it is undefined and "New chat" / message branching crashed
// with "crypto.randomUUID is not a function". Fallback: manual RFC 4122 v4 via
// crypto.getRandomValues, which IS available in insecure contexts.
export function randomUUID(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  if (typeof crypto !== 'undefined' && typeof crypto.getRandomValues === 'function') {
    const b = crypto.getRandomValues(new Uint8Array(16))
    b[6] = (b[6] & 0x0f) | 0x40 // version 4
    b[8] = (b[8] & 0x3f) | 0x80 // variant 10
    const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
    return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
  }
  // Last resort for ancient runtimes; keeps the shape of a UUID so persisted
  // conversation ids stay uniform.
  return `id-${Date.now()}-${Math.random().toString(16).slice(2, 10)}`
}
